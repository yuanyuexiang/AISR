package sdk_test

import (
	"context"
	"io"
	"log"
	"net/http/httptest"
	"testing"

	"github.com/yuanyuexiang/aisr/internal/api"
	"github.com/yuanyuexiang/aisr/internal/provider"
	"github.com/yuanyuexiang/aisr/internal/session"
	"github.com/yuanyuexiang/aisr/internal/storage"
	"github.com/yuanyuexiang/aisr/pkg/sdk"
)

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

// newClient spins a real api.Server (stub provider, temp store) over TCP and
// returns an SDK client pointed at it.
func newClient(t *testing.T) *sdk.Client {
	t.Helper()
	store, err := storage.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	reg := provider.NewRegistry(stubProvider{})
	mgr := session.NewManager(store, reg.Resolve)
	srv := api.NewServer(mgr, reg.List(), log.New(io.Discard, "", 0))
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return sdk.New(sdk.WithBaseURL(ts.URL))
}

func TestSDKProviders(t *testing.T) {
	c := newClient(t)
	ps, err := c.Providers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 1 || ps[0].Name != "claude" || !ps[0].Capabilities.Resume {
		t.Fatalf("providers = %+v, want one claude with resume", ps)
	}
}

func TestSDKCreateSendGet(t *testing.T) {
	c := newClient(t)
	ctx := context.Background()

	if _, err := c.CreateSession(ctx, sdk.CreateRequest{Name: "dev"}); err != nil {
		t.Fatal(err)
	}

	events, err := c.Send(ctx, "dev", "hi", sdk.SendOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var text string
	var kinds []sdk.EventKind
	for ev := range events {
		kinds = append(kinds, ev.Kind)
		if ev.Kind == sdk.EventText {
			text += ev.Text
		}
	}
	if text != "stub:hi" {
		t.Errorf("text = %q, want stub:hi", text)
	}
	if len(kinds) != 2 || kinds[0] != sdk.EventText || kinds[1] != sdk.EventDone {
		t.Errorf("kinds = %v, want [text done]", kinds)
	}

	got, err := c.GetSession(ctx, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if got.ProviderSession != "stub-sess" {
		t.Errorf("provider session = %q, want stub-sess", got.ProviderSession)
	}
}

func TestSDKGetNotFound(t *testing.T) {
	c := newClient(t)
	_, err := c.GetSession(context.Background(), "nope")
	apiErr, ok := err.(*sdk.APIError)
	if !ok {
		t.Fatalf("err = %v (%T), want *sdk.APIError", err, err)
	}
	if apiErr.Status != 404 || apiErr.Code != "SESSION_NOT_FOUND" {
		t.Errorf("got status=%d code=%s, want 404/SESSION_NOT_FOUND", apiErr.Status, apiErr.Code)
	}
}
