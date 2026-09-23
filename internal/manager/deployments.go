// Package manager runs several Midas server deployments from one place, one
// deployment per user. Each deployment is a midas-server process with its own
// configuration directory, state directory, browser profile and port, so one
// user's agents, secrets, inboxes and browser never touch another's.
package manager

import (
	"cmp"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"
)

// ConfigFile is the manager's own file, beside the agent's configuration.
const ConfigFile = "manager.json"

// Deployment is one user's server.
type Deployment struct {
	// User is the owner, and the key: one user has at most one deployment.
	User string `json:"user"`
	// Port is the deployment's HTTP port.
	Port int `json:"port"`
	// Dir is the deployment's own directory, holding its config, state and browser
	// profile.
	Dir string `json:"dir"`
	// ServerBinary is the midas-server executable to run.
	ServerBinary string `json:"serverBinary,omitempty"`
	// Model is the default model for this deployment's agents.
	Model string `json:"model,omitempty"`
	// Token authorises its API. It is generated with the deployment and never
	// shared between users.
	Token string `json:"token,omitempty"`
	// PID is the running process, zero when stopped.
	PID int `json:"pid,omitempty"`
	// Restart requests a restart when the process exits unexpectedly.
	Restart bool  `json:"restart,omitempty"`
	At      int64 `json:"at,omitempty"`

	started time.Time
}

// Config is the manager's whole state.
type Config struct {
	// BaseDir holds every deployment directory.
	BaseDir string `json:"baseDir,omitempty"`
	// BasePort is where port allocation starts.
	BasePort int `json:"basePort,omitempty"`
	// ServerBinary is the default server executable.
	ServerBinary string `json:"serverBinary,omitempty"`
	// Deployments are the users' servers, keyed by user.
	Deployments map[string]Deployment `json:"deployments,omitempty"`
}

// DefaultBasePort is the first port handed out; each deployment takes the next
// free one after it.
const DefaultBasePort = 8900

// Path returns the manager's config file inside a config directory.
func Path(configDir string) string {
	if strings.TrimSpace(configDir) == "" {
		configDir = "."
	}
	return filepath.Join(configDir, ConfigFile)
}

// Load reads the manager's state, returning the default when no file exists.
func Load(configDir string) (Config, error) {
	path := Path(configDir)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return Config{BasePort: DefaultBasePort, Deployments: map[string]Deployment{}}, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("manager: read %s: %w", path, err)
	}
	config := Config{}
	if err := json.Unmarshal(data, &config); err != nil {
		return Config{}, fmt.Errorf("manager: parse %s: %w", path, err)
	}
	if config.BasePort == 0 {
		config.BasePort = DefaultBasePort
	}
	if config.Deployments == nil {
		config.Deployments = map[string]Deployment{}
	}
	return config, nil
}

// Save writes the manager's state with owner-only permissions, since it holds
// every deployment's token.
func Save(configDir string, config Config) error {
	path := Path(configDir)
	encoded, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".manager-*.json")
	if err != nil {
		return err
	}
	name := temporary.Name()
	if _, err := temporary.Write(append(encoded, '\n')); err != nil {
		temporary.Close()
		os.Remove(name)
		return err
	}
	if err := temporary.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Chmod(name, 0o600); err != nil {
		os.Remove(name)
		return err
	}
	return os.Rename(name, path)
}

// Manager owns the deployments and their processes.
type Manager struct {
	configDir string
	config    Config
	stateDir  string
	now       func() time.Time
	// Run starts a process; injectable so tests do not spawn anything.
	Run func(directory string, environment []string, binary string, arguments []string) (int, error)
	// Signal stops a process; injectable with Run.
	Signal func(pid int, signal syscall.Signal) error
	// Running reports whether a deployment's process is up. It is a field so a
	// test can stand in for a process it did not spawn.
	Running func(deployment Deployment) bool
}

// Options configure a manager.
type Options struct {
	ConfigDir string
	// StateDir is where deployment directories live by default.
	StateDir string
	// ServerBinary locates midas-server when a deployment does not name one.
	ServerBinary func() (string, error)
}

