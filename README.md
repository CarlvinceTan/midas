# midas

A CDP-native browser-automation library for Go, ported from Stagehand v3
concepts and adapted for polymux (agent tool surface) and argus (semantic
snapshot) integration. It speaks the Chrome DevTools Protocol directly — no
Playwright dependency — and ships a playwright-cli-style CLI.

Module: `github.com/PolymuxOrg/midas`.

## Layout

Public API (importable by polymux/argus) lives at the module root; private
implementation lives under `internal/`; the CLI lives under `cmd/`.

**Public packages**

- `browser/` — the core: CDP-backed `Context`, `Page`, `Frame`, `Locator`,
  actionability, network, screenshots, dialogs. Sub-packages `browser/dom`
  (selector/helper injection) and `browser/snapshot` (a11y tree + XPath/URL
  maps; `snapshot.Result` is re-exported as `browser.SnapshotResult`).
- `session/` — high-level session wrapper (launch-or-attach + connect).
- `launch/` — local and kernel Chromium launch flows.
- `tools/` — the agent tool registry (`tools.BoundService`); the polymux
  integration surface.
- `cache/` — action record/replay with selector self-heal.
- `humanize/` — humanized input timing (Bezier mouse, keystroke cadence, scroll).

**Private (`internal/`, not importable outside the module)**

- `internal/cdp/` — low-level CDP connection, sessions, event dispatch.
- `internal/debug/` — conditional debug logging (`MIDAS_DEBUG_BROWSER`).

**Binary**

- `cmd/midas/` — the CLI (`open`/`goto`/`click`/`fill`/`snapshot`/… plus a
  persistent `daemon` mode that holds one CDP connection per session).

## Integration notes

- **polymux** is expected to drive midas as a library via `tools.BoundService`
  over a long-lived `browser.Context` — that path keeps one CDP connection and
  cross-command state (active tab, held modifiers, dialog listeners) for the
  session's lifetime. The CLI is the alternative integration; its `daemon` mode
  gives the same persistent-connection benefit for CLI-driven use.
- **argus** replaces the heuristic snapshot at the `extract`/snapshot seam; see
  `argus/INTEGRATION.md`.

## Testing

Unit tests run against fake CDP sessions; `e2e/` drives a real headless
Chromium (see `e2e/README.md`), including an opt-in cross-tool comparison
against playwright-cli and Vercel's agent-browser. Findings and fixes are
tracked in `docs/midas_findings.md`.
