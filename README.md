# Midas

**A native Go coding agent with its own terminal UI.**

Midas runs the agent loop in-process: the provider adapters, session store,
tools, and terminal interface are all here, with no Node.js runtime or other
coding-agent backend underneath. It speaks OpenAI-, Anthropic-, and
Google-shaped APIs directly through a Pi-compatible provider catalog, and keeps
long sessions cheap with an append-only transcript, cache warming, and
compaction.

The fullscreen terminal client is the product; the same core is a standalone
library, so it can be embedded on its own. The multi-agent server environment and
its deployment manager now live in Flarebot, which builds on these packages.

## Shape

- `pkg/ai` is the provider layer: messages, models, streams, usage accounting,
  and the native adapters.
- `pkg/agent` is the loop: streaming events, steering, cancellation, tool
  execution, compaction, and cache warming. It knows nothing about Midas.
- `internal/mcp` connects MCP servers over the official Go SDK and exposes their
  tools to the loop.
- `internal/` is Midas itself: the TUI, session and settings stores, agent
  profiles, provider resolution, the coding tools, skills, voice, remote access,
  and usage statistics.
- `cmd/midas` is the interactive terminal client.

The dependency rule is one-directional: `pkg/` never imports `internal/`, so the
agent core can be embedded on its own. That is what makes Midas a component of
something larger rather than only a terminal program.

## Documentation

- [docs/architecture.md](docs/architecture.md) describes the package boundaries
  and data ownership rules.
- [docs/hub.md](docs/hub.md) covers Hub, the optional local homeserver an agent
  can connect to as an MCP server.
- [AGENTS.md](AGENTS.md) covers building, installing, and verifying changes.
