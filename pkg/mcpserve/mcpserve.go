// Package mcpserve is the shared entry point for Midas's MCP servers: stdio for a
// single client, an authenticated HTTP listener for several agents, and the
// print-config snippet that connects a harness to either.
//
// A server started on its own keeps its own configuration file beside the
// agent's config, which is what other agents use. When Midas launches it, Midas
// points MIDAS_MCP_SETTINGS at its settings.json instead, so everything the user
// configured for Midas lives in one file.
package mcpserve

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/CarlvinceTan/midas/pkg/mcpconfig"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Options describe one server.
type Options struct {
	// Name is the command and config prefix, e.g. "control".
	Name string
	// Version is reported by the version subcommand and in the MCP handshake.
	Version string
	// Usage is printed for help.
	Usage string
	// Server is the MCP server this command serves.
	Server *mcp.Server
}

// Run parses arguments and serves until the context ends. It owns the parts every
// server needs: the listener, the bearer token, and print-config.
func Run(args []string, stdout, stderr *os.File, getenv func(string) string, options Options) error {
	if len(args) > 0 {
		switch args[0] {
		case "print-config":
			return printConfig(stdout, getenv, options)
		case "version":
			fmt.Fprintln(stdout, options.Version)
			return nil
		case "help", "--help", "-h":
			fmt.Fprint(stdout, options.Usage)
			return nil
		}
	}
	listen := ""
	for index, argument := range args {
		switch {
		case argument == "--listen" && index+1 < len(args):
			listen = args[index+1]
		case strings.HasPrefix(argument, "--listen="):
			listen = strings.TrimPrefix(argument, "--listen=")
		}
	}
	if listen == "" {
		listen = getenv(strings.ToUpper(options.Name) + "_LISTEN")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if strings.TrimSpace(listen) == "" {
		return options.Server.Run(ctx, &mcp.StdioTransport{})
	}

	token, err := ensureToken(getenv, options)
	if err != nil {
		return err
	}
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return options.Server }, nil)
	mux := http.NewServeMux()
	mux.Handle("/mcp", authorize(token, handler))
	mux.Handle("/healthz", authorize(token, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte("ok\n"))
	})))
	httpServer := &http.Server{Addr: listen, Handler: mux, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}
	fmt.Fprintf(stderr, "%s %s listening on http://%s/mcp\n", options.Name, options.Version, listen)
	done := make(chan error, 1)
	go func() { done <- httpServer.ListenAndServe() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return httpServer.Shutdown(shutdown)
	}
}

// ConfigPath is where a server keeps its own settings. A server Midas launched
// reads the agent's settings file instead, because that is where the user put
// everything; one started any other way keeps its own file beside the config, so
// other agents are unaffected.
func ConfigPath(getenv func(string) string, options Options) string {
	path, _ := mcpconfig.Location(getenv, options.Name)
	return path
}

// readSettings returns the server's settings object. When the agent's settings
// file has no section for this server yet, the server's own file is used, so a
// configuration made before the merge keeps working until the next write.
func readSettings(getenv func(string) string, options Options) (json.RawMessage, error) {
	path, keys := mcpconfig.Location(getenv, options.Name)
	stored, err := mcpconfig.Read(path, keys)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", options.Name, err)
	}
	if stored != nil || keys == nil {
		return stored, nil
	}
	return mcpconfig.Read(mcpconfig.LegacyPath(getenv, options.Name), nil)
}

// writeSettings replaces the server's settings, which is always the location the
// launcher chose: the agent's settings file when Midas started this process, and
// its own file otherwise.
func writeSettings(getenv func(string) string, options Options, value any) error {
	path, keys := mcpconfig.Location(getenv, options.Name)
	if err := mcpconfig.Update(path, keys, func(json.RawMessage) (any, error) { return value, nil }); err != nil {
		return fmt.Errorf("%s: %w", options.Name, err)
	}
	return nil
}

// ensureToken keeps a bearer token in the server's settings, generating one on
// first use, because an unauthenticated local endpoint is an open door.
func ensureToken(getenv func(string) string, options Options) (string, error) {
	settings := map[string]any{}
	stored, err := readSettings(getenv, options)
	if err != nil {
		return "", err
	}
	if len(stored) > 0 {
		// Settings that cannot be parsed are reported rather than replaced: writing a
		// token-only object over them would discard whatever else the user configured.
		if err := json.Unmarshal(stored, &settings); err != nil {
			return "", fmt.Errorf("%s: parse settings: %w", options.Name, err)
		}
	}
	if token, _ := settings["token"].(string); strings.TrimSpace(token) != "" {
		return token, nil
	}
	random := make([]byte, 24)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	token := hex.EncodeToString(random)
	settings["token"] = token
	if err := writeSettings(getenv, options, settings); err != nil {
		return "", err
	}
	return token, nil
}

func authorize(token string, next http.Handler) http.Handler {
	expected := []byte("Bearer " + token)
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		// The comparison is constant time: a bearer token is a secret, and a byte
		// by byte comparison leaks how much of it matched.
		if subtle.ConstantTimeCompare([]byte(strings.TrimSpace(request.Header.Get("Authorization"))), expected) != 1 {
			writer.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(writer, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(writer, request)
	})
}

func printConfig(stdout *os.File, getenv func(string) string, options Options) error {
	entry := map[string]any{"mcpServers": map[string]any{}}
	servers := entry["mcpServers"].(map[string]any)
	listen := strings.TrimSpace(getenv(strings.ToUpper(options.Name) + "_LISTEN"))
	if listen != "" {
		headers := map[string]string{}
		settings := map[string]any{}
		if stored, err := readSettings(getenv, options); err == nil && len(stored) > 0 {
			if json.Unmarshal(stored, &settings) == nil {
				if token, _ := settings["token"].(string); token != "" {
					headers["Authorization"] = "Bearer " + token
				}
			}
		}
		servers[options.Name] = map[string]any{"url": "http://" + listen + "/mcp", "headers": headers}
	} else {
		servers[options.Name] = map[string]any{"command": []string{options.Name}}
	}
	encoded, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return err
	}
	// Stdout stays valid JSON so a script can pipe it straight into a config file;
	// the note about where the config lives belongs on stderr.
	fmt.Fprintf(stdout, "%s\n", encoded)
	fmt.Fprintf(os.Stderr, "config: %s\n", ConfigPath(getenv, options))
	return nil
}
