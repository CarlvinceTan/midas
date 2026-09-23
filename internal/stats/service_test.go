package stats

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestServiceLoadsStaleCacheThenRefreshes(t *testing.T) {
	directory := t.TempDir()
	now := time.UnixMilli(1_000_000)
	encoded, _ := json.Marshal(map[string]any{
		"at":   now.Add(-10 * time.Minute).UnixMilli(),
		"data": Summary{Cost: 1},
	})
	if err := os.WriteFile(filepath.Join(directory, cacheFile), encoded, 0o644); err != nil {
		t.Fatal(err)
	}
	scans := 0
	service := New(Options{Directory: directory, Now: func() time.Time { return now }, Scan: func(context.Context) (Summary, error) {
		scans++
		return Summary{Cost: 2}, nil
	}})
	if cached, ok := service.Cached(); !ok || cached.Cost != 1 {
		t.Fatalf("cached = %#v, %v", cached, ok)
	}
	if service.Fresh() {
		t.Fatal("exactly ten-minute-old cache is fresh")
	}
	got, err := service.Read(context.Background())
	if err != nil || scans != 1 || got.Cost != 2 {
		t.Fatalf("read = %#v scans=%d err=%v", got, scans, err)
	}
	loaded := New(Options{Directory: directory, Now: func() time.Time { return now }})
	if !loaded.Fresh() {
		t.Fatal("written cache is not fresh")
	}
}

func TestServiceFutureTimestampIsFresh(t *testing.T) {
	directory := t.TempDir()
	now := time.UnixMilli(1_000)
	if err := os.WriteFile(filepath.Join(directory, cacheFile), []byte(`{"at":2000,"data":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	service := New(Options{Directory: directory, Now: func() time.Time { return now }})
	if !service.Fresh() {
		t.Fatal("future cache is stale")
	}
}

func TestServiceCoalescesRefreshes(t *testing.T) {
	directory := t.TempDir()
	started := make(chan struct{})
	release := make(chan struct{})
	var mu sync.Mutex
	scans := 0
	service := New(Options{Directory: directory, Scan: func(context.Context) (Summary, error) {
		mu.Lock()
		scans++
		mu.Unlock()
		close(started)
		<-release
		return Summary{Calls: 1}, nil
	}})
	type result struct {
		data Summary
		err  error
	}
	results := make(chan result, 2)
	go func() { data, err := service.Refresh(context.Background()); results <- result{data, err} }()
	<-started
	go func() { data, err := service.Refresh(context.Background()); results <- result{data, err} }()
	close(release)
	first, second := <-results, <-results
	if first.err != nil || second.err != nil || first.data.Calls != 1 || second.data.Calls != 1 {
		t.Fatalf("results = %#v %#v", first, second)
	}
	mu.Lock()
	defer mu.Unlock()
	if scans != 1 {
		t.Fatalf("scans = %d", scans)
	}
}

func TestServiceFailedScanPreservesStaleCache(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, cacheFile), []byte(`{"at":0,"data":{"cost":1}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	service := New(Options{Directory: directory, Scan: func(context.Context) (Summary, error) {
		return Summary{}, errors.New("scan failed")
	}})
	if _, err := service.Refresh(context.Background()); err == nil {
		t.Fatal("expected scan failure")
	}
	cached, ok := service.Cached()
	if !ok || cached.Cost != 1 {
		t.Fatalf("cached = %#v, %v", cached, ok)
	}
}

func TestServiceCancelledWaiterDoesNotCancelSharedScan(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var startOnce sync.Once
	service := New(Options{Directory: t.TempDir(), Scan: func(ctx context.Context) (Summary, error) {
		// The service may run this closure again once the first scan has finished,
		// so the signal is sent exactly once.
		startOnce.Do(func() { close(started) })
		select {
		case <-ctx.Done():
			return Summary{}, ctx.Err()
		case <-release:
			return Summary{Calls: 1}, nil
		}
	}})
	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() { _, err := service.Refresh(ctx); first <- err }()
	<-started
	cancel()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error = %v", err)
	}
	second := make(chan error, 1)
	go func() { _, err := service.Refresh(context.Background()); second <- err }()
	close(release)
	if err := <-second; err != nil {
		t.Fatal(err)
	}
}
