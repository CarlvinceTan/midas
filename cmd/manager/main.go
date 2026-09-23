// Command manager runs several Midas server deployments from one place, one
// deployment per user. Each user's agents, secrets, inboxes and browser profile
// live in their own directory with their own port and token, so provisioning a
// user is one command and they never share anything with another.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/CarlvinceTan/midas/internal/manager"
	"github.com/CarlvinceTan/midas/internal/storage"
)

const usage = `manager - run several Midas servers, one deployment per user

Usage:
  manager create USER [--model P/M] [--server PATH]
  manager list
  manager start USER
  manager stop USER
  manager remove USER [--purge]
  manager print-config USER

Each deployment gets its own directory, port, token, browser profile and agent
roster under the manager's state directory; its configuration is beside it in
server.json. Nothing about one user is visible to another.
`

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr, os.Getenv); err != nil {
		fmt.Fprintln(os.Stderr, "manager:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr *os.File, getenv func(string) string) error {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return nil
	}
	switch args[0] {
	case "help", "--help", "-h":
		fmt.Fprint(stdout, usage)
		return nil
	}

	configDir := strings.TrimSpace(getenv("MIDAS_CONFIG_DIR"))
	if configDir == "" {
		configDir = storage.ConfigDir()
	}
	instance, err := manager.NewManager(manager.Options{
		ConfigDir: configDir,
		StateDir:  filepath.Join(configDir, "manager"),
		ServerBinary: func() (string, error) {
			return exec.LookPath("midas-server")
		},
	})
	if err != nil {
		return err
	}

	switch args[0] {
	case "create":
		options, err := parseFlags(args[1:])
		if err != nil {
			return err
		}
		if options.user == "" {
			return fmt.Errorf("create needs a user name")
		}
		serverBinary := options.server
		if serverBinary == "" {
			serverBinary, err = exec.LookPath("midas-server")
			if err != nil {
				return fmt.Errorf("midas-server is not on PATH; pass --server")
			}
		}
		deployment, err := instance.Create(options.user, options.model, serverBinary)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "created %s\n  directory %s\n  port      %d\n  token     %s\n", deployment.User, deployment.Dir, deployment.Port, deployment.Token)
		fmt.Fprintf(stderr, "start it with: manager start %s\n", deployment.User)
		return nil
	case "list", "status":
		return printList(stdout, instance)
	case "start":
		user, err := singleUser(args[1:])
		if err != nil {
			return err
		}
		deployment, err := instance.Start(user)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "started %s on http://127.0.0.1:%d (pid %d)\n", deployment.User, deployment.Port, deployment.PID)
		return nil
	case "stop":
		user, err := singleUser(args[1:])
		if err != nil {
			return err
		}
		deployment, err := instance.Stop(user)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "stopped %s\n", deployment.User)
		return nil
	case "remove":
		options, err := parseFlags(args[1:])
		if err != nil {
			return err
		}
		if options.user == "" {
			return fmt.Errorf("remove needs a user name")
		}
		if err := instance.Remove(options.user, options.purge); err != nil {
			return err
		}
		if options.purge {
			fmt.Fprintf(stdout, "removed %s and deleted its directory\n", options.user)
			return nil
		}
		fmt.Fprintf(stdout, "removed %s (its directory is kept; pass --purge to delete it)\n", options.user)
		return nil
	case "print-config":
		user, err := singleUser(args[1:])
		if err != nil {
			return err
		}
		deployment, ok := instance.Config().Deployments[user]
		if !ok {
			return fmt.Errorf("%s has no deployment", user)
		}
		entry := map[string]any{"mcpServers": map[string]any{}}
		servers := entry["mcpServers"].(map[string]any)
		servers["midas-server"] = map[string]any{
			"url":     fmt.Sprintf("http://127.0.0.1:%d/v1/socket?token=%s", deployment.Port, deployment.Token),
			"comment": "the environment's user transport: events and sends over one socket",
		}
		encoded, err := json.MarshalIndent(entry, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "%s\n", encoded)
		return nil
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

type flags struct {
	user   string
	model  string
	server string
	purge  bool
}

func parseFlags(args []string) (flags, error) {
	parsed := flags{}
	for index := 0; index < len(args); index++ {
		switch args[index] {
		case "--model", "-m":
			if index+1 >= len(args) {
				return parsed, fmt.Errorf("--model needs a value")
			}
			parsed.model = args[index+1]
			index++
		case "--server", "-s":
			if index+1 >= len(args) {
				return parsed, fmt.Errorf("--server needs a path")
			}
			parsed.server = args[index+1]
			index++
		case "--purge":
			parsed.purge = true
		default:
			if strings.HasPrefix(args[index], "-") {
				return parsed, fmt.Errorf("unknown flag %q", args[index])
			}
			if parsed.user != "" {
				return parsed, fmt.Errorf("unexpected argument %q", args[index])
			}
			parsed.user = args[index]
		}
	}
	return parsed, nil
}

func singleUser(args []string) (string, error) {
	if len(args) != 1 || strings.HasPrefix(args[0], "-") {
		return "", fmt.Errorf("expected exactly one user name")
	}
	return args[0], nil
}

func printList(stdout *os.File, instance *manager.Manager) error {
	writer := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(writer, "USER\tPORT\tSTATE\tDIRECTORY")
	for _, listing := range instance.List() {
		state := "stopped"
		if listing.Running {
			state = "running"
		}
		fmt.Fprintf(writer, "%s\t%d\t%s\t%s\n", listing.User, listing.Port, state, listing.Dir)
	}
	return writer.Flush()
}
