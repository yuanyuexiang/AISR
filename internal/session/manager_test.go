package session

import (
	"context"
	"errors"
	"testing"

	"github.com/yuanyuexiang/aisr/internal/provider"
)

// --- fakes ---

type fakeStore struct {
	recs  map[string]*Session
	saves int
}

func newFakeStore() *fakeStore { return &fakeStore{recs: map[string]*Session{}} }

func (s *fakeStore) Exists(name string) bool { _, ok := s.recs[name]; return ok }

func (s *fakeStore) Save(rec *Session) error {
	cp := *rec
	s.recs[rec.Name] = &cp
	s.saves++
	return nil
}

func (s *fakeStore) Load(name string) (*Session, error) {
	rec, ok := s.recs[name]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *rec
	return &cp, nil
}

func (s *fakeStore) List() ([]*Session, error) {
	out := make([]*Session, 0, len(s.recs))
	for _, r := range s.recs {
		cp := *r
		out = append(out, &cp)
	}
	return out, nil
}

func (s *fakeStore) Remove(name string) error {
	if _, ok := s.recs[name]; !ok {
		return ErrNotFound
	}
	delete(s.recs, name)
	return nil
}

type fakeProvider struct {
	sessionID string
	events    []provider.Event
	sawOpts   provider.SessionOpts
}

func (f *fakeProvider) Name() string { return "fake" }
func (f *fakeProvider) Capabilities() provider.Capabilities {
	return provider.Capabilities{Resume: true}
}

func (f *fakeProvider) Send(_ context.Context, opts provider.SessionOpts, _ string) (*provider.Turn, error) {
	f.sawOpts = opts
	ch := make(chan provider.Event, len(f.events))
	for _, e := range f.events {
		ch <- e
	}
	close(ch)
	return &provider.Turn{Events: ch, SessionID: f.sessionID}, nil
}

func resolverFor(p provider.Provider) ProviderResolver {
	return func(string) (provider.Provider, error) { return p, nil }
}

func drain(t *Turn) []provider.Event {
	var evs []provider.Event
	for e := range t.Events {
		evs = append(evs, e)
	}
	return evs
}

// --- tests ---

func TestAskManagedLazyCreateAndPersist(t *testing.T) {
	store := newFakeStore()
	fp := &fakeProvider{
		sessionID: "prov-123",
		events:    []provider.Event{{Kind: provider.EventText, Text: "hi"}, {Kind: provider.EventDone}},
	}
	mgr := NewManager(store, resolverFor(fp))

	turn, err := mgr.Ask(context.Background(), AskRequest{SessionName: "dev", Provider: "claude", Prompt: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if got := drain(turn); len(got) != 2 {
		t.Fatalf("got %d events, want 2", len(got))
	}

	if !turn.Managed {
		t.Error("turn should be managed")
	}
	if fp.sawOpts.SessionID != "" {
		t.Errorf("new session should not resume; provider saw SessionID=%q", fp.sawOpts.SessionID)
	}
	if turn.Session.ProviderSession != "prov-123" {
		t.Errorf("captured provider session = %q, want prov-123", turn.Session.ProviderSession)
	}
	saved, err := store.Load("dev")
	if err != nil {
		t.Fatalf("expected persisted record: %v", err)
	}
	if saved.ProviderSession != "prov-123" {
		t.Errorf("persisted provider session = %q, want prov-123", saved.ProviderSession)
	}
}

func TestAskManagedResumePassesStoredProviderSession(t *testing.T) {
	store := newFakeStore()
	store.recs["dev"] = &Session{Name: "dev", Provider: "claude", ProviderSession: "old-1"}
	fp := &fakeProvider{sessionID: "old-1", events: []provider.Event{{Kind: provider.EventDone}}}
	mgr := NewManager(store, resolverFor(fp))

	turn, err := mgr.Ask(context.Background(), AskRequest{SessionName: "dev", Prompt: "again"})
	if err != nil {
		t.Fatal(err)
	}
	drain(turn)

	if fp.sawOpts.SessionID != "old-1" {
		t.Errorf("resume should pass stored id; provider saw SessionID=%q, want old-1", fp.sawOpts.SessionID)
	}
}

func TestAskEphemeralNotPersisted(t *testing.T) {
	store := newFakeStore()
	fp := &fakeProvider{sessionID: "eph-9", events: []provider.Event{{Kind: provider.EventDone}}}
	mgr := NewManager(store, resolverFor(fp))

	turn, err := mgr.Ask(context.Background(), AskRequest{Provider: "claude", Prompt: "x"})
	if err != nil {
		t.Fatal(err)
	}
	drain(turn)

	if turn.Managed {
		t.Error("turn should be ephemeral")
	}
	if turn.Session.ProviderSession != "eph-9" {
		t.Errorf("ephemeral session id = %q, want eph-9", turn.Session.ProviderSession)
	}
	if store.saves != 0 {
		t.Errorf("ephemeral turn must not persist; saves=%d", store.saves)
	}
}

func TestCreateRejectsDuplicate(t *testing.T) {
	store := newFakeStore()
	mgr := NewManager(store, resolverFor(&fakeProvider{}))

	if _, err := mgr.Create("claude", "dev", ""); err != nil {
		t.Fatal(err)
	}
	_, err := mgr.Create("claude", "dev", "")
	if !errors.Is(err, ErrExists) {
		t.Errorf("second create err = %v, want ErrExists", err)
	}
}

func TestRemoveMissing(t *testing.T) {
	mgr := NewManager(newFakeStore(), resolverFor(&fakeProvider{}))
	if err := mgr.Remove("nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("remove err = %v, want ErrNotFound", err)
	}
}

func TestAskRejectsInvalidName(t *testing.T) {
	mgr := NewManager(newFakeStore(), resolverFor(&fakeProvider{}))
	if _, err := mgr.Ask(context.Background(), AskRequest{SessionName: "bad/name", Prompt: "x"}); err == nil {
		t.Error("expected error for invalid session name")
	}
}