// NewManager loads the manager's state and prepares it to act.
func NewManager(options Options) (*Manager, error) {
	config, err := Load(options.ConfigDir)
	if err != nil {
		return nil, err
	}
	return &Manager{
		configDir: options.ConfigDir, config: config, stateDir: options.StateDir,
		now: time.Now, Run: startProcess, Signal: signalProcess, Running: processRunning,
	}, nil
}

// Config returns the manager's state, for a CLI to render.
func (m *Manager) Config() Config { return m.config }

// Create provisions one user's deployment: its directory, its configuration file
// with a generated token, its browser profile, and its port. It starts nothing.
func (m *Manager) Create(user, model, serverBinary string) (Deployment, error) {
	user = strings.TrimSpace(user)
	if user == "" {
		return Deployment{}, errors.New("manager: a user is required")
	}
	// A user name is one directory name: separators and spaces are refused, and
	// "." and ".." are refused explicitly because IsLocal treats "." as local, and
	// either would name the deployment root itself.
	if user == "." || user == ".." || !filepath.IsLocal(user) || strings.ContainsAny(user, "/\\ ") {
		return Deployment{}, errors.New("manager: a user name must be a plain directory name")
	}
	if _, exists := m.config.Deployments[user]; exists {
		return Deployment{}, fmt.Errorf("manager: %s already has a deployment", user)
	}
	port, err := m.freePort()
	if err != nil {
		return Deployment{}, err
	}
	base := m.config.BaseDir
	if strings.TrimSpace(base) == "" {
		base = filepath.Join(m.stateDir, "deployments")
	}
	directory := filepath.Join(base, user)
	for _, sub := range []string{"", "state", "browser"} {
		if err := os.MkdirAll(filepath.Join(directory, sub), 0o700); err != nil {
			return Deployment{}, err
		}
	}
	token, err := randomToken()
	if err != nil {
		return Deployment{}, err
	}
	deployment := Deployment{User: user, Port: port, Dir: directory, ServerBinary: serverBinary, Model: model, Token: token, At: m.now().UnixMilli()}
	if err := m.writeServerConfig(deployment); err != nil {
		return Deployment{}, err
	}
	m.config.Deployments[user] = deployment
	if err := m.persist(); err != nil {
		return Deployment{}, err
	}
	return deployment, nil
}

// writeServerConfig writes the deployment's own server.json: the token, the
// model, the browser profile, and two agents. This is what the deployment's
// process reads on boot, so provisioning and starting stay separate steps.
func (m *Manager) writeServerConfig(deployment Deployment) error {
	config := map[string]any{
		"token": deployment.Token,
		"model": deployment.Model,
		"browser": map[string]any{
			"profileDir": filepath.Join(deployment.Dir, "browser"),
		},
		"agents": []map[string]any{
			{"address": "orchestrator", "role": "orchestrator"},
			{"address": "worker", "role": "worker"},
		},
	}
	encoded, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(deployment.Dir, "server.json"), append(encoded, '\n'), 0o600)
}

// Start runs a deployment's server process, detached, with its logs in its own
// directory.
func (m *Manager) Start(user string) (Deployment, error) {
	deployment, ok := m.config.Deployments[strings.TrimSpace(user)]
	if !ok {
		return Deployment{}, fmt.Errorf("manager: %s has no deployment", user)
	}
	if deployment.PID != 0 && m.Running(deployment) {
		return deployment, nil
	}
	binary := deployment.ServerBinary
	if strings.TrimSpace(binary) == "" {
		binary = m.config.ServerBinary
	}
	if strings.TrimSpace(binary) == "" {
		return Deployment{}, errors.New("manager: no server binary configured; pass one when creating the deployment or set serverBinary")
	}
	logPath := filepath.Join(deployment.Dir, "server.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return Deployment{}, err
	}
	defer logFile.Close()
	pid, err := m.Run(deployment.Dir, os.Environ(), binary, []string{
		"--config-dir", deployment.Dir,
		"--listen", fmt.Sprintf("127.0.0.1:%d", deployment.Port),
	})
	if err != nil {
		return Deployment{}, err
	}
	deployment.PID = pid
	deployment.Restart = true
	deployment.started = m.now()
	m.config.Deployments[user] = deployment
	if err := m.persist(); err != nil {
		return Deployment{}, err
	}
	return deployment, nil
}

