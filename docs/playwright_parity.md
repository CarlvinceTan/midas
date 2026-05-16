# midas vs. playwright-cli — Parity

Status of the `cmd/midas` CLI surface relative to `playwright-cli`. Both binaries drive a CDP-attached Chromium; the question is which playwright-cli commands a midas user can invoke today and where the gaps are. Tested 2026-05-08 against playwright-cli (Bun-installed, current latest) and `cmd/midas` built from the current tree.

## How to read the table

- ✅ — implemented in `cmd/midas` with the same intent and roughly the same call shape
- ⚠ — partial: works for the common case but with missing options or different semantics
- ❌ — not implemented; library-level support may exist (`midas/browser`, `midas/session`) but no CLI command exposes it
- 🚫 — out of scope for midas (Playwright-specific debugger or feature)

## Lifecycle / sessions

| playwright-cli                | midas               | Status | Notes |
|-------------------------------|---------------------|--------|-------|
| `open [url] [--headed]`       | `open [url] [--headed]` | ✅ | Honors `MIDAS_CHROME_PATH` to point at a custom binary (e.g. cloakbrowser). Disables both signal-cleanup and parent-PID supervisor so chromium outlives the CLI. |
| `attach <name>`               | `attach <ws-url>`   | ⚠ | midas takes a CDP WS URL directly; playwright-cli takes its own session-name registry handle. Functionally similar — both register an externally-launched browser. |
| `close`                       | `close`             | ✅ | midas drops the CDP connection and SIGTERMs the recorded chromium PID. |
| `delete-data`                 | `delete-data`       | ✅ | Runtime wipe (browser stays running): `Network.clearBrowserCookies` + `Network.clearBrowserCache` browser-wide, `Storage.clearDataForOrigin` for every loaded origin (covers localStorage, IndexedDB, service workers, cache storage, …), and `sessionStorage.clear()` per tab since CDP's Storage domain doesn't manage in-memory tab state. Does not delete the user-data-dir on disk; for that, `close` the session and rm the recorded `UserData` path manually. |
| `list`                        | `list`              | ✅ | midas reads its own `$TMPDIR/midas-sessions/*.json`. |
| `close-all`                   | `close-all`         | ✅ | |
| `kill-all`                    | (none)              | ❌ | Same as `close-all` for midas right now since `close` already SIGTERMs. A separate hard-kill would call SIGKILL. |

## Navigation

| playwright-cli  | midas        | Status | Notes |
|-----------------|--------------|--------|-------|
| `goto <url>`    | `goto <url>` | ✅ | Returns HTTP status. midas uses `LoadStateDOMContentLoaded` with a 15s timeout; playwright-cli's defaults are similar. |
| `go-back`       | `go-back`    | ✅ | |
| `go-forward`    | `go-forward` | ✅ | |
| `reload`        | `reload`     | ✅ | |
| `resize <w> <h>` | (none)       | ❌ | Library has `Page.SetViewportSize`; not wired to a CLI command. |

## Mouse / pointer

| playwright-cli                       | midas                                | Status | Notes |
|--------------------------------------|--------------------------------------|--------|-------|
| `click <target> [button]`            | `click <selector> [--double]`        | ⚠ | midas accepts CSS selector only. playwright-cli also accepts `[ref=eN]` from its snapshot output. midas's snapshot uses a different ref scheme (`[0-N]`) that is not currently round-trippable from the CLI. **Actionability parity:** `Locator.Click`/`Locator.Hover` go through `awaitActionable` (`midas/browser/actionability.go`) — visible + non-zero geometry + scroll-into-view + 80ms layout stability + occlusion check via `document.elementFromPoint` — with a 30s polling retry loop. Errors name the offending element (`click point covered by <div#modal>`). |
| `dblclick <target> [button]`         | `dblclick <selector>` (or `click --double`) | ✅ | Right/middle button parameter not wired in midas — left only. |
| `hover <target>`                     | `hover <selector>`                   | ✅ | Routes through humanize when enabled (Bezier trajectory). |
| `drag <startElement> <endElement>`   | (none)                               | ❌ | Library has `Page.DragAndDrop`; no CLI command. |
| `mousemove <x> <y>`                  | `mousemove <x> <y>`                  | ✅ | |
| `mousedown [button]`                 | (none)                               | ❌ | Fixable — the humanize adapter's `RawMouse.Down` is already there. |
| `mouseup [button]`                   | (none)                               | ❌ | Same as above. |
| `mousewheel <dx> <dy>`               | `mousewheel <dx> <dy>`               | ✅ | Routes through humanize.Scroll when enabled (accel/cruise/decel). |

