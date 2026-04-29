package launch

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

const (
	supervisorModeEnv   = "MIDAS_BROWSER_SHUTDOWN_SUPERVISOR"
	supervisorConfigEnv = "MIDAS_BROWSER_SHUTDOWN_CONFIG"
)

type supervisorConfig struct {
	ChromePID           int    `json:"chromePid"`
	UserDataDir         string `json:"userDataDir"`
	CreatedTempProfile  bool   `json:"createdTempProfile"`
	PreserveUserDataDir bool   `json:"preserveUserDataDir"`
}

type shutdownSupervisor struct {
	cmd      *exec.Cmd
	lifeline *os.File
	stopOnce sync.Once
}

func init() {
	if os.Getenv(supervisorModeEnv) != "1" {
		return
	}

	runShutdownSupervisorFromEnv()
	os.Exit(0)
}

func startShutdownSupervisor(chrome *LaunchedChrome) (*shutdownSupervisor, error) {
	if chrome == nil || chrome.Cmd == nil || chrome.Cmd.Process == nil {
		return nil, nil
	}

	cfg := supervisorConfig{
		ChromePID:           chrome.Cmd.Process.Pid,
		UserDataDir:         chrome.UserDataDir,
		CreatedTempProfile:  chrome.CreatedTempProfile,
		PreserveUserDataDir: chrome.PreserveUserDataDir,
	}
	payload, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}

	readPipe, writePipe, err := os.Pipe()
	if err != nil {
		return nil, err
	}

	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(),
		supervisorModeEnv+"=1",
		supervisorConfigEnv+"="+string(payload),
	)
	cmd.Stdin = readPipe
	cmd.Stdout = nil
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := cmd.Start(); err != nil {
		_ = readPipe.Close()
		_ = writePipe.Close()
		return nil, err
	}
	_ = readPipe.Close()

	return &shutdownSupervisor{
		cmd:      cmd,
		lifeline: writePipe,
	}, nil
}

func (s *shutdownSupervisor) Stop() error {
	if s == nil {
		return nil
	}

	var result error
	s.stopOnce.Do(func() {
		if s.cmd != nil && s.cmd.Process != nil {
			if err := s.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
				result = err
			}
			_, _ = s.cmd.Process.Wait()
		}
		if s.lifeline != nil {
			_ = s.lifeline.Close()
		}
	})
	return result
}

func runShutdownSupervisorFromEnv() {
	raw := os.Getenv(supervisorConfigEnv)
	if raw == "" {
		return
	}

	var cfg supervisorConfig
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return
	}

	lifelineDone := make(chan struct{})
	go func() {
		buf := make([]byte, 1)
		_, _ = os.Stdin.Read(buf)
		close(lifelineDone)
	}()

	processGone := make(chan struct{}, 1)
	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()

		for range ticker.C {
			if !processAlive(cfg.ChromePID) {
				processGone <- struct{}{}
				return
			}
		}
	}()

	chromeGone := false
	for {
		select {
		case <-processGone:
			chromeGone = true
		case <-lifelineDone:
			if !chromeGone && cfg.ChromePID > 0 {
				_ = politeKillPID(cfg.ChromePID, 7*time.Second)
			}
			if cfg.CreatedTempProfile && !cfg.PreserveUserDataDir && cfg.UserDataDir != "" {
				_ = os.RemoveAll(cfg.UserDataDir)
			}
			return
		}
	}
}

func politeKillPID(pid int, timeout time.Duration) error {
	if pid <= 0 || !processAlive(pid) {
		return nil
	}

	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	if err := process.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}

	if err := process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	return nil
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}

	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}

	err = process.Signal(syscall.Signal(0))
	return err == nil
}
