package provider

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
)

// ParseFunc maps one raw NDJSON line from a provider's stdout into events,
// updating the Turn (e.g. capturing the session id). Lines that aren't
// recognized should be ignored.
type ParseFunc func(line []byte, turn *Turn, ch chan<- Event)

// StreamCommand runs `bin args...` (cwd = workspace, if set; env merged over the
// daemon's own environment when non-nil), scanning its stdout line by line
// through parse and emitting the resulting events on the returned Turn. It is the
// shared scaffolding for headless, line-structured CLI providers (Claude, Cursor,
// …): spawn, stream, then surface a non-zero exit as an error event. stderr is
// captured and used as the error message when the process fails.
//
// When stdin is non-empty it is fed to the child on its standard input (and the
// pipe is closed at EOF). Providers use this to pass a multi-line prompt out of
// band instead of as an argv element — argv can't safely carry newlines through a
// Windows .cmd/.bat shim (e.g. cursor-agent), which mangles the split args.
func StreamCommand(ctx context.Context, bin string, args []string, workspace string, env []string, stdin string, parse ParseFunc) (*Turn, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	if workspace != "" {
		cmd.Dir = workspace
	}
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("%s: stdout pipe: %w", bin, err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("%s: stderr pipe: %w", bin, err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("%s: start: %w", bin, err)
	}
	slog.Debug("spawn cli", "bin", bin, "cwd", workspace, "argv", capArgs(args))

	ch := make(chan Event)
	turn := &Turn{Events: ch}
	go func() {
		defer close(ch)
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024) // init/usage lines can be large
		for sc.Scan() {
			parse(sc.Bytes(), turn, ch)
		}
		errOut, _ := io.ReadAll(stderr)
		if waitErr := cmd.Wait(); waitErr != nil {
			// A non-zero exit caused by us cancelling the context (an explicit
			// /cancel, or the client disconnecting) is intentional, not a turn
			// failure — let the stream just close instead of surfacing an error.
			if ctx.Err() != nil {
				return
			}
			msg := waitErr.Error()
			if len(errOut) > 0 {
				msg = string(errOut)
			}
			ch <- Event{Kind: EventError, Text: msg}
		}
	}()
	return turn, nil
}

// capArgs joins argv for a debug log line, truncating so a huge system prompt or
// user prompt doesn't flood the log. Individual args are capped too.
func capArgs(args []string) string {
	const perArg, total = 120, 600
	parts := make([]string, len(args))
	for i, a := range args {
		if len(a) > perArg {
			a = a[:perArg] + "…"
		}
		parts[i] = a
	}
	s := strings.Join(parts, " ")
	if len(s) > total {
		s = s[:total] + "…"
	}
	return s
}

// EmitContent fans an Anthropic-style message's content blocks into typed events.
// Shared by providers whose assistant/tool messages use content arrays of
// {type:"text"} / {type:"tool_use"} / {type:"tool_result"} blocks.
func EmitContent(message json.RawMessage, ch chan<- Event) {
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
		var cb struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			Thinking string `json:"thinking"`
		}
		if err := json.Unmarshal(b, &cb); err != nil {
			continue
		}
		switch cb.Type {
		case "text":
			ch <- Event{Kind: EventText, Text: cb.Text}
		case "thinking":
			// Extended-thinking block: the prose is in "thinking"; keep the raw
			// block too (it carries the signature).
			ch <- Event{Kind: EventThinking, Text: cb.Thinking, Raw: b}
		case "tool_use":
			ch <- Event{Kind: EventToolUse, Raw: b}
		case "tool_result":
			ch <- Event{Kind: EventToolResult, Raw: b}
		}
	}
}