## Keyboard

| playwright-cli                | midas                | Status | Notes |
|-------------------------------|----------------------|--------|-------|
| `type <text>`                 | `type <text>`        | ✅ | Routes through humanize.Type when enabled (Shift wrap, thinking pauses). Note: humanized `type` does not focus an element on its own — caller must click first. playwright-cli has the same constraint. |
| `fill <target> <text>`        | `fill <selector> <text>` | ✅ | DOM-set + `input`/`change` events. Same shape as playwright-cli's. |
| `press <key>`                 | `press <key>`        | ✅ | Named keys (Enter, Escape, ArrowUp, …) handled via the `namedKeyCDP` table in `browser/page_api.go:310-326`. |
| `keydown <key>`               | `keydown <key>`      | ⚠ | Bypasses the named-key table — sends raw `Input.dispatchKeyEvent` with only `key` populated, so non-character keys may not be recognized by all sites. |
| `keyup <key>`                 | `keyup <key>`        | ⚠ | Same caveat as `keydown`. |
| `select <target> <val>`       | (none)               | ❌ | Library has `Locator.SelectOption`; not wired to CLI. |
| `upload <file>`               | (none)               | ❌ | Library has `Locator.SetInputFiles`; not wired to CLI. |
| `check <target>`              | (none)               | ❌ | Library has `Locator.Check`; not wired to CLI. |
| `uncheck <target>`            | (none)               | ❌ | Library has `Locator.Uncheck`; not wired to CLI. |

## Capture / inspection

| playwright-cli                       | midas                                | Status | Notes |
|--------------------------------------|--------------------------------------|--------|-------|
| `snapshot [element]`                 | `snapshot`                           | ⚠ | Different output format. playwright-cli emits a YAML accessibility tree with `[ref=eN]` markers intended to be round-tripped into `click`/`hover`/etc. midas emits an indented tree with `[frame-N]` codes — informative but not click-targetable from the CLI. Partial (rooted at a sub-element) snapshot is not supported. |
| `eval <func> [element]`              | `eval <expression>`                  | ⚠ | midas takes an expression string and prints the JSON result. playwright-cli takes a function literal (`() => …`) and supports targeting an element via `[ref]`. midas's eval currently scopes to `Page.Evaluate` (main world, main frame). |
| `screenshot [target]`                | `screenshot [--filename=path]`       | ⚠ | midas writes anywhere; playwright-cli rejects paths outside its allow-list. midas does not yet support per-element screenshots; library has `Locator.Screenshot` but the CLI command always captures the page. |
| `pdf`                                | (none)                               | ❌ | |
| `dialog-accept [prompt]`             | (none)                               | ❌ | Library has `Page.AddDialogListener` and `Page.handleDialog`; no CLI bridge. |
| `dialog-dismiss`                     | (none)                               | ❌ | Same as above. |

## Tabs

| playwright-cli            | midas | Status | Notes |
|---------------------------|-------|--------|-------|
| `tab-list`                | (none) | ❌ | Library has `Context.Pages()` returning all pages; CLI surface lacks any tab management. |
| `tab-new [url]`           | (none) | ❌ | Library has `Context.NewPage(url)`. |
| `tab-close [index]`       | (none) | ❌ | Library has `Page.Close`. |
| `tab-select <index>`      | (none) | ❌ | Library has internal active-page tracking. |

Wiring tabs would be the smallest meaningful expansion of midas's CLI: every primitive already exists in `browser/context.go`.

## Storage (cookies, local/session/storage state)

