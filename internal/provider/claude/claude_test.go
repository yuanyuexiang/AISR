package claude

import (
	"encoding/json"
	"slices"
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

func TestNewRespectsBinEnv(t *testing.T) {
	if New().bin != "claude" {
		t.Errorf("default bin = %q, want claude", New().bin)
	}
	t.Setenv("AISR_CLAUDE_BIN", "claude.cmd")
	if New().bin != "claude.cmd" {
		t.Errorf("override bin = %q, want claude.cmd", New().bin)
	}
}

func TestParseThinking(t *testing.T) {
	lines := []string{
		`{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"let me think","signature":"sig"},{"type":"text","text":"answer"}]}}`,
	}
	_, evs := collect(lines)

	want := []provider.EventKind{provider.EventThinking, provider.EventText}
	if got := kinds(evs); len(got) != len(want) || got[0] != provider.EventThinking || got[1] != provider.EventText {
		t.Fatalf("kinds = %v, want %v", got, want)
	}
	if evs[0].Text != "let me think" {
		t.Errorf("thinking text = %q, want %q", evs[0].Text, "let me think")
	}
	if len(evs[0].Raw) == 0 {
		t.Error("thinking event should preserve the raw block (signature)")
	}
}

// after returns the token(s) following flag in args; n=1 for single-value flags,
// n>1 for variadic flags. Fails the test if flag is absent.
func after(t *testing.T, args []string, flag string, n int) []string {
	t.Helper()
	i := slices.Index(args, flag)
	if i < 0 {
		t.Fatalf("flag %q not in args %v", flag, args)
	}
	if i+1+n > len(args) {
		t.Fatalf("flag %q has fewer than %d values in %v", flag, n, args)
	}
	return args[i+1 : i+1+n]
}

func TestBuildArgsNoAgent(t *testing.T) {
	args := buildArgs(provider.SessionOpts{Model: "opus", SessionID: "s1"}, "hi")
	want := []string{"-p", "hi", "--output-format", "stream-json", "--verbose", "--model", "opus", "--resume", "s1"}
	if !slices.Equal(args, want) {
		t.Fatalf("args = %v, want %v", args, want)
	}
}

func TestBuildArgsAgentFlags(t *testing.T) {
	args := buildArgs(provider.SessionOpts{
		Agent: &provider.AgentOptions{
			SystemPrompt:       "sysreplace",
			AppendSystemPrompt: "persona",
			AllowedTools:       []string{"Read", "mcp__auric__ask_user"},
			DisallowedTools:    []string{"Bash"},
			MCPConfig:          json.RawMessage(`{"mcpServers":{}}`),
			AddDirs:            []string{"/deal", "/ip"},
			MaxTurns:           7,
			PermissionMode:     "bypassPermissions",
		},
	}, "hi")

	if got := after(t, args, "--system-prompt", 1); got[0] != "sysreplace" {
		t.Errorf("--system-prompt = %v, want [sysreplace]", got)
	}
	if got := after(t, args, "--append-system-prompt", 1); got[0] != "persona" {
		t.Errorf("--append-system-prompt = %v, want [persona]", got)
	}
	if got := after(t, args, "--max-turns", 1); got[0] != "7" {
		t.Errorf("--max-turns = %v, want [7]", got)
	}
	if got := after(t, args, "--permission-mode", 1); got[0] != "bypassPermissions" {
		t.Errorf("--permission-mode = %v, want [bypassPermissions]", got)
	}
	if got := after(t, args, "--mcp-config", 1); got[0] != `{"mcpServers":{}}` {
		t.Errorf("--mcp-config = %v", got)
	}
	if got := after(t, args, "--allowedTools", 2); !slices.Equal(got, []string{"Read", "mcp__auric__ask_user"}) {
		t.Errorf("--allowedTools = %v", got)
	}
	if got := after(t, args, "--disallowedTools", 1); got[0] != "Bash" {
		t.Errorf("--disallowedTools = %v, want [Bash]", got)
	}
	if got := after(t, args, "--add-dir", 2); !slices.Equal(got, []string{"/deal", "/ip"}) {
		t.Errorf("--add-dir = %v", got)
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
