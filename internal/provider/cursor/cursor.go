// Package cursor implements provider.Provider for the Cursor Agent CLI.
//
// It drives `cursor-agent -p --output-format stream-json --force [--resume <id>]`
// and maps Cursor's NDJSON onto the unified provider.Event model.
//
// Cursor's stream-json differs from Claude's in a few ways found via the spike
// (see 技术方案.md §十):
//   - it requires --force (allow commands) and, in agent mode, --approve-mcps
//     (auto-approve MCP servers) + --trust (trust the workspace headless);
//   - it emits a {"type":"user"} event echoing the prompt, which is IGNORED;
//   - tool calls arrive as {"type":"tool_call", subtype:"started"|"completed"}
//     events (NOT Anthropic content blocks), each wrapping ONE tool kind
//     (mcpToolCall / readToolCall / shellToolCall / …). We normalize these to
//     tool_use (started) + tool_result (completed).
//
// Agent mode (SessionOpts.Agent) is honored differently than Claude because
// cursor-agent's knobs differ:
//   - MCP servers: written to <workspace>/.cursor/mcp.json (file-based, not a
//     CLI flag) from Agent.MCPConfig — same {"mcpServers":{…}} shape;
//   - system prompt: PREPENDED to the prompt (no --system-prompt flag);
//   - Agent.AddDirs / AllowedTools / PermissionMode: not expressible in
//     cursor-agent, so they're dropped (cursor has a single --workspace and a
//     coarse --force gate). Prefer Claude for fine tool-scoped read-only personas.
//
// Auth is the logged-in Cursor account (no API key) — matching AISR's premise.
package cursor

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/yuanyuexiang/aisr/internal/provider"
)

// Provider wraps the `cursor-agent` CLI.
type Provider struct {
	bin string
}

// New returns a CursorProvider. The binary is `cursor-agent` from PATH by
// default; override with AISR_CURSOR_BIN (e.g. `cursor-agent.cmd` on Windows).
func New() *Provider {
	bin := os.Getenv("AISR_CURSOR_BIN")
	if bin == "" {
		bin = "cursor-agent"
	}
	return &Provider{bin: bin}
}

func (p *Provider) Name() string { return "cursor" }

func (p *Provider) Capabilities() provider.Capabilities {
	return provider.Capabilities{
		StructuredOutput: true,
		Streaming:        true,
		Resume:           true,
		ToolUse:          true,
		MCP:              true, // via <workspace>/.cursor/mcp.json + --approve-mcps
	}
}

// Send spawns one headless cursor-agent turn and streams normalized events.
func (p *Provider) Send(ctx context.Context, opts provider.SessionOpts, prompt string) (*provider.Turn, error) {
	workspace := opts.Workspace
	var env []string
	if a := opts.Agent; a != nil {
		env = envSlice(a.Env)
		prompt = prependSystemPrompt(a, prompt)
		if len(a.MCPConfig) > 0 {
			ws, err := writeMCPConfig(ctx, workspace, a.MCPConfig)
			if err != nil {
				return nil, err
			}
			workspace = ws
		}
	}
	return provider.StreamCommand(ctx, p.bin, buildArgs(opts, prompt, workspace), workspace, env, parseLine)
}

// buildArgs assembles the cursor-agent argv. Pure — unit-tested directly.
func buildArgs(opts provider.SessionOpts, prompt, workspace string) []string {
	args := []string{"-p", prompt, "--output-format", "stream-json", "--force"}
	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
	}
	if opts.SessionID != "" {
		args = append(args, "--resume", opts.SessionID)
	}
	if opts.Agent != nil {
		// Headless agent mode: auto-approve MCP servers + trust the workspace so
		// the run doesn't block on prompts. The --workspace holds .cursor/mcp.json.
		args = append(args, "--approve-mcps", "--trust")
		if workspace != "" {
			args = append(args, "--workspace", workspace)
		}
	}
	return args
}

// prependSystemPrompt folds the agent's system prompt into the user prompt
// (cursor-agent has no --system-prompt / --append-system-prompt flag).
func prependSystemPrompt(a *provider.AgentOptions, prompt string) string {
	var sb strings.Builder
	if a.SystemPrompt != "" {
		sb.WriteString(a.SystemPrompt)
		sb.WriteString("\n\n")
	}
	if a.AppendSystemPrompt != "" {
		sb.WriteString(a.AppendSystemPrompt)
		sb.WriteString("\n\n")
	}
	sb.WriteString(prompt)
	return sb.String()
}

// writeMCPConfig writes Agent.MCPConfig to <workspace>/.cursor/mcp.json (the file
// cursor-agent reads). If no workspace was given, it creates a per-turn temp dir
// and schedules its removal when the turn's context ends.
func writeMCPConfig(ctx context.Context, workspace string, mcpConfig json.RawMessage) (string, error) {
	ws := workspace
	if ws == "" {
		tmp, err := os.MkdirTemp("", "aisr-cursor-")
		if err != nil {
			return "", err
		}
		ws = tmp
		go func() { <-ctx.Done(); _ = os.RemoveAll(tmp) }()
	}
	dir := filepath.Join(ws, ".cursor")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "mcp.json"), mcpConfig, 0o600); err != nil {
		return "", err
	}
	return ws, nil
}

// envSlice flattens an env map into "K=V" entries (nil for an empty map).
func envSlice(m map[string]string) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	return out
}

