// Package e2e drives Midas' MCP servers the way an agent does: the real binaries,
// the real MCP protocol, and a configuration directory of their own.
//
// Nothing here touches the machine's own state. Each server runs with
// MIDAS_CONFIG_DIR pointed at a temporary directory, the hub installs the mock
// bridge into that directory, the vault creates its own database there, and
// control is pointed at a browser the test launches with a temporary profile. A
// developer can therefore run the suite on a working machine without their
// accounts, their vault, or their browsers being involved. e2e/Dockerfile runs the
// same suite in a container that has nothing of the host at all.
package e2e
