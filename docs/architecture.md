# Architecture

`pkg/` is the reusable core, `internal/` is Midas itself, and `cmd/` holds the
binaries.

## Layout

```text
cmd/mock-bridge             reference bridge for the hub
cmd/hub                  local homeserver over MCP
cmd/midas                interactive terminal client
pkg/ai                   messages, models, streams, usage, provider adapters
pkg/agent                loop: events, steering, tools, compaction, cache warming
pkg/control              desktop and browser state, and acting on a surface
pkg/control/cdp          Chromium/Firefox debugging endpoints
pkg/hub                  optional local homeserver: bridges, store, MCP surface
pkg/vault                KeePass-compatible secrets: passwords, TOTP, recovery codes
internal/chat            chat session runtime: turns, steering, title and status helpers
internal/mcp             MCP servers: config discovery, connections, tool adapters
internal/provider        provider and credential resolution, model discovery
internal/storage         session store: transcripts, drafts, queues
internal/tui             terminal UI
internal/                goal, instructions, profiles, remote, settings, skills,
                         stats, storage, tools, voice, and the entry points above
```

## Dependency direction

```text
cmd/midas  ──> internal/tui, internal/chat ──> pkg/agent ──> pkg/ai
```

- `pkg/` never imports `internal/`, so the core embeds on its own.
- `pkg/agent` knows nothing about Midas: no profiles, no provider catalog, no UI.
- `pkg/hub` is independent of the agent: it imports nothing from `internal/`, and
  nothing in `cmd/midas` imports it, so the agent binaries never link it or its
  dependencies. It is reached over MCP, by configuration alone.
- `internal/chat` is the only package that composes an agent, a session, tools,
  goals, MCP, and compaction into something an interface can drive.

## Data ownership

- Midas reads and writes only its own data: `~/.midas` (or `MIDAS_CONFIG_DIR`),
  the project scopes it is told about, and nothing belonging to another agent.
- Instructions: `~/.midas/AGENTS.md`, then the instruction file of each directory
  from the filesystem root down to the working directory.
- MCP: the `mcpServers` object of `~/.midas/settings.json`,
  `<project>/.midas/settings.json`, `<project>/.agents/settings.json`; more
  specific scopes override earlier ones by name. A server launched by Midas also
  keeps its own settings under `mcp.<name>` in `~/.midas/settings.json`, so
  Midas' configuration is one file. The legacy `mcp.json` files in the same three
  scopes are still read for servers the settings files do not name, and a server
  started by another agent keeps its own `<name>.json` and reads `mcp.json`,
  exactly as before.
- Skills: `~/.midas/skills`, `<project>/.midas/skills`,
  `<project>/.agents/skills`.
- Agents: `agents` in `~/.midas/settings.json` defines agents beside the
  built-ins (`main`, `advisor`, `explore`, `summary`, `title`):

  ```json
  {
    "agents": {
      "reviewer": {
        "description": "Reviews diffs for correctness.",
        "mode": "subagent",
        "model": "openai/gpt-5.6-sol",
        "reasoning": "high",
        "prompt": "You review diffs and report problems.",
        "tools": "read-only",
        "parents": ["main"],
        "maxDepth": 2
      }
    }
  }
  ```

  `mode` is `primary` (an entry point like `main`), `subagent`, or `utility`;
  `tools` is `full`, `read-only`, or `none`; `parents` names the agents this one
  inherits its model from (main by default for anything that is not primary);
  `maxDepth` caps how many levels of subagents may run beneath it. A name that is
  already built in, an unknown mode, an unknown reasoning level, or a parent that
  is not an agent fails at startup with the agent's name.

## End-to-end lane

`e2e/` drives the real binaries the way an agent does: the hub, control, and vault
are built from this checkout and spoken to over the MCP protocol, each with a
configuration directory of its own. Nothing of the developer's machine is involved
— not their config, not their accounts, not their browsers:

- the hub installs `cmd/mock-bridge` (the reference bridge, which models a chat
  service with communities, channels, every attachment kind, edits, reactions,
  deletions, and a device-link login) and exercises the whole lifecycle;
- control is pointed at a Chromium the suite launches itself, with its own profile
  and debugging port (`MIDAS_E2E_BROWSER`, or Playwright's cache, or a desktop
  install), so the endpoint discovery and attach paths run against a real browser;
- the vault creates a database of its own, and one test signs into a local page
  with a credential the vault holds through the endpoint control found, which is
  the two servers working together.

The browser tests fail with instructions when no Chromium is available; set
`MIDAS_E2E_SKIP_BROWSER=1` to skip them instead. The same suite runs in a container
with nothing of the host at all:

```sh
docker build -f e2e/Dockerfile -t midas-e2e .
docker run --rm midas-e2e
```

That image is a Linux box with the Go toolchain and a Chromium.

## Agent model and reasoning

One rule decides the model and reasoning level a session runs with, for a new
session and a resumed one alike:

- An explicit `--model`/`--provider` (or `MIDAS_MODEL`/`MIDAS_PROVIDER`) is a
  choice for that run and wins.
- Otherwise the active agent's own model applies: the `/agents` pin first, then
  the `model` its settings.json definition names, then — for an entry agent —
  the model it last used in any session.
- A model whose provider has no credentials on this machine is not used at
  startup: Midas falls back to the provider default and says why, so a settings
  file carried between machines cannot break the session.
- The reasoning level is the agent's own (`/agents`, then its definition), then
  the level remembered for that model, then the model's default. An explicit
  `off` is kept.

Every model and level the user picks is persisted the moment it is chosen, so the
next session resumes with it.
