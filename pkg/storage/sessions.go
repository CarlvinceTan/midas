package storage

import (
	"cmp"
	"encoding/hex"
	"encoding/json"
	"os"
	"slices"
	"strings"
)

// Session is one Midas-owned conversation registry entry.
type Session struct {
	ID        string `json:"id"`
	CWD       string `json:"cwd"`
	Title     string `json:"title"`
	CreatedAt int64  `json:"createdAt"`
	UpdatedAt int64  `json:"updatedAt"`
}

// SessionUpdate inserts or refreshes one session. Title distinguishes omitted from explicitly empty.
type SessionUpdate struct {
	ID        string
	CWD       string
	Title     *string
	CreatedAt *int64
	UpdatedAt *int64
}

func decodeSession(id string, raw json.RawMessage) Session {
	var value map[string]json.RawMessage
	_ = json.Unmarshal(raw, &value)
	entry := Session{ID: id}
	_ = json.Unmarshal(value["cwd"], &entry.CWD)
	_ = json.Unmarshal(value["title"], &entry.Title)
	_ = json.Unmarshal(value["createdAt"], &entry.CreatedAt)
	_ = json.Unmarshal(value["updatedAt"], &entry.UpdatedAt)
	return entry
}

func readSessions(path string) map[string]Session {
	result := make(map[string]Session)
	for id, raw := range readObject(path) {
		var object map[string]json.RawMessage
		if json.Unmarshal(raw, &object) != nil || object == nil {
			continue
		}
		result[id] = decodeSession(id, raw)
	}
	return result
}

// ReadSessions returns registry entries newest first.
func (s *Store) ReadSessions() []Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	return sortedSessions(readSessions(s.SessionsPath()))
}

// ReadResumableSessions returns sessions backed by native Go state. The
// sessions registry predates the Go port and can contain legacy OpenCode rows
// whose transcripts are no longer available; exposing those rows would resume
// an empty conversation. The current session is always retained.
func (s *Store) ReadResumableSessions(currentID string) []Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	store := readSessions(s.SessionsPath())
	drafts := readDrafts(s.DraftsPath())
	states := readSessionStates(s.SessionStatePath())
	filtered := make(map[string]Session)
	for id, entry := range store {
		_, hasDraft := drafts[id]
		_, hasState := states[id]
		hasTranscript := false
		if path, err := s.TranscriptPath(id); err == nil {
			if info, statErr := os.Stat(path); statErr == nil && !info.IsDir() {
				hasTranscript = true
			}
		}
		nativeSessionState := isNativeSessionID(id) && (hasDraft || hasState || strings.TrimSpace(entry.Title) != "")
		if id == currentID || hasTranscript || nativeSessionState {
			filtered[id] = entry
		}
	}
	return sortedSessions(filtered)
}

func sortedSessions(store map[string]Session) []Session {
	result := make([]Session, 0, len(store))
	for _, entry := range store {
		result = append(result, entry)
	}
	slices.SortStableFunc(result, func(a, b Session) int {
		if a.UpdatedAt == b.UpdatedAt {
			return cmp.Compare(a.ID, b.ID)
		}
		return cmp.Compare(b.UpdatedAt, a.UpdatedAt)
	})
	return result
}

func isNativeSessionID(id string) bool {
	value := strings.TrimPrefix(id, "ses_")
	if value == id || len(value) != 24 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

// UpsertSession inserts or refreshes a session.
func (s *Store) UpsertSession(update SessionUpdate) error {
	if update.ID == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	store := readSessions(s.SessionsPath())
	previous := store[update.ID]
	now := s.now().UnixMilli()
	title := previous.Title
	if update.Title != nil {
		raw := strings.TrimSpace(*update.Title)
		if raw != "New session" {
			title = raw
		}
	}
	cwd := update.CWD
	if cwd == "" {
		cwd = previous.CWD
	}
	createdAt := now
	if previous.ID != "" {
		createdAt = previous.CreatedAt
	}
	if update.CreatedAt != nil {
		createdAt = *update.CreatedAt
	}
	updatedAt := now
	if update.UpdatedAt != nil {
		updatedAt = *update.UpdatedAt
	}
	store[update.ID] = Session{
		ID: update.ID, CWD: cwd, Title: title, CreatedAt: createdAt,
		UpdatedAt: max(updatedAt, previous.UpdatedAt, now),
	}
	if len(store) > maxSessions {
		entries := make([]Session, 0, len(store))
		for _, entry := range store {
			entries = append(entries, entry)
		}
		slices.SortStableFunc(entries, func(a, b Session) int {
			if a.UpdatedAt == b.UpdatedAt {
				return cmp.Compare(a.ID, b.ID)
			}
			return cmp.Compare(b.UpdatedAt, a.UpdatedAt)
		})
		for _, entry := range entries[maxSessions:] {
			delete(store, entry.ID)
		}
	}
	return s.writeObject(s.SessionsPath(), store)
}
