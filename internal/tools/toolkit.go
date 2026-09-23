// Package tools provides the small, workspace-scoped coding toolset used by
// Midas agents. It intentionally contains only file reading/writing/editing
// and shell execution; higher-level task and goal tools live with those
// packages.
package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/CarlvinceTan/midas/pkg/agent"
)

const (
	maxOutputLines = 2000
	maxOutputBytes = 50 * 1024
	// maxTextReadBytes bounds how much of a text file read holds in memory. The
	// tool only shows 50KB, so this is generous for the files an agent reads and
	// still refuses a multi-gigabyte log with advice instead of an OOM.
	maxTextReadBytes = 64 << 20
	// maxImageReadBytes bounds an image, which is carried whole for base64.
	maxImageReadBytes = 16 << 20
)

// Toolkit owns the root and mutation locks shared by its tools.
type Toolkit struct {
	root  string
	locks sync.Map
}

// New constructs a coding toolkit rooted at root. File tools reject paths
// outside this root, including paths that escape through symlinks.
func New(root string) (*Toolkit, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("tools: resolve workspace root: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("tools: resolve workspace root: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return nil, fmt.Errorf("tools: inspect workspace root: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("tools: workspace root is not a directory: %s", root)
	}
	return &Toolkit{root: filepath.Clean(resolved)}, nil
}

// Root returns the canonical workspace root.
func (t *Toolkit) Root() string { return t.root }

// All returns the deliberately minimal built-in coding toolset.
func (t *Toolkit) All() []agent.Tool {
	return []agent.Tool{
		readTool{kit: t},
		writeTool{kit: t},
		editTool{kit: t},
		bashTool{kit: t},
	}
}

// scratchName looks like a throwaway artifact rather than project content:
// probe scripts, captures, and temp files. Midas keeps those in the system temp
// directory so the working tree only ever holds real changes.
func scratchName(name string) bool {
	base := strings.ToLower(strings.TrimSpace(filepath.Base(name)))
	if base == "" || base == "." || base == ".." {
		return false
	}
	switch filepath.Ext(base) {
	case ".tmp", ".temp":
		return true
	}
	for _, prefix := range []string{"tmp", "temp", "scratch", "probe", "throwaway"} {
		if !strings.HasPrefix(base, prefix) {
			continue
		}
		// The prefix has to end the name or be followed by a separator, so
		// template.go and prober.go are ordinary project files.
		rest := base[len(prefix):]
		if rest == "" || !isAlphaNumeric(rest[0]) {
			return true
		}
	}
	return false
}

func isAlphaNumeric(value byte) bool {
	return (value >= 'a' && value <= 'z') || (value >= 'A' && value <= 'Z') || (value >= '0' && value <= '9')

}

// scratchDir is where temporary and generated files belong.
func scratchDir() string {
	if value := strings.TrimSpace(os.Getenv("TMPDIR")); value != "" {
		return value
	}
	return os.TempDir()
}

// ReadOnly returns the inspection-only subset used by advisory profiles.
func (t *Toolkit) ReadOnly() []agent.Tool {
	return []agent.Tool{
		readTool{kit: t},
		listTool{kit: t},
		grepTool{kit: t},
	}
}

func (t *Toolkit) mutationLock(path string) *sync.Mutex {
	value, _ := t.locks.LoadOrStore(path, new(sync.Mutex))
	return value.(*sync.Mutex)
}

func (t *Toolkit) resolveExisting(name string) (string, error) {
	candidate, err := t.lexicalPath(name)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", err
	}
	if !within(t.root, resolved) {
		return "", fmt.Errorf("path escapes workspace: %s", name)
	}
	return resolved, nil
}

func (t *Toolkit) resolveWritable(name string) (string, error) {
	candidate, err := t.lexicalPath(name)
	if err != nil {
		return "", err
	}

	ancestor := candidate
	for {
		if _, statErr := os.Lstat(ancestor); statErr == nil {
			break
		} else if !os.IsNotExist(statErr) {
			return "", statErr
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return "", fmt.Errorf("no existing ancestor for path: %s", name)
		}
		ancestor = parent
	}
	resolvedAncestor, err := filepath.EvalSymlinks(ancestor)
	if err != nil {
		return "", err
	}
	if !within(t.root, resolvedAncestor) {
		return "", fmt.Errorf("path escapes workspace: %s", name)
	}
	suffix, err := filepath.Rel(ancestor, candidate)
	if err != nil {
		return "", err
	}
	resolved := filepath.Join(resolvedAncestor, suffix)
	if !within(t.root, resolved) {
		return "", fmt.Errorf("path escapes workspace: %s", name)
	}
	return filepath.Clean(resolved), nil
}

func (t *Toolkit) lexicalPath(name string) (string, error) {
	if strings.TrimSpace(name) == "" {
		return "", fmt.Errorf("path must not be empty")
	}
	candidate := name
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(t.root, candidate)
	}
	candidate = filepath.Clean(candidate)
	if !within(t.root, candidate) {
		return "", fmt.Errorf("path escapes workspace: %s", name)
	}
	return candidate, nil
}

func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