| playwright-cli                            | midas       | Status | Notes |
|-------------------------------------------|-------------|--------|-------|
| `state-load <filename>`                   | (none)      | ❌ | Library has `Context.AddCookies`; nothing for the bundled storage-state format. |
| `state-save [filename]`                   | (none)      | ❌ | Library has `Context.Cookies()`; no aggregate save. |
| `cookie-list`                             | (none)      | ❌ | Library: `Context.Cookies(urls...)`. Easy to wire. |
| `cookie-get <name>`                       | (none)      | ❌ | Filter library output by name client-side. |
| `cookie-set <name> <value>`               | (none)      | ❌ | Library: `Context.AddCookies`. |
| `cookie-delete <name>`                    | (none)      | ❌ | Library: `Context.ClearCookies` (with filter options). |
| `cookie-clear`                            | (none)      | ❌ | Library: `Context.ClearCookies(nil)`. |
| `localstorage-list/get/set/delete/clear`  | (none)      | ❌ | Library has no first-class API; can be done via `Page.Evaluate` against `localStorage`. |
| `sessionstorage-list/get/set/delete/clear`| (none)      | ❌ | Same as above for `sessionStorage`. |

## Network

| playwright-cli                  | midas | Status | Notes |
|---------------------------------|-------|--------|-------|
| `route <pattern>`               | (none) | ❌ | Library exposes a `NetworkManager` (`browser/network_manager.go`) but no public `Route` API yet. Bigger lift. |
| `route-list`                    | (none) | ❌ | |
| `unroute [pattern]`             | (none) | ❌ | |
| `network-state-set <state>`     | (none) | ❌ | Could wrap `Network.emulateNetworkConditions`. |

## DevTools / instrumentation

| playwright-cli                | midas | Status | Notes |
|-------------------------------|-------|--------|-------|
| `console [min-level]`         | (none) | ❌ | Library has `Page.AddConsoleListener` (`browser/page_api.go:478`); no CLI surface. |
| `run-code [code]`             | (none) | ⚠ | Roughly equivalent to `eval` in midas. playwright-cli's `run-code` runs Playwright API calls against `page`/`context`; midas's `eval` only runs JS in the page. |
| `network`                     | (none) | ❌ | Library tracks requests via `NetworkManager`; no aggregate dump. |
| `tracing-start` / `tracing-stop` | (none) | ❌ | No tracing support in library. |
| `video-start` / `video-stop` / `video-chapter` | (none) | ❌ | No video recording in library. |
| `show`                        | (none) | 🚫 | Playwright-specific devtools UI. Not relevant for midas. |
| `pause-at <location>` / `resume` / `step-over` | (none) | 🚫 | Playwright-specific test debugger. Not relevant for midas. |

## Install

| playwright-cli              | midas | Status | Notes |
|-----------------------------|-------|--------|-------|
| `install`                   | (none) | 🚫 | Initialize a workspace; midas has no equivalent workspace concept. |
| `install-browser [browser]` | (none) | 🚫 | The midas CLI assumes you bring your own Chromium via `MIDAS_CHROME_PATH` (or rely on a system Chromium). |

## midas-only commands (not in playwright-cli)

| midas                       | Notes |
|-----------------------------|-------|
| `humanize on\|off\|careful` | Per-session toggle that persists in the session file. Subsequent `click`/`type`/`hover`/`mousewheel` route through the humanize package: Bezier trajectories, per-character keystroke timing, accel/decel scrolling. Confirmed visually — humanized `click + type "hello world"` took ~4.6s end-to-end vs. ~50ms unhumanized. |
| `--human` global flag       | One-shot humanize for a single command, useful when humanize is otherwise off. |
| `wait-for <selector>`       | playwright-cli has no direct equivalent; closest is `eval` in a polling loop. midas accepts `--timeout-ms`. |

## Behavioral findings

Tested side-by-side against `http://127.0.0.1:8765/midas-form.html` (input + button + result div) using the same scenario in both CLIs:

| Step                              | midas                              | playwright-cli                     |
|-----------------------------------|------------------------------------|------------------------------------|
| `open <url>`                      | ✅ Loads, returns WS URL + PID    | ✅ Loads, prints session header   |
| `click "#q"` then `type "..."`    | ✅ Focused, characters land       | ✅ Same                           |
| `click "#submit"`                 | ✅ Submit handler fires           | ✅ Same                           |
| `eval document.querySelector('#result').textContent` | ✅ `"submitted: midas types"` (raw JSON) | ✅ `"submitted: playwright types"` (in `### Result` block) |
| `close`                           | ✅ Chromium reaped via SIGTERM    | ✅ Browser closed                 |

