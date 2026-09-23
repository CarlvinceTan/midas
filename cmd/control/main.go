// Command control is the Control MCP server: it reads desktop and browser state,
// and it can enable a browser's debugging endpoint for page JavaScript and
// screenshots.
package main

import (
	"os"

	"github.com/CarlvinceTan/midas/pkg/control"
	"github.com/CarlvinceTan/midas/pkg/mcpserve"
)

const usage = `control - desktop and browser state over MCP

Usage:
  control                 serve MCP on stdin and stdout
  control --listen ADDR   serve MCP over HTTP for several agents
  control print-config    print the mcp.json entry for this server
  control version         print the version

Reading state changes nothing. A browser endpoint is only enabled when a client
asks for it, and enabling one on a Chromium browser closes its windows.
`

func main() {
	err := mcpserve.Run(os.Args[1:], os.Stdout, os.Stderr, os.Getenv, mcpserve.Options{
		Name: "control", Version: "0.1.0", Usage: usage,
		Server: control.NewServer(control.ServerOptions{Endpoints: endpoints{}}),
	})
	if err != nil {
		os.Stderr.WriteString("control: " + err.Error() + "\n")
		os.Exit(1)
	}
}
