// Command vault is the Vault MCP server: a KeePass-compatible store for account
// passwords, TOTP seeds, and recovery codes. The user unlocks it once per session
// with their password; the password is held in memory and never written anywhere.
package main

import (
	"os"

	"github.com/CarlvinceTan/midas/pkg/mcpserve"
	"github.com/CarlvinceTan/midas/pkg/vault"
)

const usage = `vault - secrets over MCP

Usage:
  vault                 serve MCP on stdin and stdout
  vault --listen ADDR   serve MCP over HTTP for several agents
  vault print-config    print the mcp.json entry for this server
  vault version         print the version

The database is KeePass-compatible (vault.kdbx beside your agent config), so it
stays readable by the tools you already trust. Each MCP session unlocks
separately: an agent inherits nothing from another one.
`

func main() {
	err := mcpserve.Run(os.Args[1:], os.Stdout, os.Stderr, os.Getenv, mcpserve.Options{
		Name: "vault", Version: "0.1.0", Usage: usage,
		Server: vault.NewServer(vault.ServerOptions{}),
	})
	if err != nil {
		os.Stderr.WriteString("vault: " + err.Error() + "\n")
		os.Exit(1)
	}
}