// --- stream-json parsing ---

type streamLine struct {
	Type      string          `json:"type"`
	Subtype   string          `json:"subtype"`
	SessionID string          `json:"session_id"`
	Message   json.RawMessage `json:"message"`
	Result    string          `json:"result"` // final text (result event only)
	CallID    string          `json:"call_id"`
	ToolCall  json.RawMessage `json:"tool_call"`
	IsError   bool            `json:"is_error"`
	Usage     json.RawMessage `json:"usage"`
}

func parseLine(line []byte, turn *provider.Turn, ch chan<- provider.Event) {
	var ev streamLine
	if err := json.Unmarshal(line, &ev); err != nil {
		return
	}

	switch ev.Type {
	case "system":
		if ev.SessionID != "" {
			turn.SessionID = ev.SessionID // chatId, stable across --resume
		}

	case "assistant":
		provider.EmitContent(ev.Message, ch) // text blocks (tool calls come separately)

	case "tool_call":
		emitToolCall(ev, ch)

	case "result":
		if ev.SessionID != "" {
			turn.SessionID = ev.SessionID
		}
		if len(ev.Usage) > 0 {
			ch <- provider.Event{Kind: provider.EventUsage, Raw: ev.Usage}
		}
		if ev.IsError {
			ch <- provider.Event{Kind: provider.EventError, Text: ev.Result, Raw: rawOf(ev)}
			return
		}
		ch <- provider.Event{Kind: provider.EventDone, Raw: rawOf(ev)}

	default:
		// Ignore "user" (Cursor echoes the prompt) and any future/unknown types.
	}
}

// emitToolCall normalizes a cursor tool_call event into tool_use (started) /
// tool_result (completed). Each event wraps exactly one "<kind>ToolCall".
func emitToolCall(ev streamLine, ch chan<- provider.Event) {
	if len(ev.ToolCall) == 0 {
		return
	}
	var wrap map[string]json.RawMessage
	if json.Unmarshal(ev.ToolCall, &wrap) != nil {
		return
	}
	for key, raw := range wrap {
		if !strings.HasSuffix(key, "ToolCall") {
			continue
		}
		if key == "mcpToolCall" {
			emitMCPToolCall(ev.Subtype, ev.CallID, raw, ch)
		} else {
			emitBuiltinToolCall(ev.Subtype, ev.CallID, key, raw, ch)
		}
		return
	}
}

// emitMCPToolCall maps an MCP tool call → mcp__<provider>__<tool>, extracting the
// nested result content on completion.
func emitMCPToolCall(subtype, callID string, raw json.RawMessage, ch chan<- provider.Event) {
	var m struct {
		Args struct {
			ProviderIdentifier string          `json:"providerIdentifier"`
			ToolName           string          `json:"toolName"`
			Args               json.RawMessage `json:"args"`
		} `json:"args"`
		Result struct {
			Success struct {
				Content []struct {
					Text struct {
						Text string `json:"text"`
					} `json:"text"`
				} `json:"content"`
				IsError bool `json:"isError"`
			} `json:"success"`
		} `json:"result"`
	}
	if json.Unmarshal(raw, &m) != nil {
		return
	}
	name := "mcp__" + m.Args.ProviderIdentifier + "__" + m.Args.ToolName
	switch subtype {
	case "started":
		ch <- provider.Event{Kind: provider.EventToolUse, Raw: toolUseRaw(callID, name, m.Args.Args)}
	case "completed":
		var text strings.Builder
		for _, c := range m.Result.Success.Content {
			text.WriteString(c.Text.Text)
		}
		ch <- provider.Event{Kind: provider.EventToolResult, Raw: toolResultRaw(callID, text.String(), m.Result.Success.IsError)}
	}
}

// emitBuiltinToolCall maps a built-in tool call (readToolCall / shellToolCall /
// …) → a generic tool_use/tool_result named by the tool kind (read/shell/…).
func emitBuiltinToolCall(subtype, callID, wrapperKey string, raw json.RawMessage, ch chan<- provider.Event) {
	name := strings.TrimSuffix(wrapperKey, "ToolCall")
	var m struct {
		Args   json.RawMessage `json:"args"`
		Result json.RawMessage `json:"result"`
	}
	_ = json.Unmarshal(raw, &m)
	switch subtype {
	case "started":
		ch <- provider.Event{Kind: provider.EventToolUse, Raw: toolUseRaw(callID, name, m.Args)}
	case "completed":
		content := ""
		if len(m.Result) > 0 {
			content = string(m.Result)
		}
		ch <- provider.Event{Kind: provider.EventToolResult, Raw: toolResultRaw(callID, content, false)}
	}
}

func toolUseRaw(id, name string, input json.RawMessage) json.RawMessage {
	if len(input) == 0 {
		input = json.RawMessage("{}")
	}
	b, _ := json.Marshal(map[string]any{"type": "tool_use", "id": id, "name": name, "input": input})
	return b
}

func toolResultRaw(id, content string, isErr bool) json.RawMessage {
	b, _ := json.Marshal(map[string]any{"type": "tool_result", "tool_use_id": id, "content": content, "is_error": isErr})
	return b
}

func rawOf(ev streamLine) json.RawMessage {
	b, _ := json.Marshal(map[string]any{
		"session_id": ev.SessionID,
		"subtype":    ev.Subtype,
	})
	return b
}
