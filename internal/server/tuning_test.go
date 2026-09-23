package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestApplyTuningWritesCgroupLimits uses a directory standing in for the cgroup
// hierarchy, so the behaviour is testable without being on Linux.
func TestApplyTuningWritesCgroupLimits(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "memory.high"), []byte("max\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "cpu.weight"), []byte("100\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Stand in for /proc/self/cgroup: the hierarchy itself is plain files, so the
	// only Linux-specific piece is finding our own path inside it.
	original := ownCgroupPath
	defer func() { ownCgroupPath = original }()
	ownCgroupPath = func(string) string { return root }

	tuning := Tuning{MemoryHigh: 1 << 30, CPUWeight: 200}
	report, err := ApplyTuning(root, tuning)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Applied) != 2 {
		t.Fatalf("applied = %#v, skipped = %#v", report.Applied, report.Skipped)
	}
	for _, want := range []string{"memory.high=1073741824", "cpu.weight=200"} {
		found := false
		for _, applied := range report.Applied {
			if applied == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("applied = %#v, want %q", report.Applied, want)
		}
	}
	// The values really landed in the files.
	for file, want := range map[string]string{"memory.high": "1073741824", "cpu.weight": "200"} {
		data, err := os.ReadFile(filepath.Join(root, file))
		if err != nil || strings.TrimSpace(string(data)) != want {
			t.Fatalf("%s = %q, %v", file, data, err)
		}
	}

	// A cgroup without the control files is normal, and the report says what it
	// could not do rather than failing the boot.
	empty := t.TempDir()
	ownCgroupPath = func(string) string { return empty }
	report, err = ApplyTuning(empty, tuning)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Skipped) != 2 || report.Notes == nil {
		t.Fatalf("report = %#v", report)
	}
	// And a platform with no cgroup at all is reported, not treated as a failure.
	ownCgroupPath = original
	report, err = ApplyTuning(filepath.Join(empty, "missing"), tuning)
	if err != nil || len(report.Skipped) == 0 {
		t.Fatalf("no hierarchy = %#v, %v", report, err)
	}
	if noCgroupYet(empty) != true && devShmBytes() > 0 {
		// On Linux with a writable hierarchy this branch is skipped; either way the
		// function must answer without panicking.
		_ = report
	}
}

func TestBrowserFlagsAreFewAndServerShaped(t *testing.T) {
	tuning := DefaultTuning()
	flags := tuning.BrowserFlags("/usr/local/bin/cloakbrowser", "/var/lib/midas/browser", 9222)
	joined := strings.Join(flags, " ")
	for _, want := range []string{
		"--user-data-dir=/var/lib/midas/browser",
		"--remote-debugging-port=9222",
		"--no-first-run",
		"--disable-background-networking",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("flags = %v, missing %q", flags, want)
		}
	}
	// A stealth browser's fingerprint is the point of using it, so the flag list
	// stays short: nothing here changes rendering or behaviour beyond the server's
	// own needs.
	if len(flags) > 6 {
		t.Fatalf("flags grew past the lean set: %v", flags)
	}
	// The shared-memory fallback appears only when the mount is small.
	original := devShmBytes
	defer func() { devShmBytes = original }()
	devShmBytes = func() int64 { return 64 << 20 }
	if small := tuning.BrowserFlags("b", "p", 1); !strings.Contains(strings.Join(small, " "), "--disable-dev-shm-usage") {
		t.Fatalf("a small /dev/shm did not add the fallback: %v", small)
	}
	devShmBytes = func() int64 { return 1 << 30 }
	if large := tuning.BrowserFlags("b", "p", 1); strings.Contains(strings.Join(large, " "), "--disable-dev-shm-usage") {
		t.Fatalf("a large /dev/shm added the fallback anyway: %v", large)
	}
}
