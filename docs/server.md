# Server

`midas-server` is the multi-agent environment: one process hosting many
long-lived Midas agents that share MCP connections and one browser, and talk to
each other and to the user over a single protocol. There is no terminal UI; a
client drives it over HTTP.

A prompt instead of the environment keeps the old single-shot behaviour, which is
still the fastest way to measure one turn:

```sh
midas-server --listen 127.0.0.1:8788     # host the environment
midas-server -p "fix the failing test"   # one prompt, streamed, then exit
```

## Setup

Nothing is hand-edited. First start writes `server.json` beside the agent config
(`MIDAS_CONFIG_DIR`, or `~/.midas`), with a generated bearer token, the agent
roster, and the state directory beside it:

```json
{
  "token": "…",
  "model": "anthropic/claude-sonnet-4.5",
  "vaultMode": "autonomous",
  "agents": [
    { "address": "orchestrator", "role": "orchestrator" },
    { "address": "worker", "role": "worker" }
  ],
  "browser": {
    "binary": "/usr/local/bin/cloakbrowser",
    "profileDir": "/var/lib/midas/browser"
  },
  "hosts": [{ "name": "local", "local": true, "default": true }]
}
```

The browser is expected to be in the image; the environment starts it with its own
profile and attaches over CDP, so agents have browser control with no
configuration. The profile lives on persistent storage, so logins survive a
restart. With no `browser.binary`, agents simply have no browser tool.

## Agents

Each agent is one address on the bus and one parked goroutine. Between messages it
costs a goroutine and its conversation, not a process, a timer, or a poll. Inboxes
are files: a restart replays unhandled work and drops nothing.

Agents reach each other with two tools, which every brain has:

- `ask_agent` — send a question to another address and wait for the answer.
- `tell_agent` — hand over work or a progress note without waiting.

The user is an address too (`user`), so user→agent, agent→agent, and agent→user
are the same mechanism rather than three.

## User transport

Shaped like Polymux Teams, so a client that speaks teams already speaks this.
Everything needs `Authorization: Bearer <token>`; only `/healthz` is open.

| Call | Purpose |
| --- | --- |
| `GET /v1/agents` | the registry: each agent's role, whether it is busy, queued work, messages handled |
| `GET /v1/groups`, `POST /v1/groups`, `DELETE /v1/groups/{group}` | conversations between any mix of agents and the user |
| `GET /v1/groups/{group}/messages` | the transcript, newest last, `?limit=` |
| `POST /v1/groups/{group}/messages` | send as the user; `to` addresses one agent, otherwise the orchestrator takes it |
| `POST /v1/groups/{group}/mark-read` | advance the read cursor |
| `GET /v1/events` | the change feed: every message, streamed, so clients never poll |
| `GET /v1/leases`, `POST /v1/leases`, `DELETE /v1/leases/{resource}` | leases over shared resources such as the browser |
| `GET /v1/hosts` | the devices the environment knows about |
| `GET /v1/mcps` | the shared MCP pool and its state |
| `GET /v1/vault/mode`, `PUT /v1/vault/mode`, `POST /v1/vault/unlock` | the vault's mode |

Leases exist because the browser is a single-owner resource: two agents driving
one tab is how work gets lost. A second holder is refused with the name of the
first, and leases expire so a crashed agent frees its resource.

## Vault modes

- `autonomous` (default) — unlocked: on a server there is nobody at the keyboard
  to approve anything, so requiring a password would mean the vault is never used.
- `user-permission` — locked until the master password arrives over the API, for
  environments where a person is accountable for the secrets.

Switching modes is an API call and is persisted. Switching *to* user-permission
locks the vault again; an earlier unlock does not carry across a mode change.

## Runtime

For maximum CPU and memory efficiency, in order:

1. **Bare systemd** on the host, or **runc** in a container. Native syscall
   performance, and the unit of deployment is one image per environment.
2. **Firecracker microVM per tenant** when customers need hard isolation from each
   other. Near-native CPU, but each VM carries its own kernel and page cache, so
   it is worth it only for isolation, never for density.
3. **Not gVisor.** Its own performance guide reports structural syscall
   interception and VFS costs that hit system-call-bound workloads hardest, and
   Chromium is about as syscall-heavy as software gets.

Tuning that matters, all of it cgroup v2 rather than a VM layer:

- `memory.high` on the environment (throttle and let it shed work) rather than
  `memory.max`, which OOM-kills the browser mid-task;
- a real `/dev/shm` size — the 64 MB container default is what makes Chromium
  fall over;
- `cpu.weight` and `io.weight` split so the browser cannot starve the agents;
- **one shared browser for every agent**, which is the single biggest memory
  decision here: a browser is 150–400 MB, an idle agent a few MB;
- the browser profile on a persistent volume, and the browser's own CPU share
  when agents are busy.

Measure before choosing: run one agent plus the browser under systemd, runc, and
Firecracker, and compare RSS (`ps_mem`), syscall pressure (`pidstat -w`) and task
latency under load.
