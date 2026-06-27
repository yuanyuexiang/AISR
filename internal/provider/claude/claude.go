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

	"github.com/yuanyuexiang/aisr/internal/provider"
)

// Provider wraps the `claude` CLI.
type Provider struct {
	bin string
}

// New returns a ClaudeProvider using `claude` from PATH.
func New() *Provider { return &Provider{bin: "claude"} }

func (p *Provider) Name() string { return "claude" }

func (p *Provider) Capabilities() provider.Capabilities {
	return provider.Capabilities{
		StructuredOutput: true,
		Streaming:        true,
		Resume:           true,
		ToolUse:          true,
		MCP:              false,
	}
}

// Send spawns one headless claude turn and streams normalized events.
func (p *Provider) Send(ctx context.Context, opts provider.SessionOpts, prompt string) (*provider.Turn, error) {
	args := []string{"-p", prompt, "--output-format", "stream-json", "--verbose"}
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
