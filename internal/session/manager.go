package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/yuanyuexiang/aisr/internal/provider"
)

// ProviderResolver maps a provider name to its implementation (the Router role).
// Injected so this package depends only on the provider interface, not concretes.
type ProviderResolver func(name string) (provider.Provider, error)

// Manager orchestrates sessions over a Store and a set of providers. It is the
// single core used by both the CLI and the daemon.
type Manager struct {
	store   Store
	resolve ProviderResolver
	now     func() time.Time

	mu     sync.Mutex             // guards active
	active map[string]*activeTurn // in-flight managed turns, by session name
}

// activeTurn is the cancel handle for one in-flight managed turn, so a separate
// /cancel request can abort it.
type activeTurn struct {
	cancel context.CancelFunc
}

// NewManager wires a Manager to its store and provider resolver.
func NewManager(store Store, resolve ProviderResolver) *Manager {
	return &Manager{store: store, resolve: resolve, now: time.Now, active: map[string]*activeTurn{}}
}

// Create registers a new managed session (no provider process is started yet).
func (m *Manager) Create(providerName, name, workspace string) (*Session, error) {
	if _, err := m.resolve(providerName); err != nil {
		return nil, err
	}
	if name == "" {
		name = GenName(providerName)
	}
	if err := ValidateName(name); err != nil {
		return nil, err
	}
	ws, err := resolveWorkspace(workspace)
	if err != nil {
		return nil, err
	}
	if m.store.Exists(name) {
		return nil, fmt.Errorf("%q: %w", name, ErrExists)
	}
	t := m.now()
	rec := &Session{Name: name, Provider: providerName, Workspace: ws, CreatedAt: t, UpdatedAt: t}
	if err := m.store.Save(rec); err != nil {
		return nil, err
	}
	return rec, nil
}

// Get returns one session by name (ErrNotFound if absent).
func (m *Manager) Get(name string) (*Session, error) { return m.store.Load(name) }

// List returns all sessions, most-recently-updated first.
func (m *Manager) List() ([]*Session, error) { return m.store.List() }

// Remove deletes a session (returns ErrNotFound if absent).
func (m *Manager) Remove(name string) error { return m.store.Remove(name) }

// AskRequest parameterizes one turn.
type AskRequest struct {
	SessionName string // "" => ephemeral (nothing is persisted)
	Provider    string // for ephemeral or lazy-created sessions
	Workspace   string // for ephemeral or lazy-created sessions
	Model       string // per-turn override; not persisted
	Prompt      string
	// Agent enables agent-mode controls (MCP, tool whitelist, add-dirs, …) for
	// providers that support them; nil keeps plain prompt-in/text-out behavior.
	Agent *provider.AgentOptions
}

// Turn is the Manager's handle for one in-flight exchange.
//
// Drain Events to completion; only then are Session (with the captured provider
// session id) and SaveErr (managed-session persistence error, if any) valid —
// the producing goroutine sets them before closing the channel, so reading after
// the range loop is race-free.
type Turn struct {
	Events  <-chan provider.Event
	Session *Session
	Managed bool
	SaveErr error
}

