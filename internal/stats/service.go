// Package stats caches Midas's own usage totals. Collection is supplied by the
// Midas application; this package never discovers or scans other agents.
package stats

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/CarlvinceTan/midas/internal/storage"
)

const (
	cacheFile  = "stats-cache.json"
	defaultTTL = 10 * time.Minute
)

type Tokens struct {
	Input      int64 `json:"input"`
	Output     int64 `json:"output"`
	CacheRead  int64 `json:"cacheRead"`
	CacheWrite int64 `json:"cacheWrite"`
}

// Summary is Midas's lifetime usage snapshot. Cost is unrounded USD.
//
// Every assistant message counts, whichever agent produced it, so delegated or
// background runs land in the same totals as interactive ones. Cache figures are
// derived from what the provider reported per call: CacheRead is prompt content
// served from its cache, and CacheWrite plus Input is what had to be processed
// fresh, so HitRate is the share of prompt tokens that were cache hits.
type Summary struct {
	Cost     float64 `json:"cost"`
	Sessions int64   `json:"sessions"`
	Calls    int64   `json:"calls"`
	Tokens   Tokens  `json:"tokens"`
	// SessionCache holds one entry per session with usage, newest last.
	SessionCache []SessionCache `json:"sessionCache,omitempty"`
}

// SessionCache is one session's cache accounting.
type SessionCache struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Calls int64  `json:"calls"`
	Tokens
}

// PromptTokens is every prompt token the provider billed for a request: cached
// reads, cache writes, and uncached input.
func (t Tokens) PromptTokens() int64 {
	return t.CacheRead + t.CacheWrite + t.Input
}

// HitRate is the share of prompt tokens served from the provider's cache.
func (t Tokens) HitRate() float64 {
	if total := t.PromptTokens(); total > 0 {
		return float64(t.CacheRead) / float64(total)
	}
	return 0
}

// HitRate is the lifetime cache hit rate across every recorded call.
func (s Summary) HitRate() float64 { return s.Tokens.HitRate() }

type ScanFunc func(context.Context) (Summary, error)

type Options struct {
	Directory string
	TTL       time.Duration
	Now       func() time.Time
	Scan      ScanFunc
}

type snapshot struct {
	at   float64
	data Summary
}

type flight struct {
	done chan struct{}
	data Summary
	err  error
}

// Service provides stale-while-refresh disk caching and process-local scan
// coalescing for Midas usage statistics.
type Service struct {
	directory string
	ttl       time.Duration
	now       func() time.Time
	scan      ScanFunc

	mu       sync.Mutex
	cache    *snapshot
	inFlight *flight
}

func New(options Options) *Service {
	directory := options.Directory
	if directory == "" {
		directory = storage.ConfigDir()
	}
	ttl := options.TTL
	if ttl == 0 {
		ttl = defaultTTL
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	return &Service{directory: directory, ttl: ttl, now: now, scan: options.Scan}
}

func (s *Service) path() string { return filepath.Join(s.directory, cacheFile) }

func parseTimestamp(raw json.RawMessage) float64 {
	if len(raw) == 0 || string(raw) == "null" {
		return 0
	}
	var number json.Number
	if json.Unmarshal(raw, &number) == nil {
		if value, err := number.Float64(); err == nil {
			return value
		}
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		if value, err := strconv.ParseFloat(text, 64); err == nil {
			return value
		}
	}
	return 0
}

func (s *Service) loadDisk() *snapshot {
	data, err := os.ReadFile(s.path())
	if err != nil {
		return nil
	}
	var envelope struct {
		At   json.RawMessage `json:"at"`
		Data json.RawMessage `json:"data"`
	}
	if json.Unmarshal(data, &envelope) != nil {
		return nil
	}
	rawData := bytes.TrimSpace(envelope.Data)
	if len(rawData) == 0 || rawData[0] != '{' {
		return nil
	}
	var summary Summary
	if json.Unmarshal(rawData, &summary) != nil {
		return nil
	}
	return &snapshot{at: parseTimestamp(envelope.At), data: summary}
}

// Cached returns the last Midas snapshot, including stale data.
func (s *Service) Cached() (Summary, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cache == nil {
		s.cache = s.loadDisk()
	}
	if s.cache == nil {
		return Summary{}, false
	}
	return s.cache.data, true
}

func (s *Service) Fresh() bool {
	if _, ok := s.Cached(); !ok {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return float64(s.now().UnixMilli())-s.cache.at < float64(s.ttl.Milliseconds())
}

func (s *Service) Read(ctx context.Context) (Summary, error) {
	if cached, ok := s.Cached(); ok && s.Fresh() {
		return cached, nil
	}
	return s.Refresh(ctx)
}

// Refresh forces a Midas scan. Concurrent callers share one scan; cancelling
// one waiter does not cancel work still needed by other waiters.
func (s *Service) Refresh(ctx context.Context) (Summary, error) {
	s.mu.Lock()
	current := s.inFlight
	if current == nil {
		current = &flight{done: make(chan struct{})}
		s.inFlight = current
		go s.runScan(context.WithoutCancel(ctx), current)
	}
	s.mu.Unlock()

	select {
	case <-current.done:
		return current.data, current.err
	case <-ctx.Done():
		return Summary{}, ctx.Err()
	}
}

func (s *Service) runScan(ctx context.Context, current *flight) {
	var data Summary
	var err error
	func() {
		defer func() {
			if recover() != nil {
				err = errors.New("midas statistics scan panicked")
			}
		}()
		if s.scan == nil {
			err = errors.New("midas statistics scanner is required")
			return
		}
		data, err = s.scan(ctx)
	}()
	if err == nil {
		completedAt := float64(s.now().UnixMilli())
		s.mu.Lock()
		s.cache = &snapshot{at: completedAt, data: data}
		s.mu.Unlock()
		s.write(snapshot{at: completedAt, data: data})
	}

	s.mu.Lock()
	current.data, current.err = data, err
	if s.inFlight == current {
		s.inFlight = nil
	}
	close(current.done)
	s.mu.Unlock()
}

func (s *Service) write(value snapshot) {
	encoded, err := json.Marshal(struct {
		At   float64 `json:"at"`
		Data Summary `json:"data"`
	}{At: value.at, Data: value.data})
	if err != nil || os.MkdirAll(s.directory, 0o755) != nil {
		return
	}
	_ = os.WriteFile(s.path(), encoded, 0o644)
}
