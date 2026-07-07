package cursor

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/yuanyuexiang/aisr/internal/provider"
)

func decodeRaw(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("decode raw: %v", err)
	}
	return m
}

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

func TestNewRespectsBinEnv(t *testing.T) {
	if New().bin != "cursor-agent" {
		t.Errorf("default bin = %q, want cursor-agent", New().bin)
	}
	t.Setenv("AISR_CURSOR_BIN", "cursor-agent.cmd")
	if New().bin != "cursor-agent.cmd" {
		t.Errorf("override bin = %q, want cursor-agent.cmd", New().bin)
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

// Cursor's real tool calls: separate tool_call events (not content blocks),
// started → tool_use, completed → tool_result. MCP calls get mcp__<prov>__<tool>.
func TestParseMCPToolCall(t *testing.T) {
	lines := []string{
		`{"type":"tool_call","subtype":"started","call_id":"t1","tool_call":{"mcpToolCall":{"args":{"providerIdentifier":"falconry","toolName":"get_section","args":{"report_key":"r"}}}}}`,
		`{"type":"tool_call","subtype":"completed","call_id":"t1","tool_call":{"mcpToolCall":{"args":{"providerIdentifier":"falconry","toolName":"get_section"},"result":{"success":{"content":[{"text":{"text":"RESULTTEXT"}}],"isError":false}}}}}`,
	}
	_, evs := collect(lines)
	if got := kinds(evs); len(got) != 2 || got[0] != provider.EventToolUse || got[1] != provider.EventToolResult {
		t.Fatalf("kinds = %v, want [tool_use tool_result]", got)
	}
	use := decodeRaw(t, evs[0].Raw)
	if use["name"] != "mcp__falconry__get_section" {
		t.Errorf("tool name = %v, want mcp__falconry__get_section", use["name"])
	}
	if use["id"] != "t1" {
		t.Errorf("tool id = %v, want t1", use["id"])
	}
	res := decodeRaw(t, evs[1].Raw)
	if res["tool_use_id"] != "t1" || res["content"] != "RESULTTEXT" || res["is_error"] != false {
		t.Errorf("tool_result = %v", res)
	}
}

// Built-in tools (read/glob/shell/…) → generic tool_use named by the tool kind.
func TestParseBuiltinToolCall(t *testing.T) {
	lines := []string{
		`{"type":"tool_call","subtype":"started","call_id":"r1","tool_call":{"readToolCall":{"args":{"path":"/x"}}}}`,
	}
	_, evs := collect(lines)
	if got := kinds(evs); len(got) != 1 || got[0] != provider.EventToolUse {
		t.Fatalf("kinds = %v, want [tool_use]", got)
	}
	if use := decodeRaw(t, evs[0].Raw); use["name"] != "read" {
		t.Errorf("builtin name = %v, want read", use["name"])
	}
}

func TestBuildArgsAgentFlags(t *testing.T) {
	args := buildArgs(provider.SessionOpts{Agent: &provider.AgentOptions{}}, "/ws")
	joined := strings.Join(args, " ")
	for _, want := range []string{"-p", "--force", "--approve-mcps", "--trust", "--workspace /ws"} {
		if !strings.Contains(joined, want) {
			t.Errorf("args missing %q: %v", want, args)
		}
	}
	// Prompt is passed on stdin, never as an argv element.
	for _, unwant := range []string{"hi", "PERSONA"} {
		if strings.Contains(joined, unwant) {
			t.Errorf("prompt leaked into argv (%q): %v", unwant, args)
		}
	}
	// No agent → none of the agent flags.
	plain := strings.Join(buildArgs(provider.SessionOpts{}, ""), " ")
	if strings.Contains(plain, "--approve-mcps") || strings.Contains(plain, "--trust") {
		t.Errorf("plain args should have no agent flags: %v", plain)
	}
}

func TestPrependSystemPrompt(t *testing.T) {
	out := prependSystemPrompt(&provider.AgentOptions{AppendSystemPrompt: "PERSONA"}, "do it")
	if !strings.HasPrefix(out, "PERSONA") || !strings.HasSuffix(out, "do it") {
		t.Errorf("prepend = %q", out)
	}
}
