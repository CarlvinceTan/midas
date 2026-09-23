---
name: control
description: "Driving the desktop an agent runs on with the Control MCP server: read devices, apps, windows and browser tabs, then act on an exact surface under a lease — clicks, keystrokes, navigation, page JavaScript, screenshots — with human attention checked first. Use at the start of any task that involves the user's screen or browser, and whenever an action has to land in a window the user can also see. Not for building a browser driver or extension, and not for the server environment's browser, which Control MCP does not drive."
user-invocable: true
license: MIT
compatibility: Designed for Midas and for any harness with the full Control MCP server configured.
---

# Control

At the start of every task, read the desktop once:

```
control_state
```

This is ambient routing evidence, not authorization to read unrelated content or
operate anything. If the task does not involve a device or an interactive surface,
use no further Control commands. Do not report unrelated availability warnings.

## Working autonomously

1. Prefer an authorized connector, API, CLI, or remote shell when it covers the task.
2. Resolve the exact device and surface from current provider IDs. Titles, URLs,
   positions, and list order are context, never identity. Narrow the reading with
   `control_state --device DEVICE --app-id APP --full --fresh` when needed.
3. Observation is lock-free and must never activate, select, scroll, or navigate.
4. Acquire a lease before UI mutation on **every** device, including agent-owned
   desktops. The coordinator resolves live identity and checks current human use on
   shared devices. Unknown identity or unknown attention blocks new acquisition,
   except for the agent-managed embedded browser route below.
5. Use a background action route bound to that exact surface. Enter an action fence
   before dispatch and finish it only after checking the outcome: one action, then
   verify. The native window route does this automatically.
6. Keep the lease while thinking, researching, waiting, or accepting human help.
   Renew it or run the bounded lease keeper. Release on completion, explicit
   abandonment, or transfer. An expired or released lease requires new admission.
7. Verify the task's actual result. A dispatched click or a changed dialog is an
   intermediate observation. Never replay an uncertain action automatically.

## What observation costs

Reading state never changes a setting, and diagnosis stays read-only: it never
installs an extension, starts or restarts a browser, adds a debugging flag,
creates a profile, changes the default browser, rewrites Dock or taskbar entries,
or requests an OS permission on its own.

Enabling a capability is a separate, deliberate step, taken only for actual
control and only after the user agrees:

| What you want | What it needs | Who decides |
| --- | --- | --- |
| Windows, tabs, titles, URLs | the one-time "wants to control" consent per app | nothing further |
| Application list, frontmost app | consent for System Events | nothing further |
| Other apps' window titles, UI elements | Accessibility | the user, in System Settings |
| Clicks, keystrokes in any app | Accessibility | the user, in System Settings |
| Screen capture for visual verification | Screen Recording | the user, in System Settings |
| Page JavaScript, screenshots, console, network | a browser debug endpoint | Control offers, the user approves |
| Anything in Firefox | its profile preferences | Control writes them on request |

A gap in a reading names which of these is missing. Report the toggle or the
decision the user has to make; never reset or rewrite privacy permissions.

## Browsers

Two routes, and the cheaper one covers most tasks:

- **Apple Events** — windows, tabs, titles, URLs, and navigation, for Safari and
  the Chromium family, after the consent dialog. No port, no flag, no settings.
- **Debug endpoint (CDP)** — a page's JavaScript, screenshots, console, and
  network. This is the only reason to enable one. Control offers to quit and
  relaunch the browser so the endpoint exists, because Chromium ignores the flag
  while that profile is already running; the user decides whether their windows
  may close. Every window opened afterwards is reachable without another restart.

Firefox has no scripting dictionary, so it needs the endpoint even to read tabs.
Its endpoint comes from a preference, so Control writes it into the profile's
`user.js` on request and it survives restarts with no launcher.

Google Chrome 136 and later refuse the flag on the default profile: it needs a
separate `--user-data-dir` and a one-time sign-in. Helium, Brave, Edge, and Firefox
do not.

## Working alongside the human

For a step only the human can complete, leave the prepared surface available,
retain coordination, request only the necessary step, and resume from live
completion evidence. Human help does not require releasing or reacquiring the
lease. Task authorization governs sends, submissions, deletion, and other
consequences; Control never grants extra authorization.

**Agent-managed embedded browser exception.** When a host application exposes its
own agent-facing tool for an embedded browser surface, that tool may be used
without a Control lease, even if human attention is unknown: resolve the exact
surface from fresh provider metadata, read the current page before acting, and
serialize actions on that tab. An ambient URL, a title, or an application calling
itself agentic is not evidence of this route. If Control cannot map the tab, do not
block, ask for extra permission, or fall back to leasing the whole native app.

## Remote devices

A remote device or VM is a device like any other: resolve it through its provider,
prefer its API or remote shell over its screen, and lease it for exactly as long as
the work needs. Do not assume that a machine without a person in front of it is
free to drive — an unattended desktop can still be someone's.

## Reporting

Separate observation from inference, and name the surface you acted on: which
device, which application, which tab. Say what was verified and what is still
uncertain. Never present a dispatched action as a completed task.
