// Package claude implements provider.Provider for Claude Code.
//
// It drives `claude -p --output-format stream-json --verbose [--resume <id>]`
// and maps the NDJSON event stream onto the unified provider.Event model. The
// spawn/stream scaffolding lives in provider.StreamCommand; this file only builds
// the command and parses Claude's lines. Event schema and the --verbose
// requirement are documented in 技术方案.md §十.
package claude

import (
	"context"
	"encoding/json"
	"os"
	"strconv"

	"github.com/yuanyuexiang/aisr/internal/provider"
)

// Provider wraps the `claude` CLI.
type Provider struct {
	bin string
}

// New returns a ClaudeProvider. The binary is `claude` from PATH by default;
// override with AISR_CLAUDE_BIN (e.g. `claude.cmd` or a full path on Windows).
func New() *Provider {
	bin := os.Getenv("AISR_CLAUDE_BIN")
	if bin == "" {
		bin = "claude"
	}
	return &Provider{bin: bin}
}

func (p *Provider) Name() string { return "claude" }

func (p *Provider) Capabilities() provider.Capabilities {
	return provider.Capabilities{
		StructuredOutput: true,
		Streaming:        true,
		Resume:           true,
		ToolUse:          true,
		MCP:              true, // honors SessionOpts.Agent.MCPConfig via --mcp-config
	}
}

// Send spawns one headless claude turn and streams normalized events.
func (p *Provider) Send(ctx context.Context, opts provider.SessionOpts, prompt string) (*provider.Turn, error) {
	var env []string
	if opts.Agent != nil {
		env = envSlice(opts.Agent.Env)
	}
	// Claude keeps the prompt in argv: claude is a native binary (no shell shim),
	// so CreateProcess carries a multi-line prompt intact — no stdin needed.
	return provider.StreamCommand(ctx, p.bin, buildArgs(opts, prompt), opts.Workspace, env, "", parseLine)
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

// buildArgs assembles the claude argv for one headless turn, including any
// agent-mode flags. Pure (no I/O) so it is unit-tested directly.
//
// Variadic flags (--allowedTools / --disallowedTools / --add-dir) take all their
// values after a single flag token; the CLI stops consuming at the next "--flag".
// --mcp-config accepts inline JSON as a string (not only a file path).
func buildArgs(opts provider.SessionOpts, prompt string) []string {
	args := []string{"-p", prompt, "--output-format", "stream-json", "--verbose"}
	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
	}
	if opts.SessionID != "" {
		args = append(args, "--resume", opts.SessionID)
	}
	a := opts.Agent
	if a == nil {
		return args
	}
	if a.SystemPrompt != "" {
		args = append(args, "--system-prompt", a.SystemPrompt)
	}
	if a.AppendSystemPrompt != "" {
		args = append(args, "--append-system-prompt", a.AppendSystemPrompt)
	}
	if a.MaxTurns > 0 {
		args = append(args, "--max-turns", strconv.Itoa(a.MaxTurns))
	}
	if a.PermissionMode != "" {
		args = append(args, "--permission-mode", a.PermissionMode)
	}
	if len(a.MCPConfig) > 0 {
		args = append(args, "--mcp-config", string(a.MCPConfig))
	}
	if len(a.AllowedTools) > 0 {
		args = append(args, "--allowedTools")
		args = append(args, a.AllowedTools...)
	}
	if len(a.DisallowedTools) > 0 {
		args = append(args, "--disallowedTools")
		args = append(args, a.DisallowedTools...)
	}
	if len(a.AddDirs) > 0 {
		args = append(args, "--add-dir")
		args = append(args, a.AddDirs...)
	}
	return args
}

// --- stream-json parsing ---

type streamLine struct {
	Type       string          `json:"type"`
	Subtype    string          `json:"subtype"`
	SessionID  string          `json:"session_id"`
	Message    json.RawMessage `json:"message"`
	Result     string          `json:"result"`
	StopReason string          `json:"stop_reason"`
	IsError    bool            `json:"is_error"`
	Usage      json.RawMessage `json:"usage"`
}

func parseLine(line []byte, turn *provider.Turn, ch chan<- provider.Event) {
	var ev streamLine
	if err := json.Unmarshal(line, &ev); err != nil {
		return // ignore non-JSON / partial lines
	}

	switch ev.Type {
	case "system":
		// init event carries the session id (stable across --resume).
		if ev.SessionID != "" {
			turn.SessionID = ev.SessionID
		}

	case "assistant", "user":
		// For Claude, "user" carries tool results; both are content arrays.
		provider.EmitContent(ev.Message, ch)

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
		// rate_limit_event and any future types: ignore for forward-compat.
	}
}

func rawOf(ev streamLine) json.RawMessage {
	b, _ := json.Marshal(map[string]any{
		"session_id":  ev.SessionID,
		"stop_reason": ev.StopReason,
	})
	return b
}
