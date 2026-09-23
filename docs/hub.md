# Hub

Hub is a local agent homeserver: one process that connects messaging bridges on
demand and exposes their accounts, messages, and logins over MCP. Agents talk to
it like any other MCP server; nothing in Midas depends on it.

It exists so several agents can share one set of connections without each of them
holding credentials, and so the heavy part of messaging — bridge processes — is
paid for only when someone actually uses it.

## Nothing is attached by default

An unconfigured hub starts no bridge processes, opens no connections, runs no
timers, and reads no credentials. Its configuration is `hub.json` beside the
agent config (`MIDAS_CONFIG_DIR`, or `~/.midas`), and it is created only when
something changes:

```json
{
  "listen": "",
  "idleTimeout": "10m",
  "registry": "",
  "bridges": {}
}
```

`listen` empty means stdio only. `idleTimeout` is how long a bridge may sit
unused before it is stopped again. `registry` points at an optional catalog of
installable bridges, described below; empty means an install must name an
explicit source, so an unconfigured hub never uses the network.

The hub's own state lives in `~/.midas/hub/` (`HUB_STATE_DIR` overrides it):
`messages.jsonl`, `reads.json`, and `bridges/<name>/`. It reads nothing else in
that directory — not `auth.json`, not sessions, not settings.

## Connecting an agent

`hub print-config` prints the snippet to paste into an MCP client's config:

```json
{ "mcpServers": { "hub": { "command": ["hub"] } } }
```

For several agents sharing one homeserver, run `hub --listen 127.0.0.1:8787`. The
hub generates a bearer token into `hub.json` (mode 0600) the first time it
listens, and every request must present it:

```json
{ "mcpServers": { "hub": { "url": "http://127.0.0.1:8787/mcp",
  "headers": { "Authorization": "Bearer <token from hub.json>" } } } }
```

## Accounts

A bridge serves one or more accounts, and every account-scoped call names the one
it means: `hub_send`, `hub_threads`, `hub_messages`, `hub_mark_read`, and
`hub_login` all take an `account`. One bridge process serves all of its accounts,
so using a second account on the same bridge does not start anything new. Each
account keeps its own history, threads, unread cursors, and logins — two logins
can be in flight at once, each with its own challenge.

Naming a bridge is optional only when exactly one is installed; with several
installed, the call says so rather than guessing. An account the bridge does not
serve is refused, and so is a call with no account at all.

The reference bridge in `cmd/mock-bridge` serves two accounts (`personal` and
`work`), which is how the multi-account path stays exercised.

A skill for agents that talk to this hub ships in `skills/hub/SKILL.md`. Install
it by copying that directory into `~/.midas/skills/hub/` (or a project's
`.agents/skills/hub/`), which is where a harness discovers skills.

## Tool surface

The tool list is fixed. Installing a bridge changes what the tools return, never
which tools exist, so a client's request prefix stays stable and cacheable.

| Tool | Purpose |
| --- | --- |
| `hub_bridges` | list installed bridges, list what the registry offers, and install, uninstall, start, or stop them |
| `hub_accounts` | accounts the installed bridges serve (starts them lazily) |
| `hub_threads` | conversations for an account, with unread counts |
| `hub_messages` | a bounded window of messages for a thread |
| `hub_send` | send a message through a named bridge account |
| `hub_mark_read` | advance a thread's read cursor |
| `hub_login` | start, inspect, answer, or cancel an account login |

With no bridges installed, these return an empty result plus a one-line
explanation rather than an error.

## Finding and installing bridges

A registry is an optional catalog, either a local path or a URL:

```json
{
  "registry": "/path/to/registry.json",
  "registryTTLSeconds": 3600
}
```

```json
{
  "bridges": [
    {
      "name": "telegram",
      "description": "Telegram accounts",
      "source": "/opt/bridges/telegram-bridge",
      "checksum": "…sha256…",
      "command": ["telegram-bridge"],
      "env": { "TELEGRAM_TOKEN_FILE": "/home/me/.secrets/telegram" }
    }
  ]
}
```

