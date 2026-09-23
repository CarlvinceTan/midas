package storage

import (
	"cmp"
	"encoding/json"
	"slices"
)

// ImageChip identifies an image marker retained in an input draft.
type ImageChip struct {
	Marker string `json:"marker"`
	Path   string `json:"path"`
}

// FileChip identifies a generic file marker retained in an input draft or queue.
type FileChip struct {
	Marker string `json:"marker"`
	Path   string `json:"path"`
	ID     string `json:"id,omitempty"`
	Name   string `json:"name,omitempty"`
}

// Draft is unsent editor input for one session.
type Draft struct {
	Text        string      `json:"text"`
	Attachments []ImageChip `json:"attachments,omitempty"`
	Files       []FileChip  `json:"files,omitempty"`
}

type draftRecord struct {
	Draft
	UpdatedAt int64 `json:"updatedAt"`
}

func readDrafts(path string) map[string]draftRecord {
	result := make(map[string]draftRecord)
	for id, raw := range readObject(path) {
		var record draftRecord
		if json.Unmarshal(raw, &record) == nil {
			result[id] = record
		}
	}
	return result
}

// ReadDraft returns a copy of a session draft.
func (s *Store) ReadDraft(sessionID string) (Draft, bool) {
	if sessionID == "" {
		return Draft{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := readDrafts(s.DraftsPath())[sessionID]
	if !ok {
		return Draft{}, false
	}
	return record.Draft, true
}

// WriteDraft persists a draft; an empty draft removes the entry.
func (s *Store) WriteDraft(sessionID string, draft Draft) error {
	if sessionID == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	store := readDrafts(s.DraftsPath())
	if draft.Text == "" && len(draft.Attachments) == 0 && len(draft.Files) == 0 {
		if _, ok := store[sessionID]; !ok {
			return nil
		}
		delete(store, sessionID)
	} else {
		var newest int64
		for _, record := range store {
			if record.UpdatedAt > newest {
				newest = record.UpdatedAt
			}
		}
		store[sessionID] = draftRecord{Draft: draft, UpdatedAt: max(s.now().UnixMilli(), newest+1)}
	}
	pruneDrafts(store)
	return s.writeObject(s.DraftsPath(), store)
}

// DeleteDraft removes a draft if present.
func (s *Store) DeleteDraft(sessionID string) error {
	if sessionID == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	store := readDrafts(s.DraftsPath())
	if _, ok := store[sessionID]; !ok {
		return nil
	}
	delete(store, sessionID)
	return s.writeObject(s.DraftsPath(), store)
}

func pruneDrafts(store map[string]draftRecord) {
	if len(store) <= maxDrafts {
		return
	}
	ids := make([]string, 0, len(store))
	for id := range store {
		ids = append(ids, id)
	}
	slices.SortStableFunc(ids, func(a, b string) int {
		left, right := store[a].UpdatedAt, store[b].UpdatedAt
		if left == right {
			return cmp.Compare(a, b)
		}
		return cmp.Compare(right, left)
	})
	for _, id := range ids[maxDrafts:] {
		delete(store, id)
	}
}
