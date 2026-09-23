package hub

import (
	"bufio"
	"cmp"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// Store keeps the hub's own state: the messages it has seen and cursors a client
// uses to page through them. Version one is an append-only JSONL file plus an
// in-memory index, which is enough for a local homeserver and keeps the hub
// dependency-free; the Store interface is the seam for a real database later.
type Store struct {
	mu       sync.RWMutex
	messages []Message
	byID     map[string]bool
	path     string
	// reads is the read cursor per account/thread, so unread counts survive a
	// restart without asking a bridge to replay anything.
	reads     map[string]int64
	readsPath string
	// threads holds what the bridges said about conversations, keyed like reads:
	// kind, community, participants, and the service's own muted/archived flags.
	threads     map[string]Thread
	threadsPath string
}

// readKey identifies one thread's read cursor.
func readKey(account, thread string) string { return account + "\x00" + thread }

// rewriteMessages replaces the message log atomically: a crash mid-write must not
// lose what the hub already had. The lock is held by the caller.
func (s *Store) rewriteMessagesLocked() error {
	encoded := make([]byte, 0, 64*1024)
	for _, message := range s.messages {
		line, err := json.Marshal(message)
		if err != nil {
			return err
		}
		encoded = append(encoded, line...)
		encoded = append(encoded, '\n')
	}
	return writeAtomic(s.path, encoded)
}

// writeAtomic replaces a file with owner-only permissions.
func writeAtomic(path string, data []byte) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// Edit replaces a message's text, which is what a bridge reports when the sender
// corrected it.
func (s *Store) Edit(account, id, text string, at int64) (Message, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	index := s.indexLocked(account, id)
	if index < 0 {
		return Message{}, false
	}
	s.messages[index].Text = text
	if len(s.messages[index].Media) == 0 {
		s.messages[index].Kind = KindText
	}
	s.messages[index].Edited = true
	if at > 0 {
		s.messages[index].Timestamp = at
	}
	if err := s.rewriteMessagesLocked(); err != nil {
		return Message{}, false
	}
	return s.messages[index], true
}

// Delete clears a message's content, keeping the record so a client's history does
// not shift under it.
func (s *Store) Delete(account, id string) (Message, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	index := s.indexLocked(account, id)
	if index < 0 {
		return Message{}, false
	}
	s.messages[index].Text = ""
	s.messages[index].Media = nil
	s.messages[index].Deleted = true
	if err := s.rewriteMessagesLocked(); err != nil {
		return Message{}, false
	}
	return s.messages[index], true
}

// React folds a reaction into a message: an empty emoji, or one with Removed set,
// takes that sender's reaction away.
func (s *Store) React(account, id string, reaction Reaction) (Message, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	index := s.indexLocked(account, id)
	if index < 0 {
		return Message{}, false
	}
	sender := strings.TrimSpace(reaction.Sender)
	reactions := s.messages[index].Reactions[:0]
	for _, existing := range s.messages[index].Reactions {
		// One reaction per sender, which is what the chat services do.
		if sender != "" && strings.EqualFold(strings.TrimSpace(existing.Sender), sender) {
			continue
		}
		reactions = append(reactions, existing)
	}
	if strings.TrimSpace(reaction.Emoji) != "" && !reaction.Removed {
		reactions = append(reactions, reaction)
	}
	s.messages[index].Reactions = reactions
	if err := s.rewriteMessagesLocked(); err != nil {
		return Message{}, false
	}
	return s.messages[index], true
}

// SetThread records what a bridge says about a conversation: its kind, the
// community it belongs to, and who is in it. A thread with no messages yet — an
// empty channel in a space — is stored so a client can still list it.
func (s *Store) SetThread(thread Thread) error {
	if strings.TrimSpace(thread.Account) == "" || strings.TrimSpace(thread.ID) == "" {
		return errors.New("hub: a thread needs an account and an id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.threads == nil {
		s.threads = map[string]Thread{}
	}
	key := readKey(thread.Account, thread.ID)
	existing := s.threads[key]
	// The bridge owns the description; the store owns the counters.
	existing.ID, existing.Account = thread.ID, thread.Account
	if thread.Title != "" {
		existing.Title = thread.Title
	}
	if thread.Bridge != "" {
		existing.Bridge = thread.Bridge
	}
	if thread.Kind != "" {
		existing.Kind = thread.Kind
	}
	if thread.Parent != "" {
		existing.Parent = thread.Parent
	}
	if len(thread.Participants) > 0 {
		existing.Participants = append([]string(nil), thread.Participants...)
	}
	// Flags are only changed when the report actually carries them.
	if thread.Muted != nil {
		existing.Muted = thread.Muted
	}
	if thread.Archived != nil {
		existing.Archived = thread.Archived
	}
	// A service reports how much is waiting before a client has read any history,
	// and it is the service that knows; a client that has read the thread clears it.
	if thread.Unread > existing.Unread {
		existing.Unread = thread.Unread
	}
	if thread.LastMessage > existing.LastMessage {
		existing.LastMessage = thread.LastMessage
	}
	s.threads[key] = existing
	return s.writeThreadsLocked()
}

// writeThreadsLocked persists the thread descriptions. The caller holds the lock.
func (s *Store) writeThreadsLocked() error {
	encoded, err := json.MarshalIndent(s.threads, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(s.threadsPath, append(encoded, '\n'))
}

// loadThreads reads the thread descriptions written by SetThread.
func (s *Store) loadThreads() error {
	data, err := os.ReadFile(s.threadsPath)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(data, &s.threads)
}

// indexLocked finds a message by ID, scoped to an account when one is named.
func (s *Store) indexLocked(account, id string) int {
	id = strings.TrimSpace(id)
	if id == "" {
		return -1
	}
	for index := range s.messages {
		if s.messages[index].ID != id {
			continue
		}
		if account == "" || strings.EqualFold(s.messages[index].Account, account) {
			return index
		}
	}
	return -1
}

// OpenStore loads the hub's state directory, creating it when missing.
func OpenStore(directory string) (*Store, error) {
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, err
	}
	store := &Store{
		byID: map[string]bool{}, path: filepath.Join(directory, "messages.jsonl"),
		reads: map[string]int64{}, readsPath: filepath.Join(directory, "reads.json"),
		threads: map[string]Thread{}, threadsPath: filepath.Join(directory, "threads.json"),
	}
	if err := store.load(); err != nil {
		return nil, err
	}
	if err := store.loadReads(); err != nil {
		return nil, err
	}
	if err := store.loadThreads(); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *Store) loadReads() error {
	data, err := os.ReadFile(s.readsPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(data, &s.reads)
}

// MarkRead records that everything up to timestamp has been read in a thread.
func (s *Store) MarkRead(account, thread string, upTo int64) error {
	if upTo == 0 {
		upTo = time.Now().UnixMilli()
	}
	// The write happens under the lock so two concurrent marks cannot persist a
	// cursor older than the one already recorded.
	s.mu.Lock()
	defer s.mu.Unlock()
	key := readKey(account, thread)
	if existing, ok := s.reads[key]; ok && existing >= upTo {
		return nil
	}
	s.reads[key] = upTo
	// A thread description is what the service last reported; reading the thread
	// clears that count as well, or a client would keep seeing unread messages it
	// has already read.
	if described, ok := s.threads[key]; ok && described.Unread != 0 {
		described.Unread = 0
		s.threads[key] = described
		if err := s.writeThreadsLocked(); err != nil {
			return err
		}
	}
	encoded, err := json.MarshalIndent(s.reads, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(s.readsPath, append(encoded, '\n'))
}

// Threads lists the conversations the hub has seen, newest activity first, with
// unread counts against the read cursors.
func (s *Store) Threads(account string) []Thread {
	s.mu.RLock()
	defer s.mu.RUnlock()
	index := map[string]*Thread{}
	order := []string{}
	// A thread the bridge described starts from that description, so an empty
	// channel in a space is still listed.
	for key, described := range s.threads {
		if account != "" && described.Account != account {
			continue
		}
		copied := described
		copied.Participants = append([]string(nil), described.Participants...)
		index[key] = &copied
		order = append(order, key)
	}
	for _, message := range s.messages {
		if account != "" && message.Account != account {
			continue
		}
		key := readKey(message.Account, message.Thread)
		thread, ok := index[key]
		if !ok {
			thread = &Thread{ID: message.Thread, Account: message.Account}
			index[key] = thread
			order = append(order, key)
		}
		if message.Timestamp > thread.LastMessage {
			thread.LastMessage = message.Timestamp
		}
		if message.Incoming && message.Timestamp > s.reads[key] {
			thread.Unread++
		}
		if thread.Title == "" && message.Sender != "" {
			thread.Title = message.Sender
		}
	}
	threads := make([]Thread, 0, len(order))
	for _, key := range order {
		thread := *index[key]
		// A bridge reports how much the service says is unread, which is the count a
		// client needs before it has read any history; the messages the hub has
		// stored can only add to it.
		if described, ok := s.threads[key]; ok && described.Unread > thread.Unread {
			thread.Unread = described.Unread
		}
		threads = append(threads, thread)
	}
	slices.SortStableFunc(threads, func(a, b Thread) int { return cmp.Compare(b.LastMessage, a.LastMessage) })
	return threads
}

func (s *Store) load() error {
	file, err := os.Open(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 8<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var message Message
		if err := json.Unmarshal([]byte(line), &message); err != nil {
			continue
		}
		if message.ID == "" || s.byID[message.ID] {
			continue
		}
		s.byID[message.ID] = true
		s.messages = append(s.messages, message)
	}
	return scanner.Err()
}

// Append records a message, ignoring one that is already stored.
func (s *Store) Append(message Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if message.ID == "" || s.byID[message.ID] {
		return nil
	}
	s.byID[message.ID] = true
	s.messages = append(s.messages, message)
	file, err := os.OpenFile(s.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	encoded, err := json.Marshal(message)
	if err != nil {
		return err
	}
	_, err = file.Write(append(encoded, '\n'))
	return err
}

// Messages returns messages for an account and optional thread, newest last,
// bounded by limit. A limit of zero means the store's default of 50.
func (s *Store) Messages(account, thread string, limit int) []Message {
	if limit <= 0 {
		limit = 50
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	found := make([]Message, 0, limit)
	for _, message := range s.messages {
		if account != "" && message.Account != account {
			continue
		}
		if thread != "" && message.Thread != thread {
			continue
		}
		found = append(found, message)
	}
	slices.SortStableFunc(found, func(a, b Message) int { return cmp.Compare(a.Timestamp, b.Timestamp) })
	if len(found) > limit {
		found = found[len(found)-limit:]
	}
	return found
}

// Count reports how many messages the hub holds.
func (s *Store) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.messages)
}
