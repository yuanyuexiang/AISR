// Package session is the Session Manager: session records, lifecycle, and the
// orchestration shared by the CLI and the daemon (see 技术方案.md §五).
//
// To avoid an import cycle (storage depends on this package's Session type), the
// Manager depends on the Store *interface* defined here — storage implements it,
// not the other way around (dependency inversion). Likewise providers are reached
// via a ProviderResolver injected by the caller, so this package needs no concrete
// provider import.
package session

import (
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Sentinel errors, shared by the Manager, storage, and the API layer (which maps
// them to HTTP status codes).
var (
	ErrNotFound         = errors.New("session not found")
	ErrExists           = errors.New("session already exists")
	ErrInvalidName      = errors.New("invalid session name")
	ErrWorkspaceInvalid = errors.New("invalid workspace")
)

// Session is one managed conversation, persisted as JSON by a Store.
type Session struct {
	Name            string    `json:"name"`             // AISR-facing handle (friendly)
	Provider        string    `json:"provider"`         // claude / cursor / gemini
	Workspace       string    `json:"workspace"`        // absolute working dir ("" = provider cwd)
	ProviderSession string    `json:"provider_session"` // underlying CLI session id; "" until first turn
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// Store is the persistence contract the Manager depends on. The storage package
// implements it; defining it here keeps the dependency pointing storage→session.
type Store interface {
	Exists(name string) bool
	Save(rec *Session) error
	Load(name string) (*Session, error) // returns ErrNotFound if absent
	List() ([]*Session, error)
	Remove(name string) error // returns ErrNotFound if absent
}

// ValidateName rejects names that are unsafe as a filename.
func ValidateName(name string) error {
	if name == "" {
		return fmt.Errorf("%w: empty", ErrInvalidName)
	}
	if name == "." || name == ".." || strings.Contains(name, "..") ||
		strings.ContainsAny(name, `/\`) {
		return fmt.Errorf("%w: %q", ErrInvalidName, name)
	}
	return nil
}

// GenName produces a short unique name like "claude-9f3a1c".
func GenName(provider string) string {
	var b [3]byte
	if _, err := rand.Read(b[:]); err != nil {
		return provider + "-000000"
	}
	return fmt.Sprintf("%s-%x", provider, b[:])
}
