package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// SessionRecord is the on-disk handle to a launched browser. The CLI persists
// it so subsequent invocations can attach via CDP without relaunching.
type SessionRecord struct {
	Name      string    `json:"name"`
	WSURL     string    `json:"ws_url"`
	ChromePID int       `json:"chrome_pid"`
	UserData  string    `json:"user_data_dir,omitempty"`
	Humanize  string    `json:"humanize,omitempty"` // "", "off", "on"
	CreatedAt time.Time `json:"created_at"`
}

func sessionsDir() string {
	if dir := os.Getenv("MIDAS_SESSIONS_DIR"); dir != "" {
		return dir
	}
	return filepath.Join(os.TempDir(), "midas-sessions")
}

func sessionPath(name string) string {
	return filepath.Join(sessionsDir(), safeName(name)+".json")
}

func safeName(name string) string {
	clean := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		case r == '-' || r == '_':
			return r
		default:
			return '_'
		}
	}, name)
	if clean == "" {
		return "default"
	}
	return clean
}

func saveSession(rec SessionRecord) error {
	if err := os.MkdirAll(sessionsDir(), 0o755); err != nil {
		return fmt.Errorf("create sessions dir: %w", err)
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(sessionPath(rec.Name), data, 0o600)
}

func loadSession(name string) (SessionRecord, error) {
	data, err := os.ReadFile(sessionPath(name))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return SessionRecord{}, fmt.Errorf("no session %q (open it first)", name)
		}
		return SessionRecord{}, err
	}
	var rec SessionRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return SessionRecord{}, fmt.Errorf("parse session %q: %w", name, err)
	}
	return rec, nil
}

func deleteSession(name string) error {
	err := os.Remove(sessionPath(name))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func listSessions() ([]SessionRecord, error) {
	entries, err := os.ReadDir(sessionsDir())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []SessionRecord
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".json")
		rec, err := loadSession(name)
		if err != nil {
			continue
		}
		out = append(out, rec)
	}
	return out, nil
}
