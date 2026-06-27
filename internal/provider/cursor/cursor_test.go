package cursor

import (
	"testing"

	"github.com/yuanyuexiang/aisr/internal/provider"
)

func collect(lines []string) (*provider.Turn, []provider.Event) {
	ch := make(chan provider.Event, 64)
	turn := &provider.Turn{Events: ch}
	for _, ln := range lines {
		parseLine([]byte(ln), turn, ch)
	}
	close(ch)
	var evs []provider.Event
	for e := range ch {
		evs = append(evs, e)
	}
	return turn, evs
}

func kinds(evs []provider.Event) []provider.EventKind {
	out := make([]provider.EventKind, len(evs))
	for i, e := range evs {
		out[i] = e.Kind
	}
	return out
}

// Fixtures shaped after real `cursor-agent ... --output-format stream-json` output
// (see 技术方案.md §十). Note the {"type":"user"} echo line.
func TestParseHappyPath(t *testing.T) {
	lines := []string{
		`{"type":"system","subtype":"init","apiKeySource":"login","session_id":"c-1","model":"Composer 2.5 Fast"}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"用一句话回答:1+1等于几"}]},"session_id":"c-1"}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"1+1等于2。"}]},"session_id":"c-1"}`,
		`{"type":"result","subtype":"success","is_error":false,"result":"1+1等于2。","session_id":"c-1","usage":{"inputTokens":6967,"outputTokens":63}}`,
	}
	turn, evs := collect(lines)

	if turn.SessionID != "c-1" {
		t.Fatalf("session id = %q, want c-1", turn.SessionID)
	}
	want := []provider.EventKind{provider.EventText, provider.EventUsage, provider.EventDone}
	if got := kinds(evs); len(got) != len(want) {
		t.Fatalf("kinds = %v, want %v (the user echo must be ignored)", got, want)
	}
	for i, k := range want {
		if evs[i].Kind != k {
			t.Errorf("event[%d].Kind = %q, want %q", i, evs[i].Kind, k)
		}
	}
	if evs[0].Text != "1+1等于2。" {
		t.Errorf("text = %q, want 1+1等于2。", evs[0].Text)
	}
}

// The cursor-specific bug guard: a {"type":"user"} event echoes the prompt and
// must NOT be emitted as model output.
func TestUserEchoIgnored(t *testing.T) {
	lines := []string{
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"don't echo me"}]},"session_id":"c-1"}`,
	}
	_, evs := collect(lines)
	if len(evs) != 0 {
		t.Fatalf("user echo should produce no events, got %v with text %q", kinds(evs), evs[0].Text)
	}
}

func TestParseToolUse(t *testing.T) {
	lines := []string{
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"looking"},{"type":"tool_use","name":"read_file","input":{"path":"x"}}]}}`,
	}
	_, evs := collect(lines)
	want := []provider.EventKind{provider.EventText, provider.EventToolUse}
	if got := kinds(evs); len(got) != len(want) || got[1] != provider.EventToolUse {
		t.Fatalf("kinds = %v, want %v", got, want)
	}
}

func TestParseResultError(t *testing.T) {
	lines := []string{
		`{"type":"result","subtype":"error","is_error":true,"result":"boom","session_id":"c-2"}`,
	}
	turn, evs := collect(lines)
	if turn.SessionID != "c-2" {
		t.Errorf("session id = %q, want c-2", turn.SessionID)
	}
	if got := kinds(evs); len(got) != 1 || got[0] != provider.EventError {
		t.Fatalf("kinds = %v, want [error]", got)
	}
	if evs[0].Text != "boom" {
		t.Errorf("error text = %q, want boom", evs[0].Text)
	}
}
