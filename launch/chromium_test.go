package launch

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCleanupLocalBrowserRemovesTempDir(t *testing.T) {
	cmd := startHelperProcess(t)
	tempDir := t.TempDir()
	profileDir := filepath.Join(tempDir, "profile")
	if err := os.MkdirAll(profileDir, 0o755); err != nil {
		t.Fatal(err)
	}

	chrome := &LaunchedChrome{
		Cmd:                 cmd,
		UserDataDir:         profileDir,
		CreatedTempProfile:  true,
		PreserveUserDataDir: false,
	}

	if err := cleanupLocalBrowser(chrome); err != nil {
		t.Fatalf("cleanupLocalBrowser returned error: %v", err)
	}

	if _, err := os.Stat(profileDir); !os.IsNotExist(err) {
		t.Fatalf("expected profile dir to be removed, got err=%v", err)
	}
}

func TestCleanupLocalBrowserPreservesCallerDir(t *testing.T) {
	cmd := startHelperProcess(t)
	tempDir := t.TempDir()
	profileDir := filepath.Join(tempDir, "profile")
	if err := os.MkdirAll(profileDir, 0o755); err != nil {
		t.Fatal(err)
	}

	chrome := &LaunchedChrome{
		Cmd:                 cmd,
		UserDataDir:         profileDir,
		CreatedTempProfile:  false,
		PreserveUserDataDir: true,
	}

	if err := cleanupLocalBrowser(chrome); err != nil {
		t.Fatalf("cleanupLocalBrowser returned error: %v", err)
	}

	if _, err := os.Stat(profileDir); err != nil {
		t.Fatalf("expected profile dir to remain, got err=%v", err)
	}
}

func TestShutdownSupervisorCleansUpOnLifelineClose(t *testing.T) {
	cmd := startHelperProcess(t)
	tempDir := t.TempDir()
	profileDir := filepath.Join(tempDir, "profile")
	if err := os.MkdirAll(profileDir, 0o755); err != nil {
		t.Fatal(err)
	}

	chrome := &LaunchedChrome{
		Cmd:                 cmd,
		UserDataDir:         profileDir,
		CreatedTempProfile:  true,
		PreserveUserDataDir: false,
	}

	supervisor, err := startShutdownSupervisor(chrome)
	if err != nil {
		t.Fatalf("startShutdownSupervisor returned error: %v", err)
	}
	if supervisor == nil {
		t.Fatal("expected supervisor to be started")
	}
	defer func() {
		_ = supervisor.Stop()
	}()

	if err := supervisor.lifeline.Close(); err != nil {
		t.Fatalf("closing lifeline returned error: %v", err)
	}
	supervisor.lifeline = nil

	waitForCommandExit(t, cmd, 10*time.Second)
	waitForCondition(t, 10*time.Second, func() bool {
		_, err := os.Stat(profileDir)
		return os.IsNotExist(err)
	})

	_ = supervisor.cmd.Wait()
}
