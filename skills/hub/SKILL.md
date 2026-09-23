---
name: hub
description: "Driving the hub MCP server — a local agent homeserver that attaches messaging bridges on demand. Covers installing bridges from a registry, the login flow with QR codes for terminal display, reading accounts and threads, sending messages, and subscribing to changes. Use when the user asks about their messages, mail, or chat accounts through hub, when a hub_ tool call fails because nothing is installed, or when setting hub up for the first time. Not for MCP servers generally (→ that is the harness's own MCP configuration) and not for writing a bridge (→ see the bridge protocol in the hub repository's docs)."
user-invocable: true
license: MIT
compatibility: Designed for Midas, Claude Code, Codex or any harness with a hub MCP server configured.
---

# Hub

Hub is a local homeserver that connects messaging bridges on demand and exposes
their accounts, messages, and logins over MCP. It runs as its own process; an
agent reaches it only if the harness configuration lists it as an MCP server.

## Nothing is attached by default

An unconfigured hub has no bridges, and every tool answers with a note saying so.
That is the expected first-run state, not a failure.

To see what can be installed:

```
hub_bridges {"action":"available"}
```

If that reports no registry configured, the user has not set one up: hub never
fetches a catalog it was not pointed at. Ask them for a source path or URL, or
install with an explicit source. To install from the catalog, name only the
entry — source, command, environment, and checksum come from the registry:

```
hub_bridges {"action":"install","name":"telegram"}
```

Installing does not start anything. A bridge starts on first use and stops again
after it goes idle, so the hub costs nothing while it is unused.

## Accounts come first

One bridge process can serve several accounts, and every account-scoped call
names the account it means. Never guess one; if unsure, ask:

```
hub_accounts
```

An account the bridge does not serve is refused, and so is a call with no account.

## Logging in

A login is a device link, not a password form. Start it, then show the user what
the bridge returned:

```
hub_login {"action":"start","account":"personal","render":true}
```

`kind` is `qr`, `code`, `link`, `password`, or `verify`. Pass `render: true` to
get `rendered`: rows of Unicode half blocks a terminal prints verbatim, so no QR
library is needed. Without `render` you get the raw payload instead, which is
what a client with its own renderer wants.

- Print the QR rows exactly as given, including the quiet zone, and mention the
  `hint` (for example "WhatsApp → Linked devices").
- A QR expires and rotates: poll `hub_login {"action":"status","loginID":…}` and
  reprint when the payload changes.
- Codes and passwords are answered with
  `hub_login {"action":"submit","loginID":…,"response":…}`.
- A challenge payload links a device, so it belongs to the user's screen only:
  never store it, echo it into a transcript unnecessarily, or send it anywhere.
  It is readable only by the session that started the login.
- When the login finishes the account appears in `hub_accounts`; no restart is
  needed.

## Reading and sending

Read with bounded windows rather than whole histories:

```
hub_threads  {"account":"personal"}
hub_messages {"account":"personal","thread":"alice","limit":20}
hub_send     {"account":"personal","to":"alice","text":"on my way"}
hub_mark_read{"account":"personal","thread":"alice"}
```

`hub_messages` returns the newest messages in a window; page back by lowering the
window rather than asking for everything. `hub_threads` carries unread counts,
which come from the read cursors `hub_mark_read` advances.

## Prefer being told over asking

`hub://threads/{account}/{thread}` and `hub://logins/{loginID}` are updated when
a thread or a login changes. Subscribe to the ones that matter and react to the
update instead of polling: a polling loop burns tokens and attention for no gain.
A client that never subscribes still sees the same data through the tools.

## Reporting

Say what was actually observed: which account, which thread, which bridge. Do not
summarise a thread you have not read, and do not claim a message was delivered
without the `hub_send` result that says so.
