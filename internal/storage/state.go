package storage

import (
	"cmp"
	"encoding/json"
	"slices"
	"unicode/utf16"
)

// BashEntry is one persisted local shell execution.
type BashEntry struct {
	Command  string `json:"command"`
	Output   string `json:"output"`
	Exclude  bool   `json:"exclude"`
	Status   string `json:"status"`
	ExitCode *int   `json:"exitCode,omitempty"`
	At       int64  `json:"at"`
}

// Attachment is an inline prompt attachment.
type Attachment struct {
	MIME     string `json:"mime"`
	Filename string `json:"filename"`
	URL      string `json:"url"`
}

// FrozenFile is a queued file payload captured when the prompt was prepared.
type FrozenFile struct {
	ID         string      `json:"id,omitempty"`
	Marker     string      `json:"marker"`
	Path       string      `json:"path"`
	Name       string      `json:"name"`
	Kind       string      `json:"kind"`
	Content    string      `json:"content,omitempty"`
	Attachment *Attachment `json:"attachment,omitempty"`
}

// QueuedPrompt is one persisted follow-up.
type QueuedPrompt struct {
	Text          string       `json:"text"`
	Attachments   []Attachment `json:"attachments,omitempty"`
	Chips         []ImageChip  `json:"chips,omitempty"`
	Files         []FileChip   `json:"files,omitempty"`
	FrozenFiles   []FrozenFile `json:"frozenFiles,omitempty"`
	UnfrozenFiles []FileChip   `json:"unfrozenFiles,omitempty"`
	NeedsReattach []FileChip   `json:"needsReattach,omitempty"`
}

func (q QueuedPrompt) MarshalJSON() ([]byte, error) {
	object := map[string]any{"text": q.Text}
	if q.Attachments != nil {
		object["attachments"] = q.Attachments
	}
	if len(q.Chips) > 0 {
		object["chips"] = q.Chips
	}
	if len(q.Files) > 0 {
		object["files"] = q.Files
	}
	if len(q.FrozenFiles) > 0 {
		object["frozenFiles"] = q.FrozenFiles
	}
	if len(q.UnfrozenFiles) > 0 {
		object["unfrozenFiles"] = q.UnfrozenFiles
	}
	if len(q.NeedsReattach) > 0 {
		object["needsReattach"] = q.NeedsReattach
	}
	return json.Marshal(object)
}

// SessionState is Midas-owned client state stored alongside a conversation.
type SessionState struct {
	CWD       string         `json:"cwd,omitempty"`
	Bash      []BashEntry    `json:"bash,omitempty"`
	Queue     []QueuedPrompt `json:"queue,omitempty"`
	QueueHold bool           `json:"queueHold,omitempty"`
}

type sessionStateRecord struct {
	SessionState
	UpdatedAt int64 `json:"updatedAt"`
}

func readSessionStates(path string) map[string]sessionStateRecord {
	result := make(map[string]sessionStateRecord)
	for id, raw := range readObject(path) {
		var record sessionStateRecord
		if json.Unmarshal(raw, &record) == nil {
			result[id] = record
		}
	}
	return result
}

