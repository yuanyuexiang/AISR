// Package cursor implements provider.Provider for the Cursor Agent CLI.
//
// It drives `cursor-agent -p --output-format stream-json --force [--resume <id>]`
// and maps Cursor's NDJSON onto the unified provider.Event model.
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
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"

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
// --force is passed so headless turns don't deadlock on the workspace-trust gate
// or tool-approval prompts (consistent with Claude's -p full tool access; AISR is
// a local personal runtime on the user's own machine).
func (p *Provider) Send(ctx context.Context, opts provider.SessionOpts, prompt string) (*provider.Turn, error) {
	args := []string{"-p", prompt, "--output-format", "stream-json", "--force"}
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
		return nil, fmt.Errorf("cursor: stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("cursor: stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("cursor: start: %w", err)
	}

	ch := make(chan provider.Event)
	turn := &provider.Turn{Events: ch}

	go func() {
		defer close(ch)

		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
		for sc.Scan() {
			parseLine(sc.Bytes(), turn, ch)
		}

		errOut, _ := io.ReadAll(stderr)
		if waitErr := cmd.Wait(); waitErr != nil {
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
	Type      string          `json:"type"`
	Subtype   string          `json:"subtype"`
	SessionID string          `json:"session_id"`
	Message   json.RawMessage `json:"message"`
	Result    string          `json:"result"`
	IsError   bool            `json:"is_error"`
	Usage     json.RawMessage `json:"usage"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
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
		// Ignore "user" (Cursor echoes the prompt) and any future/unknown types.
	}
}

// emitContent fans an assistant message's content blocks into typed events.
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
		"session_id": ev.SessionID,
		"subtype":    ev.Subtype,
	})
	return b
}
