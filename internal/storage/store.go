// Package storage persists session records to disk.
//
// One JSON file per session under the session dir (default ~/.aisr/sessions),
// so list is a dir scan and remove is a file unlink. See 技术方案.md §四/§九.
package storage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/yuanyuexiang/aisr/internal/session"
)

// Store reads/writes session records under a directory. It implements
// session.Store; ErrNotFound is the shared sentinel session.ErrNotFound.
type Store struct {
	dir string
}

// DefaultDir returns ~/.aisr/sessions.
func DefaultDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".aisr", "sessions"), nil
}

// New opens (creating if needed) a Store rooted at dir.
func New(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Store{dir: dir}, nil
}

func (s *Store) path(name string) string {
	return filepath.Join(s.dir, name+".json")
}

// Exists reports whether a session record is present.
func (s *Store) Exists(name string) bool {
	_, err := os.Stat(s.path(name))
	return err == nil
}

// Save writes (or overwrites) a record. Owner-only perms (0600).
func (s *Store) Save(rec *session.Session) error {
	b, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path(rec.Name), b, 0o600)
}

// Load reads a record by name; returns ErrNotFound if absent.
func (s *Store) Load(name string) (*session.Session, error) {
	b, err := os.ReadFile(s.path(name))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, session.ErrNotFound
		}
		return nil, err
	}
	var rec session.Session
	if err := json.Unmarshal(b, &rec); err != nil {
		return nil, err
	}
	return &rec, nil
}

// List returns all records, most-recently-updated first.
func (s *Store) List() ([]*session.Session, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	var recs []*session.Session
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".json")
		rec, err := s.Load(name)
		if err != nil {
			continue // skip unreadable/corrupt records
		}
		recs = append(recs, rec)
	}
	sort.Slice(recs, func(i, j int) bool {
		return recs[i].UpdatedAt.After(recs[j].UpdatedAt)
	})
	return recs, nil
}

// Remove deletes a record; returns ErrNotFound if absent.
func (s *Store) Remove(name string) error {
	err := os.Remove(s.path(name))
	if os.IsNotExist(err) {
		return session.ErrNotFound
	}
	return err
}
