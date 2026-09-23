# Manager

`manager` runs several Midas server deployments from one place, **one deployment
per user**. Each deployment is its own `midas-server` process with its own
directory, port, token, browser profile and agent roster, so provisioning a user
is one command and nothing of theirs is visible to anybody else.

```sh
manager create alice --model anthropic/claude-sonnet-4.5
manager start alice
manager list
```

```
USER   PORT  STATE    DIRECTORY
alice  8900  running  ~/.midas/manager/deployments/alice
bob    8901  running  ~/.midas/manager/deployments/bob
```

## What a deployment is

Provisioning does not start anything: `create` writes the deployment's directory
and its `server.json`, and `start` runs the process. The directory holds
everything that user's agents touch:

```text
~/.midas/manager/deployments/alice/
├── server.json     # token, model, browser profile, agent roster
├── server.log      # the deployment's own log
├── state/          # inboxes and group transcripts
└── browser/        # the shared browser's profile, on persistent storage
```

The token is generated per deployment and never shared, so one user's client
cannot reach another's server. Ports are allocated from `basePort` (8900 by
default), skipping any that are taken.

## Commands

| Command | Purpose |
| --- | --- |
| `manager create USER [--model P/M] [--server PATH]` | provision a user's deployment; starts nothing |
| `manager list` | every deployment with its port, state and directory |
| `manager start USER` | run the deployment's server, detached, logging to its own file |
| `manager stop USER` | ask it to shut down, and clear its restart flag so it stays down |
| `manager remove USER [--purge]` | forget a deployment; its directory is kept unless `--purge` |
| `manager print-config USER` | the socket URL and token for a client |

`remove` deliberately keeps the directory: taking a user out of the manager
should not silently destroy their agents' state, so deleting it is a separate,
explicit `--purge`.

## Configuration

`manager.json` beside the agent config holds the manager's state, mode 0600
because it contains every deployment's token:

```json
{
  "basePort": 8900,
  "serverBinary": "/usr/local/bin/midas-server",
  "deployments": {
    "alice": { "user": "alice", "port": 8900, "dir": "…", "token": "…", "restart": true }
  }
}
```

`--server` on `create` pins a deployment to a specific binary; otherwise the
manager uses `serverBinary`, or the `midas-server` on `PATH`.

## Talking to a deployment

`manager print-config alice` prints the WebSocket URL with that deployment's
token, which is the user transport: the change feed and sends over one socket.
The HTTP API is the same one `docs/server.md` describes, on the deployment's own
port.

## Deployment shape

The manager assumes one small machine per deployment, or one container per
deployment when you want hard isolation between users — see the runtime section of
`docs/server.md` for the cgroup and browser tuning, and why Firecracker is worth
it only for tenant isolation while gVisor is worth it for nothing here.