`hub_bridges {"action":"available"}` lists that catalog, marking entries that are
already installed, so an agent can see what exists without searching the network.
`hub_bridges {"action":"install","name":"telegram"}` then installs from the
catalog — source, command, env, and checksum all come from the entry, so nothing
has to be guessed. Add `"refresh": true` to read the registry again instead of the
cached copy, which is reused for `registryTTLSeconds` (an hour by default).

With no registry configured the hub fetches nothing: `available` says so, and an
install must name its own `source`. That is deliberate — an unconfigured hub never
reaches the network on its own, and it never invents a source for a bridge.

## Login

A login is a small state machine, because a QR payload is displayed rather than
typed into an agent:

- `hub_login {"action":"start","account":"personal","render":true}` returns a
  `login` with a challenge: `kind` is `qr`, `code`, `link`, `password`, or
  `verify`, and `payload` is the bridge's own value.
- `render` adds `rendered`: rows of Unicode half blocks a terminal prints
  verbatim, so a TUI needs no QR library. It is opt-in because a matrix of rows
  is expensive to put in a model's context.
- `hub_login {"action":"submit","loginID":"…","response":"123456"}` answers a
  code or password step, `status` polls, `cancel` abandons it.

A challenge payload links a device, so it is readable only by the MCP session
that started the login, and a login whose owner is unknown belongs to nobody.

## Push

Clients that want to be told about incoming messages subscribe to a resource:

`hub://threads/{account}/{thread}` and `hub://logins/{loginID}` are updated
whenever the hub stores a message or a login changes step. MCP has no generic
server notification and logging notifications are deprecated, so resource
subscriptions are the channel; a client that never subscribes loses nothing,
since the same data is readable through the tools.

## The bridge protocol

A bridge is any executable that speaks newline-delimited JSON on stdio. The hub
starts it on first use and stops it when idle. `cmd/mock-bridge` is a complete
reference in about a hundred lines.

Hub to bridge:

```json
{"id":1,"method":"hello"}
{"id":2,"method":"accounts"}
{"id":3,"method":"threads","params":{"account":"personal"}}
{"id":4,"method":"history","params":{"account":"personal","thread":"alice","limit":20}}
{"id":5,"method":"send","params":{"account":"personal","to":"alice","text":"hi"}}
{"id":6,"method":"markRead","params":{"account":"personal","thread":"alice","upTo":1790000000000}}
{"id":7,"method":"login","params":{"account":"personal"}}
{"id":8,"method":"answer","params":{"account":"personal","response":"123456"}}
{"id":9,"method":"cancel","params":{"account":"personal"}}
```

Bridge to hub:

```json
{"id":1,"result":{"name":"mock","version":"0.1.0"}}
{"id":3,"error":"no such account"}
{"event":"message","data":{"id":"m1","account":"personal","thread":"alice","text":"hi","timestamp":1790000000000,"incoming":true}}
{"event":"challenge","data":{"account":"personal","kind":"qr","payload":"2@abc…","hint":"Scan in the app","expiresAt":1790000060000}}
{"event":"status","data":{"account":"personal","status":"connected"}}
```

`hello` is the readiness handshake: the hub waits for it before considering a
bridge started. Everything else is optional — a bridge that answers only
`hello`, `accounts`, and `login` is a working bridge.

Writing one in Go is a `main` plus a handler, since `hub.ServeBridge` owns the
framing, and the handler is handed an emitter for `message`, `challenge`, and
`status` events.

## Installing a bridge

```json
{"action":"install","name":"mock","source":"/path/to/bridge","command":"bridge-binary"}
```

The source is a local path or URL and is copied into `~/.midas/hub/bridges/<name>/`;
`checksum` is verified when given. The bridge is not started by installing it:
it starts on first use. `uninstall` stops it, forgets it, and removes its files.
