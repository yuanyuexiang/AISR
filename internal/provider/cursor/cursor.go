// Package cursor implements provider.Provider for the Cursor Agent CLI.
//
// It drives `cursor-agent -p --output-format stream-json --force [--resume <id>]`
// and maps Cursor's NDJSON onto the unified provider.Event model. The spawn/stream
// scaffolding lives in provider.StreamCommand; this file only builds the command
// and parses Cursor's lines.
//
// Cursor's stream-json is close to Claude's but differs in two ways found via the
// spike (see 技术方案.md §十):
//   - it requires --force (or --trust) to clear the workspace-trust gate in
//     non-interactive mode;
//   - it emits a {"type":"user"} event echoing the prompt, which must be IGNORED
//     (unlike Claude, where "user" carries tool results).
//
// Auth is the logged-in Cursor account (apiKeySource:"login"), so no API key —
// matching AISR's zero-key premise.
package cursor

import (
	"context"
	"encoding/json"

	"github.com/yuanyuexiang/aisr/internal/provider"
)

// Provider wraps the `cursor-agent` CLI.
type Provider struct {
	bin string
}

// New returns a CursorProvider using `cursor-agent` from PATH.
func New() *Provider { return &Provider{bin: "cursor-agent"} }

func (p *Provider) Name() string { return "cursor" }

func (p *Provider) Capabilities() provider.Capabilities {
	return provider.Capabilities{
		StructuredOutput: true,
		Streaming:        true,
		Resume:           true,
		ToolUse:          true,
		MCP:              false,
	}
}

// Send spawns one headless cursor-agent turn and streams normalized events.
//
// --force clears the workspace-trust gate and allows tools in headless mode so
// turns don't deadlock on prompts (consistent with Claude's -p full tool access;
// AISR is a local personal runtime on the user's own machine).
func (p *Provider) Send(ctx context.Context, opts provider.SessionOpts, prompt string) (*provider.Turn, error) {
	args := []string{"-p", prompt, "--output-format", "stream-json", "--force"}
	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
	}
	if opts.SessionID != "" {
		args = append(args, "--resume", opts.SessionID)
	}
	return provider.StreamCommand(ctx, p.bin, args, opts.Workspace, parseLine)
}

// --- stream-json parsing ---

type streamLine struct {
	Type      string          `json:"type"`
	Subtype   string          `json:"subtype"`
	SessionID string          `json:"session_id"`
	Message   json.RawMessage `json:"message"`
	Result    string          `json:"result"`
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
		// Ignore "user" (Cursor echoes the prompt) and any future/unknown types.
	}
}

func rawOf(ev streamLine) json.RawMessage {
	b, _ := json.Marshal(map[string]any{
		"session_id": ev.SessionID,
		"subtype":    ev.Subtype,
	})
	return b
}