// Stop asks a deployment's process to shut down.
func (m *Manager) Stop(user string) (Deployment, error) {
	deployment, ok := m.config.Deployments[strings.TrimSpace(user)]
	if !ok {
		return Deployment{}, fmt.Errorf("manager: %s has no deployment", user)
	}
	if deployment.PID != 0 {
		// Clear the restart flag first, so a graceful stop is not undone by the
		// supervisor.
		deployment.Restart = false
		// A crashed process is already gone: ESRCH means the same thing as
		// ErrProcessDone, and treating it as an error would leave a dead
		// deployment marked as running forever.
		if err := m.Signal(deployment.PID, syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) && !errors.Is(err, syscall.ESRCH) {
			return deployment, err
		}
	}
	deployment.PID = 0
	m.config.Deployments[user] = deployment
	if err := m.persist(); err != nil {
		return deployment, err
	}
	return deployment, nil
}

// Remove stops a deployment and forgets it. The directory is only deleted when
// purge is set: removing a user from the manager should not silently destroy
// their agents' state.
func (m *Manager) Remove(user string, purge bool) error {
	name := strings.TrimSpace(user)
	deployment, ok := m.config.Deployments[name]
	if !ok {
		return fmt.Errorf("manager: %s has no deployment", user)
	}
	if _, err := m.Stop(name); err != nil {
		return err
	}
	delete(m.config.Deployments, name)
	if err := m.persist(); err != nil {
		return err
	}
	if purge {
		return os.RemoveAll(deployment.Dir)
	}
	return nil
}

// processRunning reports whether a pid is still alive, without touching it.
func processRunning(deployment Deployment) bool {
	if deployment.PID == 0 {
		return false
	}
	process, err := os.FindProcess(deployment.PID)
	if err != nil {
		return false
	}
	return process.Signal(syscall.Signal(0)) == nil
}

// Listing is a deployment plus what the manager can see about it right now.
type Listing struct {
	Deployment
	Running bool   `json:"running"`
	URL     string `json:"url,omitempty"`
}

// List reports every deployment, ordered by user.
func (m *Manager) List() []Listing {
	listings := make([]Listing, 0, len(m.config.Deployments))
	for _, deployment := range m.config.Deployments {
		listings = append(listings, Listing{
			Deployment: deployment, Running: m.Running(deployment),
			URL: fmt.Sprintf("http://127.0.0.1:%d", deployment.Port),
		})
	}
	slices.SortStableFunc(listings, func(a, b Listing) int { return cmp.Compare(a.User, b.User) })
	return listings
}

// freePort finds the first free port at or above the configured base.
func (m *Manager) freePort() (int, error) {
	used := map[int]bool{}
	for _, deployment := range m.config.Deployments {
		used[deployment.Port] = true
	}
	base := m.config.BasePort
	if base == 0 {
		base = DefaultBasePort
	}
	for port := base; port < base+1000; port++ {
		if used[port] {
			continue
		}
		listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			continue
		}
		_ = listener.Close()
		return port, nil
	}
	return 0, errors.New("manager: no free port in range")
}

func (m *Manager) persist() error { return Save(m.configDir, m.config) }

func randomToken() (string, error) {
	random := make([]byte, 24)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return hex.EncodeToString(random), nil
}

// startProcess starts a detached process and returns its pid.
func startProcess(directory string, environment []string, binary string, arguments []string) (int, error) {
	command := exec.Command(binary, arguments...)
	command.Dir = directory
	command.Env = environment
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	logPath := filepath.Join(directory, "server.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, err
	}
	defer logFile.Close()
	command.Stdout, command.Stderr = logFile, logFile
	if err := command.Start(); err != nil {
		return 0, err
	}
	pid := command.Process.Pid
	go func() { _ = command.Wait() }()
	return pid, nil
}

// signalProcess sends a signal to a process.
func signalProcess(pid int, signal syscall.Signal) error {
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return process.Signal(signal)
}
