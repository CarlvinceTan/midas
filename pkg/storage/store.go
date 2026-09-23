// Package storage persists Midas-owned session metadata, transcripts, drafts,
// and client-side state as JSON under the agent's config directory.
package storage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	maxSessions          = 500
	maxDrafts            = 100
	maxSessionStates     = 100
	maxBashEntries       = 200
	maxBashOutput        = 100_000
	maxQueuedPrompts     = 50
	maxAttachmentURL     = 1_500_000
	maxFrozenFileContent = 1_500_000
)

// Store persists Midas-owned session metadata and client-side state. It is safe
// for concurrent use within one process, and each write is atomic; two processes
// sharing a config directory still see last-writer-wins, which is the case for
// two Midas sessions started from the same account.
type Store struct {
	dir string
	now func() time.Time
	mu  sync.Mutex
}

// New returns a store rooted at dir. An empty dir uses ConfigDir.
func New(dir string) *Store {
	if dir == "" {
		dir = ConfigDir()
	}
	return &Store{dir: dir, now: time.Now}
}

func newWithClock(dir string, now func() time.Time) *Store { return &Store{dir: dir, now: now} }

// ConfigDir returns MIDAS_CONFIG_DIR or ~/.midas.
func ConfigDir() string {
	if configured := os.Getenv("MIDAS_CONFIG_DIR"); configured != "" {
		return configured
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".midas"
	}
	return filepath.Join(home, ".midas")
}

func (s *Store) SessionsPath() string     { return filepath.Join(s.dir, "sessions.json") }
func (s *Store) DraftsPath() string       { return filepath.Join(s.dir, "drafts.json") }
func (s *Store) SessionStatePath() string { return filepath.Join(s.dir, "session-state.json") }
func (s *Store) Directory() string        { return s.dir }

func readObject(path string) map[string]json.RawMessage {
	data, err := os.ReadFile(path)
	if err != nil {
		return map[string]json.RawMessage{}
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(data, &object) != nil || object == nil {
		return map[string]json.RawMessage{}
	}
	return object
}

// writeObject replaces a state file atomically: the data is written to a
// temporary file in the same directory, given owner-only permissions, and
// renamed over the destination. A crash mid-write therefore leaves the previous
// file intact instead of truncating it.
func (s *Store) writeObject(path string, object any) error {
	data, err := json.MarshalIndent(object, "", "  ")
	if err != nil {
		return fmt.Errorf("storage: encode %s: %w", filepath.Base(path), err)
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return fmt.Errorf("storage: create %s: %w", s.dir, err)
	}
	temporary, err := os.CreateTemp(s.dir, ".midas-*.tmp")
	if err != nil {
		return fmt.Errorf("storage: create a temporary file in %s: %w", s.dir, err)
	}
	name := temporary.Name()
	defer os.Remove(name)
	if _, err := temporary.Write(append(data, '\n')); err != nil {
		temporary.Close()
		return fmt.Errorf("storage: write %s: %w", filepath.Base(path), err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("storage: close %s: %w", filepath.Base(path), err)
	}
	if err := os.Chmod(name, 0o600); err != nil {
		return fmt.Errorf("storage: protect %s: %w", filepath.Base(path), err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("storage: replace %s: %w", filepath.Base(path), err)
	}
	return nil
}