// Ask runs a single turn. For a managed session it resumes the stored provider
// session and, once the stream completes, persists the (possibly new) provider
// session id transparently.
func (m *Manager) Ask(ctx context.Context, req AskRequest) (*Turn, error) {
	managed := req.SessionName != ""

	rec, err := m.resolveRecord(req, managed)
	if err != nil {
		return nil, err
	}

	prv, err := m.resolve(rec.Provider)
	if err != nil {
		return nil, err
	}
	// Fail clearly before spawning if the workspace vanished since creation.
	if err := checkWorkspace(rec.Workspace); err != nil {
		return nil, err
	}

	// Managed turns get a cancellable context registered under the session name
	// so a separate /cancel request can abort the in-flight CLI process.
	turnCtx := ctx
	var cancel context.CancelFunc
	var at *activeTurn
	if managed {
		turnCtx, cancel = context.WithCancel(ctx)
		at = m.registerActive(rec.Name, cancel)
	}

	pturn, err := prv.Send(turnCtx, provider.SessionOpts{
		SessionID: rec.ProviderSession,
		Workspace: rec.Workspace,
		Model:     req.Model,
		Agent:     req.Agent,
	}, req.Prompt)
	if err != nil {
		if at != nil {
			m.clearActive(rec.Name, at)
			cancel()
		}
		return nil, err
	}

	out := make(chan provider.Event)
	turn := &Turn{Events: out, Session: rec, Managed: managed}
	go func() {
		defer close(out)
		if at != nil {
			defer cancel() // release the context once the turn ends
			defer m.clearActive(rec.Name, at)
		}
		for ev := range pturn.Events {
			select {
			case out <- ev:
			case <-turnCtx.Done():
				// The consumer went away (client disconnect or /cancel). Stop
				// forwarding, but drain the provider in the background so its
				// goroutine exits and the CLI's pipes close — don't persist a
				// partial turn.
				go drainEvents(pturn.Events)
				return
			}
		}
		if pturn.SessionID != "" {
			rec.ProviderSession = pturn.SessionID
			rec.UpdatedAt = m.now()
			if managed {
				turn.SaveErr = m.store.Save(rec)
			}
		}
	}()
	return turn, nil
}

// drainEvents consumes the rest of a provider stream after the consumer has
// gone away, so the provider's goroutine can finish (its send no longer blocks)
// and close the channel instead of leaking.
func drainEvents(ch <-chan provider.Event) {
	for range ch { //nolint:revive // intentional drain
	}
}

// Cancel aborts the in-flight turn for a managed session, killing its CLI
// process. Returns ErrNoActiveTurn if nothing is running for that name.
func (m *Manager) Cancel(name string) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	m.mu.Lock()
	at := m.active[name]
	m.mu.Unlock()
	if at == nil {
		return fmt.Errorf("%q: %w", name, ErrNoActiveTurn)
	}
	at.cancel()
	return nil
}

// registerActive records the cancel handle for a session's in-flight turn,
// cancelling any prior turn still registered under the same name, and returns
// the handle so the caller can later clearActive exactly this turn.
func (m *Manager) registerActive(name string, cancel context.CancelFunc) *activeTurn {
	at := &activeTurn{cancel: cancel}
	m.mu.Lock()
	defer m.mu.Unlock()
	if prev := m.active[name]; prev != nil {
		prev.cancel()
	}
	m.active[name] = at
	return at
}

// clearActive removes the active entry for name, but only if it's still this
// turn (pointer identity) — so a newer turn that replaced it isn't unregistered.
func (m *Manager) clearActive(name string, at *activeTurn) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active[name] == at {
		delete(m.active, name)
	}
}

// resolveRecord loads (or lazily builds) the session record for a turn.
func (m *Manager) resolveRecord(req AskRequest, managed bool) (*Session, error) {
	if !managed {
		ws, err := resolveWorkspace(req.Workspace)
		if err != nil {
			return nil, err
		}
		return &Session{Provider: req.Provider, Workspace: ws}, nil
	}

	if err := ValidateName(req.SessionName); err != nil {
		return nil, err
	}
	rec, err := m.store.Load(req.SessionName)
	switch {
	case err == nil:
		return rec, nil
	case errors.Is(err, ErrNotFound):
		ws, werr := resolveWorkspace(req.Workspace)
		if werr != nil {
			return nil, werr
		}
		return &Session{
			Name:      req.SessionName,
			Provider:  req.Provider,
			Workspace: ws,
			CreatedAt: m.now(),
		}, nil
	default:
		return nil, err
	}
}

// resolveWorkspace makes a non-empty path absolute and verifies it exists.
func resolveWorkspace(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if err := checkWorkspace(abs); err != nil {
		return "", err
	}
	return abs, nil
}

// checkWorkspace turns the exec-layer chdir failure into a clear up-front error.
func checkWorkspace(path string) error {
	if path == "" {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%w: does not exist: %s", ErrWorkspaceInvalid, path)
		}
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%w: not a directory: %s", ErrWorkspaceInvalid, path)
	}
	return nil
}
