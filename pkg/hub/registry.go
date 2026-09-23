package hub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// InstallOptions describe one bridge installation. Either the source is named, or
// the name is found in the configured registry; with neither, the hub refuses
// rather than guessing.
type InstallOptions struct {
	Name     string
	Source   string
	Checksum string
	Command  []string
	Env      map[string]string
}

// Install copies a bridge into the hub's own state directory, verifies its
// checksum when one is given, and records it in the configuration. The bridge is
// not started: it starts the first time something calls it.
func (h *Hub) Install(ctx context.Context, options InstallOptions) (BridgeStatus, error) {
	name := strings.TrimSpace(options.Name)
	if name == "" {
		return BridgeStatus{}, errors.New("hub: install needs a bridge name")
	}
	source := strings.TrimSpace(options.Source)
	checksum := options.Checksum
	command := options.Command
	environment := options.Env
	if source == "" {
		// No source given: the name has to come from the configured registry.
		entry, found, err := h.lookup(ctx, name)
		if err != nil {
			return BridgeStatus{}, err
		}
		if !found {
			if available, _ := h.Available(ctx, false); len(available) == 0 {
				return BridgeStatus{}, fmt.Errorf("hub: no bridge named %q and no registry is configured; install with an explicit source", name)
			}
			return BridgeStatus{}, fmt.Errorf("hub: no bridge named %q in the registry; available: %s", name, catalogNames(mustAvailable(ctx, h)))
		}
		source, checksum = entry.Source, entry.Checksum
		if len(command) == 0 {
			command = entry.Command
		}
		environment = entry.Env
	}
	if len(command) == 0 {
		return BridgeStatus{}, errors.New("hub: install needs the command that runs the bridge")
	}
	if err := validateBridgeName(name); err != nil {
		return BridgeStatus{}, err
	}
	directory := filepath.Join(h.stateDir, "bridges", name)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return BridgeStatus{}, err
	}
	// Only the file name of the command is used: a command that contains
	// separators or .. must not place the downloaded binary outside the bridge's
	// own directory.
	binary := filepath.Join(directory, filepath.Base(filepath.Clean(strings.TrimSpace(command[0]))))
	if err := fetch(ctx, source, binary, checksum); err != nil {
		return BridgeStatus{}, err
	}
	if err := os.Chmod(binary, 0o700); err != nil {
		return BridgeStatus{}, err
	}
	resolvedCommand := append([]string(nil), command...)
	resolvedCommand[0] = binary

	bridge := BridgeConfig{Source: source, Checksum: checksum, Command: resolvedCommand, Env: environment}
	if err := h.updateConfig(func(config *Config) { config.Bridges[name] = bridge }); err != nil {
		return BridgeStatus{}, err
	}
	h.supervisor.add(name, bridge)
	return h.supervisor.Status(name), nil
}

// Uninstall stops a bridge, forgets it, and removes its binary. State the bridge
// produced is kept unless purge is set, so uninstalling is not destructive.
func (h *Hub) Uninstall(name string, purge bool) error {
	if err := validateBridgeName(name); err != nil {
		return err
	}
	var entry BridgeConfig
	removed := false
	if err := h.updateConfig(func(config *Config) {
		existing, ok := config.Bridges[name]
		if !ok {
			return
		}
		entry, removed = existing, true
		delete(config.Bridges, name)
	}); err != nil {
		return err
	}
	if !removed {
		return fmt.Errorf("hub: no bridge named %q is installed", name)
	}

	h.supervisor.remove(name)
	directory := filepath.Join(h.stateDir, "bridges", name)
	if purge {
		return os.RemoveAll(directory)
	}
	// Without purge only the fetched binary goes: anything the bridge left in its
	// directory stays, so uninstalling is not destructive.
	binary := ""
	if len(entry.Command) > 0 {
		binary = filepath.Clean(strings.TrimSpace(entry.Command[0]))
	}
	if !strings.HasPrefix(binary, directory+string(os.PathSeparator)) {
		return nil
	}
	if err := os.Remove(binary); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// validateBridgeName rejects names that would place a bridge's directory outside
// the bridges directory, which matters because Uninstall removes that directory.
func validateBridgeName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) || name != filepath.Base(name) {
		return fmt.Errorf("hub: invalid bridge name %q", name)
	}
	return nil
}

// fetch copies a local path or downloads a URL into destination, verifying the
// sha256 when one is expected.
func fetch(ctx context.Context, source, destination, checksum string) error {
	if strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://") {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
		if err != nil {
			return err
		}
		client := &http.Client{Timeout: 60 * time.Second}
		response, err := client.Do(request)
		if err != nil {
			return fmt.Errorf("hub: download %s: %w", source, err)
		}
		defer response.Body.Close()
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return fmt.Errorf("hub: download %s: HTTP %d", source, response.StatusCode)
		}
		return writeVerified(response.Body, destination, checksum)
	}
	file, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("hub: open %s: %w", source, err)
	}
	defer file.Close()
	return writeVerified(file, destination, checksum)
}

// catalogNames lists available bridge names for an error message.
func catalogNames(entries []CatalogEntry) string {
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name)
	}
	return strings.Join(names, ", ")
}

// mustAvailable is Available for error messages, where a registry failure should
// not replace the error being reported.
func mustAvailable(ctx context.Context, h *Hub) []CatalogEntry {
	entries, err := h.Available(ctx, false)
	if err != nil {
		return nil
	}
	return entries
}

func writeVerified(source io.Reader, destination, checksum string) error {
	temporary, err := os.CreateTemp(filepath.Dir(destination), ".bridge-*")
	if err != nil {
		return err
	}
	name := temporary.Name()
	digest := sha256.New()
	if _, err := io.Copy(io.MultiWriter(temporary, digest), source); err != nil {
		temporary.Close()
		os.Remove(name)
		return err
	}
	if err := temporary.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if expected := strings.TrimSpace(strings.ToLower(checksum)); expected != "" {
		actual := hex.EncodeToString(digest.Sum(nil))
		if actual != expected {
			os.Remove(name)
			return fmt.Errorf("hub: checksum mismatch: got %s, expected %s", actual, expected)
		}
	}
	return os.Rename(name, destination)
}
