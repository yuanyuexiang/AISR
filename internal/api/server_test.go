package api_test

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yuanyuexiang/aisr/internal/api"
	"github.com/yuanyuexiang/aisr/internal/provider"
	"github.com/yuanyuexiang/aisr/internal/session"
	"github.com/yuanyuexiang/aisr/internal/storage"
)

// stubProvider stands in for claude so API tests stay hermetic (no real CLI).
type stubProvider struct{}

func (stubProvider) Name() string { return "claude" }
func (stubProvider) Capabilities() provider.Capabilities {
	return provider.Capabilities{StructuredOutput: true, Resume: true}
}
func (stubProvider) Send(_ context.Context, _ provider.SessionOpts, prompt string) (*provider.Turn, error) {
	ch := make(chan provider.Event, 2)
	ch <- provider.Event{Kind: provider.EventText, Text: "stub:" + prompt}
	ch <- provider.Event{Kind: provider.EventDone}
	close(ch)
	return &provider.Turn{Events: ch, SessionID: "stub-sess"}, nil
}

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	store, err := storage.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	reg := provider.NewRegistry(stubProvider{})
	mgr := session.NewManager(store, reg.Resolve)
	srv := api.NewServer(mgr, reg.List(), log.New(io.Discard, "", 0), "")
	return httptest.NewServer(srv.Handler())
}

func TestCreateAndList(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	resp := postJSON(t, ts.URL+"/v1/sessions", `{"name":"t1"}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, want 201", resp.StatusCode)
	}
	resp.Body.Close()

	body := getBody(t, ts.URL+"/v1/sessions")
	if !strings.Contains(body, `"t1"`) {
		t.Errorf("list does not contain t1: %s", body)
	}
}

func TestMessagesStreamAndPersist(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	// Posting to a fresh name lazily creates the session.
	resp := postJSON(t, ts.URL+"/v1/sessions/chat/messages", `{"prompt":"hi"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("messages status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/x-ndjson" {
		t.Errorf("content-type = %q, want application/x-ndjson", ct)
	}

	var kinds []string
	var firstText string
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		var ev provider.Event
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			t.Fatalf("bad NDJSON line %q: %v", sc.Text(), err)
		}
		kinds = append(kinds, string(ev.Kind))
		if ev.Kind == provider.EventText && firstText == "" {
			firstText = ev.Text
		}
	}
	resp.Body.Close()

	if firstText != "stub:hi" {
		t.Errorf("text = %q, want stub:hi", firstText)
	}
	if len(kinds) != 2 || kinds[0] != "text" || kinds[1] != "done" {
		t.Errorf("event kinds = %v, want [text done]", kinds)
	}

	// The provider session id should now be persisted on the record.
	body := getBody(t, ts.URL+"/v1/sessions/chat")
	if !strings.Contains(body, "stub-sess") {
		t.Errorf("record missing provider session: %s", body)
	}
}

func TestCancelNoActiveTurn(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	resp := postJSON(t, ts.URL+"/v1/sessions/dev/cancel", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("cancel status = %d, want 409", resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), "NO_ACTIVE_TURN") {
		t.Errorf("body missing NO_ACTIVE_TURN: %s", b)
	}
}

func TestGetNotFound(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/v1/sessions/nope")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "SESSION_NOT_FOUND") {
		t.Errorf("body missing error code: %s", body)
	}
}

func TestTokenAuth(t *testing.T) {
	store, err := storage.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	reg := provider.NewRegistry(stubProvider{})
	mgr := session.NewManager(store, reg.Resolve)
	srv := api.NewServer(mgr, reg.List(), log.New(io.Discard, "", 0), "secret")
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	do := func(bearer string) int {
		req, _ := http.NewRequest(http.MethodGet, ts.URL+"/v1/providers", nil)
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}

	if got := do(""); got != http.StatusUnauthorized {
		t.Errorf("no token: status = %d, want 401", got)
	}
	if got := do("wrong"); got != http.StatusUnauthorized {
		t.Errorf("wrong token: status = %d, want 401", got)
	}
	if got := do("secret"); got != http.StatusOK {
		t.Errorf("correct token: status = %d, want 200", got)
	}
}

func TestProviders(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	body := getBody(t, ts.URL+"/v1/providers")
	if !strings.Contains(body, `"claude"`) {
		t.Errorf("providers missing claude: %s", body)
	}
}

// --- helpers ---

func postJSON(t *testing.T, url, body string) *http.Response {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func getBody(t *testing.T, url string) string {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}
