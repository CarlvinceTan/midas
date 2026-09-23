package storage

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/CarlvinceTan/midas/pkg/ai"
)

func (s *Store) TranscriptsDir() string { return filepath.Join(s.dir, "transcripts") }

func (s *Store) TranscriptPath(id string) (string, error) {
	id = strings.TrimSpace(id)
	if id == "" || id == "." || id == ".." || filepath.Base(id) != id || strings.ContainsAny(id, `/\\`) {
		return "", errors.New("sessions: invalid session id")
	}
	return filepath.Join(s.TranscriptsDir(), id+".json"), nil
}

func (s *Store) WriteTranscript(id string, messages []ai.Message) error {
	path, err := s.TranscriptPath(id)
	if err != nil {
		return err
	}
	data, err := ai.MarshalMessages(messages)
	if err != nil {
		return fmt.Errorf("sessions: encode transcript: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".transcript-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(append(data, '\n')); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("sessions: commit transcript: %w", err)
	}
	return nil
}

func (s *Store) ReadTranscript(id string) ([]ai.Message, error) {
	path, err := s.TranscriptPath(id)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	messages, err := ai.UnmarshalMessages(data)
	if err != nil {
		return nil, fmt.Errorf("sessions: decode transcript: %w", err)
	}
	return messages, nil
}
