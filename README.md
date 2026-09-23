# Midas

**A native Go coding agent with its own terminal UI.**

Midas runs the agent loop in-process: the provider adapters, session store,
tools, and terminal interface are all here, with no Node.js runtime or other
coding-agent backend underneath. It speaks OpenAI-, Anthropic-, and
Google-shaped APIs directly through a Pi-compatible provider catalog, and keeps
long sessions cheap with an append-only transcript, cache warming, and
compaction.

The same core drives a fullscreen terminal client and a headless binary that can
serve the agent over HTTP.

## Shape

- `pkg/ai` is the provider layer: messages, models, streams, usage accounting,
  and the native adapters.
- `pkg/agent` is the loop: streaming events, steering, cancellation, tool
  execution, compaction, and cache warming. It knows nothing about Midas.
- `internal/mcp` connects MCP servers over the official Go SDK and exposes their
  tools to the loop.
- `pkg/protocol` is the wire contract for driving an agent from outside the
  process, described for other languages in `api/`.
- `internal/` is Midas itself: the TUI, session and settings stores, agent
  profiles, provider resolution, the coding tools, skills, voice, remote access,
  and usage statistics.
- `internal/api` serves that contract over HTTP, and `internal/server` is the
  multi-agent environment — many long-lived agents sharing one MCP pool and one
  browser, with a Teams-shaped API for the user. `cmd/midas` is the interactive
  terminal client; `cmd/server` hosts the environment.

The dependency rule is one-directional: `pkg/` never imports `internal/`, so the
agent core can be embedded on its own. The headless binary is the smallest
demonstration of that — one agent, no delegation, no interface — and the natural
place to build from if Midas becomes a component of something larger.

## Documentation

- [docs/architecture.md](docs/architecture.md) describes the package boundaries
  and data ownership rules.
- [docs/hub.md](docs/hub.md) covers Hub, the optional local homeserver an agent
  can connect to as an MCP server.
- [docs/server.md](docs/server.md) covers the multi-agent server environment: its
  setup, API, vault modes, browser and runtime tuning.
- [docs/manager.md](docs/manager.md) covers running many deployments, one per
  user, from one place.
- [AGENTS.md](AGENTS.md) covers building, installing, and verifying changes.
