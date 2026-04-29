package cache

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

type Storage struct {
	mu  sync.RWMutex
	dir string
	mem map[string]*Entry
}

func NewStorage(cacheDir string) (*Storage, error) {
	if cacheDir == "" {
		return &Storage{
			mem: make(map[string]*Entry),
		}, nil
	}
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		return nil, fmt.Errorf("create cache directory: %w", err)
	}
	return &Storage{
		dir: cacheDir,
		mem: make(map[string]*Entry),
	}, nil
}

func NewMemoryStorage() *Storage {
	return &Storage{
		mem: make(map[string]*Entry),
	}
}

func (s *Storage) IsPersistent() bool {
	return s.dir != ""
}

func (s *Storage) Read(key string) (*Entry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.dir == "" {
		entry := s.mem[key]
		if entry == nil {
			return nil, nil
		}
		return s.cloneEntry(entry), nil
	}

	filePath := s.keyToPath(key)
	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read cache file: %w", err)
	}

	var entry Entry
	if err := json.Unmarshal(data, &entry); err != nil {
		return nil, fmt.Errorf("unmarshal cache entry: %w", err)
	}
	return &entry, nil
}

func (s *Storage) Write(entry *Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.dir == "" {
		s.mem[entry.Key] = s.cloneEntry(entry)
		return nil
	}

	filePath := s.keyToPath(entry.Key)
	data, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal cache entry: %w", err)
	}

	if err := os.WriteFile(filePath, data, 0644); err != nil {
		return fmt.Errorf("write cache file: %w", err)
	}
	return nil
}

func (s *Storage) Delete(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.dir == "" {
		delete(s.mem, key)
		return nil
	}

	filePath := s.keyToPath(key)
	if err := os.Remove(filePath); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("delete cache file: %w", err)
	}
	return nil
}

func (s *Storage) List() ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.dir == "" {
		keys := make([]string, 0, len(s.mem))
		for k := range s.mem {
			keys = append(keys, k)
		}
		return keys, nil
	}

	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("read cache directory: %w", err)
	}

	keys := make([]string, 0, len(entries))
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) == ".json" {
			key := entry.Name()[:len(entry.Name())-5]
			keys = append(keys, key)
		}
	}
	return keys, nil
}

func (s *Storage) Clear(key string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if key != "" {
		if s.dir == "" {
			if _, exists := s.mem[key]; exists {
				delete(s.mem, key)
				return 1, nil
			}
			return 0, nil
		}

		filePath := s.keyToPath(key)
		if err := os.Remove(filePath); err != nil {
			if os.IsNotExist(err) {
				return 0, nil
			}
			return 0, fmt.Errorf("delete cache file: %w", err)
		}
		return 1, nil
	}

	if s.dir == "" {
		count := len(s.mem)
		s.mem = make(map[string]*Entry)
		return count, nil
	}

	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return 0, fmt.Errorf("read cache directory: %w", err)
	}

	count := 0
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) == ".json" {
			filePath := filepath.Join(s.dir, entry.Name())
			if err := os.Remove(filePath); err != nil {
				continue
			}
			count++
		}
	}
	return count, nil
}

func (s *Storage) keyToPath(key string) string {
	return filepath.Join(s.dir, key+".json")
}

func (s *Storage) cloneEntry(entry *Entry) *Entry {
	if entry == nil {
		return nil
	}
	clone := &Entry{
		Version:   entry.Version,
		Key:       entry.Key,
		URL:       entry.URL,
		Timestamp: entry.Timestamp,
	}
	if entry.Actions != nil {
		clone.Actions = make([]Action, len(entry.Actions))
		copy(clone.Actions, entry.Actions)
	}
	if entry.Variables != nil {
		clone.Variables = make([]string, len(entry.Variables))
		copy(clone.Variables, entry.Variables)
	}
	if entry.Metadata != nil {
		clone.Metadata = make(map[string]any, len(entry.Metadata))
		for k, v := range entry.Metadata {
			clone.Metadata[k] = v
		}
	}
	return clone
}
