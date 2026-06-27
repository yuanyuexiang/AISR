// Package session defines the persisted session record.
//
// A session's identity is its name + the underlying provider session id; the
// process is a disposable carrier (see 技术方案.md §六). Persistence lives in
// the storage package; orchestration currently lives in cmd/aisr (a Manager will
// be extracted when the daemon needs it).
package session

import (
	"crypto/rand"
	"fmt"
	"strings"
	"time"
)

// Session is one managed conversation, persisted as a JSON file by storage.Store.
type Session struct {
	Name            string    `json:"name"`             // AISR-facing handle (friendly)
	Provider        string    `json:"provider"`         // claude / cursor / gemini
	Workspace       string    `json:"workspace"`        // absolute working dir ("" = provider cwd)
	ProviderSession string    `json:"provider_session"` // underlying CLI session id; "" until first turn
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// ValidateName rejects names that are unsafe as a filename.
func ValidateName(name string) error {
	if name == "" {
		return fmt.Errorf("session name is empty")
	}
	if name == "." || name == ".." || strings.Contains(name, "..") ||
		strings.ContainsAny(name, `/\`) {
		return fmt.Errorf("invalid session name %q", name)
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
