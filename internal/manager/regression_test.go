package manager

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCreateRefusesTraversalUserNames: a user name becomes a directory under the
// deployment root, and Remove(purge) removes it, so ".." must never be accepted.
func TestCreateRefusesTraversalUserNames(t *testing.T) {
	manager, _ := testManager(t)
	for _, user := range []string{"..", ".", "a/b", "a b", `a\\b`} {
		if _, err := manager.Create(user, "model", ""); err == nil {
			t.Fatalf("create %q was allowed", user)
		}
	}
	base := manager.Config().BaseDir
	if strings.TrimSpace(base) == "" {
		base = filepath.Join(manager.stateDir, "deployments")
	}
	if _, err := os.Stat(filepath.Join(base, "..", "server.json")); err == nil {
		t.Fatal("a traversal name wrote outside the deployment root")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(base), "server.json")); err == nil {
		t.Fatal("a traversal name wrote beside the deployment root")
	}
	// A plain name still works.
	if _, err := manager.Create("carl", "model", ""); err != nil {
		t.Fatalf("create carl: %v", err)
	}
}
