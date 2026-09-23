package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// CatalogEntry is one installable bridge in a registry. A registry is an optional
// catalog of bridges, so an agent can install one by name instead of being told a
// source path, and so `hub_bridges available` can answer without searching the
// network.
//
// The hub ships no catalog of its own: a registry is either a path or a URL in
// `registry` in hub.json, and with that field empty the hub fetches nothing and
// an install must name its source. That keeps an unconfigured hub offline.
type CatalogEntry struct {
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Source      string            `json:"source"`
	Checksum    string            `json:"checksum,omitempty"`
	Command     []string          `json:"command"`
	Env         map[string]string `json:"env,omitempty"`
	// Installed is filled in when listing, not read from the registry.
	Installed bool `json:"installed,omitempty"`
}

type catalogFile struct {
	Bridges []CatalogEntry `json:"bridges"`
}

// Available lists the bridges the configured registry offers, marking the ones
// already installed. An unconfigured hub returns nothing rather than reaching
// out to the network.
func (h *Hub) Available(ctx context.Context, refresh bool) ([]CatalogEntry, error) {
	registry := strings.TrimSpace(h.Config().Registry)
	if registry == "" {
		return nil, nil
	}
	entries, err := h.catalog(ctx, registry, refresh)
	if err != nil {
		return nil, err
	}
	installed := map[string]bool{}
	for _, status := range h.BridgeStatuses() {
		installed[status.Name] = true
	}
	for index := range entries {
		entries[index].Installed = installed[entries[index].Name]
	}
	return entries, nil
}

// lookup returns the registry entry for one bridge name.
func (h *Hub) lookup(ctx context.Context, name string) (CatalogEntry, bool, error) {
	entries, err := h.Available(ctx, false)
	if err != nil {
		return CatalogEntry{}, false, err
	}
	for _, entry := range entries {
		if entry.Name == name {
			return entry, true, nil
		}
	}
	return CatalogEntry{}, false, nil
}

// catalog reads the registry, using the cached copy until it goes stale so a
// listing does not fetch on every call.
func (h *Hub) catalog(ctx context.Context, registry string, refresh bool) ([]CatalogEntry, error) {
	cachePath := filepath.Join(h.stateDir, "registry.json")
	if !refresh {
		if entries, ok := readCatalogCache(cachePath, h.Config().RegistryTTL()); ok {
			return entries, nil
		}
	}
	entries, err := fetchCatalog(ctx, registry)
	if err != nil {
		return nil, err
	}
	writeCatalogCache(cachePath, entries)
	return entries, nil
}

func fetchCatalog(ctx context.Context, registry string) ([]CatalogEntry, error) {
	var data []byte
	if strings.HasPrefix(registry, "http://") || strings.HasPrefix(registry, "https://") {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, registry, nil)
		if err != nil {
			return nil, err
		}
		client := &http.Client{Timeout: 20 * time.Second}
		response, err := client.Do(request)
		if err != nil {
			return nil, fmt.Errorf("hub: read registry %s: %w", registry, err)
		}
		defer response.Body.Close()
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return nil, fmt.Errorf("hub: read registry %s: HTTP %d", registry, response.StatusCode)
		}
		data, err = readLimited(response.Body)
		if err != nil {
			return nil, err
		}
	} else {
		contents, err := os.ReadFile(registry)
		if err != nil {
			return nil, fmt.Errorf("hub: read registry %s: %w", registry, err)
		}
		data = contents
	}
	var parsed catalogFile
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, fmt.Errorf("hub: parse registry %s: %w", registry, err)
	}
	entries := make([]CatalogEntry, 0, len(parsed.Bridges))
	for _, entry := range parsed.Bridges {
		if strings.TrimSpace(entry.Name) == "" || strings.TrimSpace(entry.Source) == "" || len(entry.Command) == 0 {
			return nil, fmt.Errorf("hub: registry %s has an entry without a name, source, or command", registry)
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

// cachedCatalog is the on-disk copy, with the time it was fetched.
type cachedCatalog struct {
	FetchedAt int64          `json:"fetchedAt"`
	Entries   []CatalogEntry `json:"bridges"`
}

func readCatalogCache(path string, ttl time.Duration) ([]CatalogEntry, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var cached cachedCatalog
	if json.Unmarshal(data, &cached) != nil {
		return nil, false
	}
	if ttl <= 0 || time.Since(time.UnixMilli(cached.FetchedAt)) > ttl {
		return nil, false
	}
	return cached.Entries, true
}

func writeCatalogCache(path string, entries []CatalogEntry) {
	encoded, err := json.Marshal(cachedCatalog{FetchedAt: time.Now().UnixMilli(), Entries: entries})
	if err != nil {
		return
	}
	_ = os.WriteFile(path, append(encoded, '\n'), 0o600)
}

// readLimited reads a registry, refusing anything larger than a megabyte.
func readLimited(source io.Reader) ([]byte, error) {
	const limit = 1 << 20
	buffer, err := io.ReadAll(io.LimitReader(source, limit+1))
	if err != nil {
		return nil, err
	}
	if len(buffer) > limit {
		return nil, errors.New("hub: registry is larger than 1 MB")
	}
	return buffer, nil
}
