package claude

import (
	"testing"

	"github.com/yuanyuexiang/aisr/internal/provider"
)

// collect runs each NDJSON line through parseLine and returns the captured
// session id plus the events emitted, mirroring the streaming consumer.
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

// Fixtures shaped after real `claude ... --output-format stream-json --verbose`
// output (see 技术方案.md §十).
func TestParseHappyPath(t *testing.T) {
	lines := []string{
		`{"type":"system","subtype":"init","session_id":"sess-1","model":"claude-opus-4-8","apiKeySource":"none"}`,
		`{"type":"rate_limit_event","rate_limit_info":{"status":"allowed"},"session_id":"sess-1"}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"2。"}]},"session_id":"sess-1"}`,
		`{"type":"result","subtype":"success","is_error":false,"result":"2。","stop_reason":"end_turn","session_id":"sess-1","usage":{"input_tokens":2917,"output_tokens":4}}`,
	}
	turn, evs := collect(lines)

	if turn.SessionID != "sess-1" {
		t.Fatalf("session id = %q, want %q", turn.SessionID, "sess-1")
	}
	want := []provider.EventKind{provider.EventText, provider.EventUsage, provider.EventDone}
	if got := kinds(evs); len(got) != len(want) {
		t.Fatalf("kinds = %v, want %v", got, want)
	}
	for i, k := range want {
		if evs[i].Kind != k {
			t.Errorf("event[%d].Kind = %q, want %q", i, evs[i].Kind, k)
		}
	}
	if evs[0].Text != "2。" {
		t.Errorf("text = %q, want %q", evs[0].Text, "2。")
	}
}

func TestParseToolUse(t *testing.T) {
	lines := []string{
		`{"type":"assistant","message":{"content":[{"type":"text","text":"looking"},{"type":"tool_use","id":"t1","name":"list_dir","input":{"path":"."}}]}}`,
	}
	_, evs := collect(lines)

	want := []provider.EventKind{provider.EventText, provider.EventToolUse}
	if got := kinds(evs); len(got) != len(want) || got[1] != provider.EventToolUse {
		t.Fatalf("kinds = %v, want %v", got, want)
	}
	if len(evs[1].Raw) == 0 {
		t.Error("tool_use event should preserve the raw block")
	}
}

func TestParseResultError(t *testing.T) {
	lines := []string{
		`{"type":"result","subtype":"error","is_error":true,"result":"boom","stop_reason":"error","session_id":"s"}`,
	}
	turn, evs := collect(lines)

	if turn.SessionID != "s" {
		t.Errorf("session id = %q, want %q", turn.SessionID, "s")
	}
	if got := kinds(evs); len(got) != 1 || got[0] != provider.EventError {
		t.Fatalf("kinds = %v, want [error]", got)
	}
	if evs[0].Text != "boom" {
		t.Errorf("error text = %q, want %q", evs[0].Text, "boom")
	}
}

func TestUnknownAndMalformedIgnored(t *testing.T) {
	lines := []string{
		`{"type":"rate_limit_event","rate_limit_info":{"status":"allowed"}}`,
		`{"type":"some_future_event","data":123}`,
		`not json at all`,
		``,
	}
	turn, evs := collect(lines)

	if len(evs) != 0 {
		t.Fatalf("expected no events, got %v", kinds(evs))
	}
	if turn.SessionID != "" {
		t.Errorf("expected empty session id, got %q", turn.SessionID)
	}
}
