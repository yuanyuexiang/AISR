package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
}

// NewManager wires a Manager to its store and provider resolver.
func NewManager(store Store, resolve ProviderResolver) *Manager {
	return &Manager{store: store, resolve: resolve, now: time.Now}
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

	pturn, err := prv.Send(ctx, provider.SessionOpts{
		SessionID: rec.ProviderSession,
		Workspace: rec.Workspace,
		Model:     req.Model,
	}, req.Prompt)
	if err != nil {
		return nil, err
	}

	out := make(chan provider.Event)
	turn := &Turn{Events: out, Session: rec, Managed: managed}
	go func() {
		defer close(out)
		for ev := range pturn.Events {
			out <- ev
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