// ReadSessionState returns client-side state for one session.
func (s *Store) ReadSessionState(sessionID string) (SessionState, bool) {
	if sessionID == "" {
		return SessionState{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := readSessionStates(s.SessionStatePath())[sessionID]
	if !ok {
		return SessionState{}, false
	}
	return record.SessionState, true
}

// WriteSessionState bounds and persists state; an empty state removes it.
func (s *Store) WriteSessionState(sessionID string, state SessionState) error {
	if sessionID == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	store := readSessionStates(s.SessionStatePath())
	normalized := normalizeSessionState(state)
	if normalized.CWD == "" && len(normalized.Bash) == 0 && len(normalized.Queue) == 0 && !normalized.QueueHold {
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
		store[sessionID] = sessionStateRecord{SessionState: normalized, UpdatedAt: max(s.now().UnixMilli(), newest+1)}
	}
	pruneSessionStates(store)
	return s.writeObject(s.SessionStatePath(), store)
}

// DeleteSessionState removes client-side state if present.
func (s *Store) DeleteSessionState(sessionID string) error {
	if sessionID == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	store := readSessionStates(s.SessionStatePath())
	if _, ok := store[sessionID]; !ok {
		return nil
	}
	delete(store, sessionID)
	return s.writeObject(s.SessionStatePath(), store)
}

func normalizeSessionState(state SessionState) SessionState {
	result := SessionState{CWD: state.CWD, QueueHold: state.QueueHold}
	if len(state.Bash) > 0 {
		start := 0
		if len(state.Bash) > maxBashEntries {
			start = len(state.Bash) - maxBashEntries
		}
		result.Bash = make([]BashEntry, len(state.Bash)-start)
		copy(result.Bash, state.Bash[start:])
		for index := range result.Bash {
			result.Bash[index].Output = utf16Suffix(result.Bash[index].Output, maxBashOutput)
		}
	}
	if len(state.Queue) > 0 {
		start := 0
		if len(state.Queue) > maxQueuedPrompts {
			start = len(state.Queue) - maxQueuedPrompts
		}
		result.Queue = make([]QueuedPrompt, 0, len(state.Queue)-start)
		for _, prompt := range state.Queue[start:] {
			result.Queue = append(result.Queue, normalizeQueuedPrompt(prompt))
		}
	}
	return result
}

func normalizeQueuedPrompt(prompt QueuedPrompt) QueuedPrompt {
	result := QueuedPrompt{Text: prompt.Text}
	if prompt.Attachments != nil {
		result.Attachments = make([]Attachment, 0, len(prompt.Attachments))
		for _, attachment := range prompt.Attachments {
			if utf16Length(attachment.URL) <= maxAttachmentURL {
				result.Attachments = append(result.Attachments, attachment)
			}
		}
	}
	if len(prompt.Chips) > 0 {
		result.Chips = append([]ImageChip(nil), prompt.Chips...)
	}
	if len(prompt.Files) > 0 {
		result.Files = append([]FileChip(nil), prompt.Files...)
	}
	frozenKeys := make(map[string]struct{})
	for _, file := range prompt.FrozenFiles {
		keep := utf16Length(file.Content) <= maxFrozenFileContent
		if file.Kind == "image" {
			length := 0
			if file.Attachment != nil {
				length = utf16Length(file.Attachment.URL)
			}
			keep = length <= maxAttachmentURL
		}
		if !keep {
			continue
		}
		result.FrozenFiles = append(result.FrozenFiles, file)
		frozenKeys[fileKey(file.ID, file.Path)] = struct{}{}
	}
	seen := make(map[string]struct{})
	markUnfrozen := func(file FileChip) {
		key := fileKey(file.ID, file.Path)
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		result.UnfrozenFiles = append(result.UnfrozenFiles, file)
	}
	for _, file := range prompt.NeedsReattach {
		markUnfrozen(file)
	}
	for _, file := range prompt.Files {
		if _, frozen := frozenKeys[fileKey(file.ID, file.Path)]; !frozen {
			markUnfrozen(file)
		}
	}
	return result
}

func fileKey(id, path string) string {
	if id != "" {
		return id
	}
	return path
}

func utf16Length(value string) int { return len(utf16.Encode([]rune(value))) }

func utf16Suffix(value string, maximum int) string {
	encoded := utf16.Encode([]rune(value))
	if len(encoded) <= maximum {
		return value
	}
	return string(utf16.Decode(encoded[len(encoded)-maximum:]))
}

func pruneSessionStates(store map[string]sessionStateRecord) {
	if len(store) <= maxSessionStates {
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
	for _, id := range ids[maxSessionStates:] {
		delete(store, id)
	}
}
