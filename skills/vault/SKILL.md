---
name: vault
description: "Using the user's secrets during a task through the Vault MCP server: account passwords, TOTP codes, and recovery codes from a KeePass-compatible database the user unlocks. Covers unlocking for a session, using a credential without leaking it, and the confirmation needed before anything is stored or deleted. Use when a task needs to sign in somewhere, answer a 2FA prompt, use a recovery code, or store a new credential. Not for deciding what the user's passwords should be, and not for reading secrets out of a browser or a keychain directly — the vault is the route the user has approved."
user-invocable: true
license: MIT
compatibility: Designed for Midas and for any harness with the Vault MCP server configured.
---

# Vault

The vault is a KeePass-compatible database beside the agent's own configuration.
It holds account passwords, TOTP seeds, and recovery codes, and it stays readable
by the tools the user already trusts, which is the point: nothing about it is
Midas-specific.

## Unlocking

Nothing is readable until the user unlocks the session:

1. If a tool reports the vault is locked, ask the user for the password. Do not
   guess, and do not look for it anywhere else on the machine.
2. Call `vault_unlock` with what they gave you. It is held in memory for that
   session only and never written anywhere.
3. If there is no vault yet, say so and offer to create one: the user chooses the
   password, and they should choose it themselves.

Unlocking is per session, so another agent's unlock does not extend to this one —
and yours does not extend to it. Call `vault_lock` when the work is done.

## Using a secret

- `vault_list` first when you do not know the entry's exact title; it shows what
  each entry holds and never a secret.
- `vault_password` returns a password; `vault_recovery_codes` returns recovery
  codes. Use them for the request at hand, then move on.
- `vault_totp` returns the current code **and how long it stays valid**. If it is
  about to expire, wait a few seconds and ask again rather than submitting a code
  that will be rejected. Never store a code or reuse one.

A secret is a means to an end, not a result: never echo a password or code into a
transcript, a commit, a log, a summary, or a message to anyone. If the user needs
to see one, read it back to them in the conversation where they asked for it.

## Storing and deleting

Ask before writing a secret the user did not just give you — a password they
typed, or one you generated and they accepted. `vault_put` updates only the
fields you pass, so adding a TOTP seed never clears an existing password.

Deleting an entry, changing the vault password, and destroying the vault are the
user's decisions, not the agent's: confirm the specific thing first, and for
destroying the vault repeat the path they approved. Never invent a value, never
"fix" a stored password to make a login work, and never create a second vault to
work around a locked one.

## Reporting

Say what you used and what it achieved — "signed in as carl@example" — and never
what the secret was. If a credential fails, report the failure rather than the
value, and ask the user to confirm the secret is current before trying again.
