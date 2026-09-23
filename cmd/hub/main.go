// Command hub is the MCP server agents connect to for messaging. It is a separate
// program on purpose: nothing in an agent depends on it, and it does nothing
// until a client installs a bridge.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/CarlvinceTan/midas/pkg/hub"
)

const usage = `hub - local agent homeserver over MCP

Usage:
  hub                     serve MCP on stdin and stdout
  hub --listen ADDR       serve MCP over HTTP for several agents
  hub print-config        print the mcp.json entry for this hub
  hub version             print the version

Configuration lives in hub.json beside your agent config (MIDAS_CONFIG_DIR, or
~/.midas). Nothing is attached by default: install a bridge with the hub_bridges
tool, then it starts on first use.
`

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr, os.Getenv); err != nil {
		fmt.Fprintln(os.Stderr, "hub:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr *os.File, getenv func(string) string) error {
	if len(args) > 0 {
		switch args[0] {
		case "print-config":
			return printConfig(stdout, getenv)
		case "version":
			fmt.Fprintln(stdout, "0.1.0")
			return nil
		case "help", "--help", "-h":
			fmt.Fprint(stdout, usage)
			return nil
		}
	}
	listen := ""
	flags := flag.NewFlagSet("hub", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&listen, "listen", "", "HTTP address to serve MCP on (default: stdio)")
	flags.Usage = func() { fmt.Fprint(stderr, usage) }
	if err := flags.Parse(args); err != nil {
		return err
	}
	if listen == "" {
		listen = getenv("HUB_LISTEN")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	instance, err := hub.Open(getenv)
	if err != nil {
		return err
	}
	defer instance.Close()

	// The hub runs no timers of its own: this is the only periodic work, and it
	// exists to stop bridges that went idle, not to poll them.
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				instance.Reap()
			}
		}
	}()

	if strings.TrimSpace(listen) == "" {
		return instance.ServeStdio(ctx)
	}
	token, err := ensureToken(instance, getenv)
	if err != nil {
		return err
	}
	handler := instance.MCPHandler()
	mux := http.NewServeMux()
	mux.Handle("/mcp", authorize(token, handler))
	mux.Handle("/healthz", authorize(token, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte("ok\n"))
	})))
	server := &http.Server{Addr: listen, Handler: mux, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}
	fmt.Fprintf(stderr, "hub %s listening on http://%s/mcp\n", "0.1.0", listen)
	done := make(chan error, 1)
	go func() { done <- server.ListenAndServe() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return server.Shutdown(shutdown)
	}
}

// ensureToken keeps a bearer token in the hub's configuration, generating one on
// first listen.
func ensureToken(instance *hub.Hub, getenv func(string) string) (string, error) {
	config := instance.Config()
	if strings.TrimSpace(config.Token) != "" {
		return config.Token, nil
	}
	random := make([]byte, 24)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	config.Token = hex.EncodeToString(random)
	if err := instance.SaveConfig(config); err != nil {
		return "", err
	}
	_ = getenv
	return config.Token, nil
}

// authorize requires the hub's bearer token, so a local HTTP endpoint is not an
// open door on a shared machine.
func authorize(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		provided := strings.TrimSpace(request.Header.Get("Authorization"))
		if provided != "Bearer "+token {
			writer.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(writer, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(writer, request)
	})
}

// printConfig prints the mcp.json entry that connects an agent to this hub.
func printConfig(stdout *os.File, getenv func(string) string) error {
	config, path, err := hub.LoadConfig(getenv)
	if err != nil {
		return err
	}
	entry := map[string]any{"mcpServers": map[string]any{}}
	servers := entry["mcpServers"].(map[string]any)
	if listen := strings.TrimSpace(os.Getenv("HUB_LISTEN")); listen != "" {
		headers := map[string]string{}
		if token := strings.TrimSpace(config.Token); token != "" {
			headers["Authorization"] = "Bearer " + token
		}
		servers["hub"] = map[string]any{"url": "http://" + listen + "/mcp", "headers": headers}
	} else {
		servers["hub"] = map[string]any{"command": []string{"hub"}}
	}
	encoded, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "%s\n\n# written to %s\n", encoded, path)
	return nil
}
