package server

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// Tuning is what the environment applies to the machine it runs on, and what it
// launches the browser with. Limits are soft: memory.high throttles the
// environment and lets it shed work, where memory.max would OOM-kill the browser
// in the middle of a task.
type Tuning struct {
	// MemoryHigh is the throttle limit for the environment's cgroup, in bytes.
	// Zero leaves the cgroup alone.
	MemoryHigh int64
	// CPUWeight is the environment's cgroup v2 cpu.weight (1–10000). Zero leaves
	// it alone; 100 is the system default.
	CPUWeight int
	// BrowserCPUWeight is the weight for the browser's own cgroup, so a browser
	// cannot starve the agents it is working for.
	BrowserCPUWeight int
	// DevShmBytes is the /dev/shm size below which the browser is started with a
	// disk-backed shared memory fallback, because the 64 MB container default is
	// what makes Chromium crash rather than merely slow down.
	DevShmBytes int64
}

// DefaultTuning is the shape a server deployment wants: a throttle high enough to
// keep a browser alive, an even CPU split, and the shared-memory check on.
func DefaultTuning() Tuning {
	return Tuning{MemoryHigh: 2 << 30, CPUWeight: 200, BrowserCPUWeight: 200, DevShmBytes: 256 << 20}
}

// TuningReport is what applying the tuning actually did, so an operator can see
// whether the environment is constrained or unbounded.
type TuningReport struct {
	CgroupPath string   `json:"cgroupPath,omitempty"`
	Applied    []string `json:"applied,omitempty"`
	Skipped    []string `json:"skipped,omitempty"`
	Notes      []string `json:"notes,omitempty"`
}

// ApplyTuning writes the cgroup limits this environment is allowed to set. It is
// deliberately best-effort: a container without a writable cgroup, or a host
// where the environment does not own its own cgroup, is normal, and the report
// says what was and was not applied rather than failing the boot.
func ApplyTuning(root string, tuning Tuning) (TuningReport, error) {
	report := TuningReport{}
	if strings.TrimSpace(root) == "" {
		root = "/sys/fs/cgroup"
	}
	if _, err := os.Stat(root); err != nil {
		report.Skipped = append(report.Skipped, "no cgroup v2 hierarchy at "+root)
		report.Notes = append(report.Notes, "tuning is only meaningful on Linux with cgroup v2")
		return report, nil
	}
	path := ownCgroupPath(root)
	if path == "" {
		report.Skipped = append(report.Skipped, "the environment's own cgroup could not be identified")
		return report, nil
	}
	report.CgroupPath = path

	if tuning.MemoryHigh > 0 {
		if err := writeCgroup(path, "memory.high", strconv.FormatInt(tuning.MemoryHigh, 10)); err != nil {
			report.Skipped = append(report.Skipped, "memory.high: "+err.Error())
		} else {
			report.Applied = append(report.Applied, "memory.high="+strconv.FormatInt(tuning.MemoryHigh, 10))
		}
	}
	if tuning.CPUWeight > 0 {
		if err := writeCgroup(path, "cpu.weight", strconv.Itoa(tuning.CPUWeight)); err != nil {
			report.Skipped = append(report.Skipped, "cpu.weight: "+err.Error())
		} else {
			report.Applied = append(report.Applied, "cpu.weight="+strconv.Itoa(tuning.CPUWeight))
		}
	}
	if report.Skipped != nil && report.Applied == nil {
		report.Notes = append(report.Notes, "the environment is running unconstrained: set memory.high and cpu.weight with cgroup v2 to bound it")
	}
	return report, nil
}

// BrowserFlags returns the launch flags the environment adds for a browser on a
// server. They are deliberately few: a stealth browser's fingerprint is the point
// of using it, and every extra flag is another difference to explain.
func (t Tuning) BrowserFlags(binary, profileDir string, port int) []string {
	flags := []string{
		"--user-data-dir=" + profileDir,
		fmt.Sprintf("--remote-debugging-port=%d", port),
		// A server has nobody to answer a first-run wizard, and it has no reason to
		// phone home while an agent works.
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-background-networking",
	}
	// /dev/shm inside a container is often 64 MB, which Chromium fills and then
	// fails in confusing ways; the fallback trades memory bandwidth for not
	// crashing.
	if t.DevShmBytes > 0 && devShmBytes() < t.DevShmBytes {
		flags = append(flags, "--disable-dev-shm-usage")
	}
	return flags
}

// devShmBytes reports the size of the shared-memory mount, or zero when it cannot
// be read. It is a variable so a test can stand in a small one.
var devShmBytes = func() int64 {
	var stat syscall.Statfs_t
	if err := syscall.Statfs("/dev/shm", &stat); err != nil {
		return 0
	}
	return int64(stat.Bsize) * int64(stat.Blocks)
}

// writeCgroup writes one cgroup v2 control file, and reports why it could not.
func writeCgroup(path, name, value string) error {
	file := filepath.Join(path, name)
	if _, err := os.Stat(file); err != nil {
		return fmt.Errorf("%s is not available", name)
	}
	if err := os.WriteFile(file, []byte(value), 0o644); err != nil {
		return err
	}
	return nil
}

// ownCgroupPath finds the cgroup this process belongs to, from /proc/self/cgroup.
// It is a variable so a test can stand in a hierarchy: the control files are
// ordinary files, and the only Linux-specific part is finding our own path.
var ownCgroupPath = func(root string) string {
	data, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		parts := strings.SplitN(line, ":", 3)
		if len(parts) != 3 || parts[0] != "0" {
			continue
		}
		if strings.TrimSpace(parts[2]) == "" || parts[2] == "/" {
			// In the root cgroup there is nothing of our own to limit: the whole
			// host's limits are already in force.
			return root
		}
		return filepath.Join(root, strings.TrimPrefix(parts[2], "/"))
	}
	return ""
}

// noCgroupYet reports whether this platform has no cgroup hierarchy to write to,
// which is every non-Linux host and most containers without a writable one.
func noCgroupYet(root string) bool {
	if _, err := os.Stat("/proc/self/cgroup"); err != nil {
		return true
	}
	if _, err := os.Stat(root); err != nil {
		return true
	}
	return false
}
