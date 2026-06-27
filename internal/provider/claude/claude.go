// Package claude implements provider.Provider for Claude Code.
//
// It drives `claude -p --output-format stream-json --verbose [--resume <id>]`
// and maps the NDJSON event stream onto the unified provider.Event model.
// Event schema and the --verbose requirement are documented in 技术方案.md §十.
package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"

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

	cmd := exec.CommandContext(ctx, p.bin, args...)
	if opts.Workspace != "" {
		cmd.Dir = opts.Workspace
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("claude: stdout pipe: %w", err)
	}
	// claude prints diagnostics to stdout as JSON; stderr carries hard failures.
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("claude: stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("claude: start: %w", err)
	}

	ch := make(chan provider.Event)
	turn := &provider.Turn{Events: ch}

	go func() {
		defer close(ch)

		sc := bufio.NewScanner(stdout)
		// stream-json lines (esp. the init event) can be large.
		sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
		for sc.Scan() {
			parseLine(sc.Bytes(), turn, ch)
		}

		errOut, _ := io.ReadAll(stderr)
		waitErr := cmd.Wait()
		if waitErr != nil {
			msg := waitErr.Error()
			if len(errOut) > 0 {
				msg = string(errOut)
			}
			ch <- provider.Event{Kind: provider.EventError, Text: msg}
		}
	}()

	return turn, nil
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

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
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
		emitContent(ev.Message, ch)

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

// emitContent fans a message's content blocks out into typed events.
func emitContent(message json.RawMessage, ch chan<- provider.Event) {
	if len(message) == 0 {
		return
	}
	var msg struct {
		Content []json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(message, &msg); err != nil {
		return
	}
	for _, b := range msg.Content {
		var cb contentBlock
		if err := json.Unmarshal(b, &cb); err != nil {
			continue
		}
		switch cb.Type {
		case "text":
			ch <- provider.Event{Kind: provider.EventText, Text: cb.Text}
		case "tool_use":
			ch <- provider.Event{Kind: provider.EventToolUse, Raw: b}
		case "tool_result":
			ch <- provider.Event{Kind: provider.EventToolResult, Raw: b}
		}
	}
}

func rawOf(ev streamLine) json.RawMessage {
	b, _ := json.Marshal(map[string]any{
		"session_id":  ev.SessionID,
		"stop_reason": ev.StopReason,
	})
	return b
}