Behavior is functionally identical. Differences are presentational:

- **Output format.** playwright-cli wraps every command in markdown sections (`### Page`, `### Result`, `### Ran Playwright code`) and writes auxiliary artifacts under `.playwright-cli/`. midas prints the bare answer to stdout. Pipe-friendly out of the box; not as rich for a human-in-the-loop reading the output. midas does not auto-snapshot after each command.
- **Snapshot interop.** playwright-cli snapshots produce `[ref=eN]` markers that round-trip into `click <ref>`. midas snapshots produce `[frame-node]` codes that don't. If a workflow relied on referring to elements by snapshot-ref rather than by CSS selector, that workflow won't port without an extra resolution step.
- **Side-effect logging.** playwright-cli writes per-command snapshot YAMLs and console-log files under `.playwright-cli/`. midas does not — every command is purely transactional.
- **File path restrictions.** playwright-cli rejects screenshot paths outside its allow-list (`/home/polymux/code/polymux/.playwright-cli`, `/home/polymux/code/polymux`). midas writes anywhere the process has permission for.

## Bugs / quirks observed

- **Chromium dies if `EnableCrashCleanup` is left at its default.** The `launch` package spawns a parent-PID-watching supervisor that kills the browser when the launching process exits — fine for long-running services, fatal for a CLI that returns immediately. `cmd/midas/cmds.go` explicitly disables both `EnableCrashCleanup` and `HandleSignals` for `open`. If callers later use `session.New` directly they need to do the same or chromium will die between commands.
- **`launch.LaunchLocalChrome` does not set `--password-store=basic` or `--no-sandbox`.** On a Linux host without a kwallet/gnome-keyring instance, chromium can stall on dbus during the first navigation. polymux's launcher (`polymux/internal/browser/chromium.go:158`) avoids this with `--password-store=basic` and `--no-sandbox`. midas's launch path passes neither — so `MIDAS_CHROME_PATH` users on Linux may hit the same hang the polymux comments warn about. Workaround: pass them via `LaunchLocalOptions.ChromeFlags`. Real fix lives in the midas launcher, not the CLI.
- **No active-page targeting.** When more than one tab is open, midas's CLI always operates on whatever `Context.ActivePage()` reports. playwright-cli has explicit `tab-select` to disambiguate. For now, single-tab workflows only.

## Coverage summary

```
Surface counted: ~55 playwright-cli commands

✅ Implemented:        18  (lifecycle, navigation, basic interaction, capture,
                            delete-data)
⚠  Partial:             7  (snapshot ref system, eval scope, screenshot paths,
                            keydown/keyup named keys, click ref vs selector)
❌ Library-ready, no CLI: 19  (drag, mousedown/up, select, upload, check/uncheck,
                              tabs, dialog-accept/dismiss, cookies, console, …)
❌ Library missing:      6  (route/unroute/network-state, state-save/load, video,
                            tracing, pdf, run-code-as-Playwright)
🚫 Playwright-only:     5  (show, pause-at, resume, step-over, install*)
```

The thickest near-term wins (each ≤ 50 LOC of CLI code, library already exists):

1. Tab management — `tab-list/new/close/select`
2. Cookies — `cookie-list/get/set/delete/clear`
3. Dialog — `dialog-accept/dismiss` (drives existing `Page.handleDialog`)
4. Element-targeted commands — `select`, `upload`, `check`, `uncheck`, `drag`, `mousedown`/`mouseup`, screenshot-of-element

The biggest gap that affects polymux today is probably the snapshot-ref system: playwright-cli's `snapshot → click <ref>` flow is what an LLM agent typically uses to disambiguate "which button" without falling back to brittle CSS. midas snapshots emit codes but the `click` command does not accept them. Closing that loop would require either (a) extending `Page.Click` to accept ref codes via a registry kept on the snapshot, or (b) emitting a CSS-selector column in the snapshot that the agent can copy-paste.
