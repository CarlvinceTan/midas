# midas — test-suite findings & fix backlog

This is the output of building the real-browser e2e suite (`midas/e2e/`). The
suite was designed by studying how Playwright, Stagehand v3, and Vercel's
agent-browser test themselves, then mapping those behaviors onto midas's
CDP-native stack and its polymux/argus integration surface.

Building the suite surfaced two **blocker deadlocks** (Bug 0 and Bug F — the
second is the structural class behind the first), a cluster of
**actionability/input defects** (Bugs A–E), a dialog-API deadlock, a **CLI
browser-lifetime bug** (Bug G), a **broken drag-and-drop** (Bug H), and
**non-functional keyboard modifier combos** (Bug I). **All are now fixed**; the
tests that pinned them are enforcing regression tests.

Status: **107 pass · 0 skip · 0 fail.** All pre-existing unit tests still pass
(`go test ./...`), and the concurrency changes are race-clean.

## Cross-tool comparison (midas vs playwright-cli vs agent-browser)

`TestCrossToolComparison` (opt-in: `MIDAS_COMPARE=1`) drives all three CLIs
through 19 website scenarios over identical fixtures, verifying each outcome via
the tool's own `eval`. Latest result:

| tool | PASS | FAIL | UNSUP | time |
|---|---|---|---|---|
| **midas** | **19** | 0 | 0 | ~3m |
| playwright-cli | 18 | 1 | 0 | ~33s |
| agent-browser | 14 | 5 | 0 | ~10s |

- **midas is the only tool that passes all 19** — and the only one besides
  playwright to pass the five **actionability** scenarios (scroll-into-view,
  disabled→enabled wait, overlay-clears wait, open-shadow click, dynamic
  late-appearing element). agent-browser fails all five (its click neither waits
  nor scrolls nor pierces shadow); that gap is exactly what midas's actionability
  layer (bugs A/B/E) handles. playwright's one miss is dynamic-wait via its CLI.
- The first run showed midas at **12 PASS / 7 UNSUP** — the UNSUP were
  `check`/`uncheck`/`select`/`drag`/`keycombo`, all polymux agent tools with
  existing library primitives but no CLI/tool exposure. Closing that gap (below)
  took midas to 19/19.
- midas was slowest because its stateless CLI re-attached CDP per command. Root
  cause + fix below (Bug J); midas CLI is now ~0.03s/command, and a new daemon
  mode is ~0.00s/command.

Full matrix: `docs/tool_comparison.md` (regenerated each run).

## Bug J — every CLI command paid a fixed 3s bootstrap stall — FIXED

**Symptom.** Each midas CLI command took a flat ~3.00s (so the comparison took
3m vs agent-browser's 10s). Traced to `waitForInitialTopLevelTargets(ctx,
topLevel, 3*time.Second)` in `browser/context.go`: on re-attach to a running
browser, the wait required **every** pre-existing top-level page to register,
and a leftover `chrome://newtab/` page (whose `createPage` never completes) made
it burn the full timeout on every connect.

**Fix.** `waitForInitialTopLevelTargets` now returns as soon as **at least one**
top-level page registers (a usable page is all bootstrap needs; late tabs still
surface via `Pages()`, which re-queries). **Per-command latency dropped 3.00s →
0.03s** (100×) with no correctness change. This also sped the library/connect
path's worst case.

## CLI daemon mode (persistent CDP connection)

`midas -s=NAME daemon` (run in the background) holds one CDP connection for the
session and serves subsequent commands over a unix socket
(`cmd/midas/daemon.go`); `dispatch` auto-routes to it when present, else falls
back to the stateless attach. `close` stops it. Two payoffs:

- **Speed**: ~0.00s/command (no re-attach). The win is largest for **remote
  browsers** (kernel/Browserbase/dev-tunnel), where a per-command CDP dial costs
  network round-trips.
- **Cross-command state**: the active tab, **keys held via keydown/keyup**,
  dialog listeners, and the humanized cursor persist across separate CLI
  invocations — the stateless CLI loses all of these between commands. Verified:
  `keydown Control` / `press a` / `keyup Control` as three separate processes
  selects the whole field (`TestCLIDaemonServesCommands`).

**For polymux:** if it drives midas as a **library** (`tools.BoundService` over a
long-lived `browser.Context`), it already has both — persistent connection and
state — at ~0.05s/op, no daemon needed (this is the recommended path). The daemon
is for the **CLI** integration path, especially against remote browsers.

## Gap-closing — CLI + tool parity for polymux's agent surface

The comparison's UNSUP column mapped exactly to polymux agent tools. Wired the
existing library primitives through:

- **midas CLI**: added `check`, `uncheck`, `select <sel> <val…>`, `drag <from>
  <to>` (`cmd/midas/cmds.go`).
- **tools registry** (the `tools.BoundService` polymux seam): added `check`,
  `uncheck`, `select` (`drag_and_drop`, `key_down`/`key_up` already present).
- **single-call key combos**: `KeyPress`/CLI `press`/`keys` now parse
  `"Control+a"`-style combos in one call (`parseKeyCombo`, `browser/page_api.go`),
  so combos work even on the stateless CLI where cross-process keydown/keyup
  state can't.

---

## Bug 0 — Context re-entrant mutex deadlock — FIXED

**Symptom.** The first e2e test passed; every subsequent test's `NewPage`
hung until its 15s context timeout, and the whole binary eventually hit Go's
10-minute test timeout. No prior test caught this because no prior test ever
launched a real browser and created a page over CDP — the entire existing suite
runs against fake CDP sessions.

**Root cause.** `browser/context.go`:
`cleanupByTarget` acquires `c.mu`, then called `removeFromOrder`, which acquired
`c.mu` *again*. `sync.Mutex` is non-recursive → self-deadlock. Because
`cleanupByTarget` runs on the CDP **readLoop** goroutine (events are dispatched
synchronously), the readLoop wedged the first time any target detached — i.e.
the first time a page closed — and from then on no CDP response was ever
delivered, stalling all subsequent calls.

**Fix.** Call the already-locked variant `removeFromOrderLocked` from
`cleanupByTarget`, and delete the unused `removeFromOrder` locking wrapper (its
only caller already held the lock; leaving it is a landmine for the same bug).

**Follow-up:** see Bug F — the underlying "event handler blocks the readLoop"
class is now eliminated structurally.

---

## Bug F — readLoop event-dispatch deadlock (the class behind Bug 0) — FIXED

**Symptom.** Loading any page with an iframe hung: `Page.Goto`'s
`waitForMainLoadState(domcontentloaded)` timed out and the connection wedged,
so every subsequent `NewPage` timed out. Surfaced the moment iframe coverage was
added (`e2e/frame_test.go`). Critical in practice — polymux agents navigate
iframe-bearing pages constantly, and midas launches with `--site-per-process`,
so even same-origin iframes become OOPIFs with their own attached targets.

**Root cause.** `cdp.Conn` dispatched CDP **events synchronously on the readLoop
goroutine**. `installFrameEventBridges` (`browser/context.go`) registers a frame
event handler that calls `refreshFrameOwnerMetadata`, which issues a synchronous
CDP `Send`. Running on the readLoop, that `Send` blocks waiting for a response
that only the readLoop can read → deadlock. This is the same class as Bug 0 (and
the same trap the dialog API hit): *any* event handler that does a blocking
`Send` wedges the whole connection.

**Fix (structural, eliminates the class).** `cdp.Conn` now delivers events on a
dedicated worker goroutine (`eventLoop`) fed by an unbounded FIFO queue
(`enqueueEvent`), instead of calling handlers on the readLoop
(`internal/cdp/conn.go`). Command **responses** are still processed inline on the
readLoop (non-blocking — they just close channels), so while a handler is parked
in a `Send`, the readLoop keeps delivering that `Send`'s response and the worker
unblocks. Ordering is preserved (single FIFO worker). Internal bookkeeping
(session-map updates, inflight cancellation on detach) stays on the readLoop for
promptness. With this in place, the dialog listener can call `Accept`/`Dismiss`
directly (no goroutine dance), and the Bug 0 lock-discipline hazard is moot.
Verified race-clean (`go test ./cdp -race`, dispatch-heavy e2e `-race`).

## Bugs A–E — actionability / input defects — FIXED

Each was pinned by an executable spec in `e2e/`. They now pass and enforce.

### A — `click-enabled`: click ignored the `enabled` actionability check — FIXED
`awaitActionable` checked visible / geometry / stability / occlusion but never
`enabled`, so clicking a disabled control neither waited nor errored — it
dispatched mouse events the browser silently ignored and returned success.
**Fix:** added `RequireEnabled` to `ActionabilityOptions`
(`browser/actionability.go`); `clickAtGeometry` (`browser/locator.go`) sets it,
so click/dblclick now wait for the control to become enabled and time out
(no side effect) if it never does. Hover deliberately does **not** set it,
matching Playwright's matrix.
Tests: `TestClickWaitsForDisabledButtonToEnable`,
`TestClickDisabledButtonTimesOutWithoutSideEffects`.

### B — `fill-no-retry` / `fill-no-visible`: Fill had no actionability loop — FIXED
`Locator.Fill` resolved once and checked `IsEditable()` once, with no retry loop
and no visibility check — a temporarily readonly field failed instantly, and a
`display:none` field was filled anyway. **Fix:** `Locator.Fill` now routes
through `awaitActionable` with `RequireEditable` + `SkipStability` +
`SkipOcclusion` (`browser/locator.go`), so it waits for visible + editable with
the standard poll/timeout. Added the matching `RequireEditable` check to the
loop (`browser/actionability.go`).
Tests: `TestFillWaitsForReadonlyToClear`, `TestFillTimesOutOnHiddenElement`.

### C — `fill-contenteditable`: Fill was a no-op on contenteditable — FIXED
The fill body assigned only `this.value`, guarded by `"value" in this`; a
`contenteditable` element has no `value`. **Fix:** added an `isContentEditable`
branch that sets `textContent`, in both the inline `Locator.Fill` body
(`browser/locator.go`) and the shared `HelperFillElementValue`
(`browser/dom/invocation.go`, used by the cache/replay `fill_form` path).
Test: `TestFillContentEditable`.

### D — `keypress-text`: KeyPress inserted no character for printable keys — FIXED
`Page.KeyPress` sent `{type:"keyDown"/"keyUp", key}` with no `text` for keys not
in `namedKeyCDP`, so Chrome fired keydown/keyup but inserted nothing. **Fix:**
for a single-rune key, populate `text`/`unmodifiedText` on the keyDown
(`browser/page_api.go`) so Chrome emits keypress/input and inserts the char.
Test: `TestPressNamedKeys`.

### E — `shadow-occlusion`: clicking open-shadow-DOM elements timed out — FIXED
The occlusion check used `document.elementFromPoint`, which returns the shadow
**host** for an element inside a shadow root; the host was then treated as an
occluder (`Node.contains` doesn't cross shadow boundaries), so every poll failed.
**Fix:** the hit-test now descends through shadow roots — open via
`el.shadowRoot`, closed via the piercer's `window.__stagehandV3__.getClosedRoot`
registry — resolving to the real element under the point
(`browser/actionability.go`).
Test: `TestClickShadowDOMButton`.

---

## Bug G — `midas open` browser dies instantly — FIXED

**Symptom.** The CLI's whole model is `open` (launch a persistent browser) then
separate `goto`/`click`/`eval`/… invocations that attach to it. Every command
after `open` failed with `connection refused` — the browser was already dead.

**Root cause.** `LaunchLocalChrome` uses `exec.CommandContext(ctx, …)`, which
kills the child process when `ctx` is cancelled. `cmd/midas`'s `cmdOpen` passed
`main`'s signal context, which is cancelled the moment the CLI process exits —
so chromium was reaped immediately, despite `open` disabling the crash-cleanup
supervisor and signal handler.

**Fix.** `cmdOpen` now launches with `context.Background()` (`cmd/midas/cmds.go`)
so the persistent browser is not bound to the command's lifetime. (Same root
cause the e2e harness had worked around.)
Test: `TestCLILifecycle` (+ `TestCLIHelp`, `TestCLIListEmpty`,
`TestCLIErrorOnMissingSession`).

## Bug H — DragAndDrop clicked the source instead of dragging — FIXED

**Symptom.** `Page.DragAndDrop` (and the `drag_and_drop` tool) never completed a
drop — pointer-based draggables saw press+release at the *source*, not a drag to
the target.

**Root cause.** `DragAndDrop` called `Page.Click` at the source, which presses
**and releases** the button, then "moved with the button held" (nothing was
held) and released again at the target. So: click-at-source → phantom move →
stray release. Not a drag.

**Fix.** `Page.DragAndDrop` (`browser/page_api.go`) now does the correct
sequence: move to source → `mousePressed` (down only) → move to target with the
button held → `mouseReleased` at target.
Test: `TestDragAndDropTool`.

## Bug I — keyboard modifier combos didn't work — FIXED

**Symptom.** polymux's agent exposes `keydown`/`keyup` tools "for modifier keys
like Shift, Control, Alt, Meta", composed with key presses. But there was no
`Page.KeyDown`/`KeyUp` (only `KeyPress`), no modifier-state tracking, and the
CLI's keydown/keyup sent bare `Input.dispatchKeyEvent` with no modifier bitmask
and no virtual key code. So `keydown Control → press a → keyup Control` produced
an `a` with `modifiers:0` — Chrome never saw Control+A. Select-all, copy/paste,
Shift-extend-selection, etc. were all broken.

**Fix.** Added `Page.KeyDown`/`KeyUp` (`browser/page_api.go`) that maintain a
`heldModifiers` bitmask on the Page; `KeyPress`/`KeyDown` now apply it and set
the `code` + `windowsVirtualKeyCode` for printable keys (Chrome resolves
shortcuts off the VK, not the `key` field). Exposed as `key_down`/`key_up` tools
(`tools/`) — the polymux integration surface — and wired the CLI's keydown/keyup
to the new methods. **Scope note:** modifier state lives on the `Page`, so combos
work on the **library / `tools.BoundService`** path where polymux holds a live
browser (the documented integration). The stateless CLI recreates the Page per
invocation, so cross-process `keydown; press; keyup` can't carry state — a CLI
architecture limit (it would need the modifier state persisted to the session
file, like the humanize toggle), noted but not necessary for the tools path.
Tests: `TestModifierControlASelectsAll`, `TestModifierShiftArrowExtendsSelection`,
`TestModifierComboViaTools`, `TestKeyToolsRegistered`.

---

## What the suite confirms works (101 tests)

Navigation (goto, 302 redirects, 404-resolves, data: URLs, back/forward, reload,
cross-origin re-drive, load-state waits, context-cancel abort); click
actionability (scroll-into-view, transient-overlay wait, disabled-wait/timeout,
hidden→visible wait, dblclick, coordinate click, shadow-DOM click, hover);
fill/type/inputValue on inputs, textareas, and contenteditable, value
replacement, clearing, readonly-wait, hidden-rejection, input/change events;
keyboard (Enter-submits, printable keys insert text, Backspace, Type);
**WaitForSelector** states (attached/visible/hidden/detached, late-appear,
timeout); **locator misc** (count, nth/first, check/uncheck/idempotent-check,
radio, selectOption by value+label, text/innerText/innerHTML, boundingBox,
isVisible); **dialogs** (alert capture, confirm accept/dismiss, prompt text,
click-triggered); **iframes/OOPIF** (FrameLocator read/click/fill across a
frame, page.Frames/ChildFrames); **screenshots** (PNG, full-page, element clip,
region clip); **cookies** (add/read, page-visible, clear); the snapshot a11y
tree + XPathMap/URLMap contract, **ref→click and ref→fill round-trips** (the
agent loop), `ResolveXPathForLocation`, shadow-piercing snapshots; **file
upload** (`SetInputFiles`, single + multiple); **drag-and-drop** (pointer-based,
via the tool); **multi-`select`**; **AddInitScript** (runs on new document);
**cache replay** against a live page (action sequence, variable interpolation,
attr-match self-heal); the **`cmd/midas` CLI** end-to-end (open→goto→eval→fill→
snapshot→list→close, plus help / empty-list / missing-session errors); and the
entire `tools.BoundService` polymux surface (specs +
go_to/click/type/fill_form/extract/aria_tree/screenshot/wait/scroll/think and
error paths) — which had **zero** prior coverage.

---

## Remaining follow-ups (not blocking)

Scoped against polymux's actual agent tool surface (navigate, click, mouse*,
fill, type, press, select, dblclick, drag, hover, check, uncheck, upload,
go_back/forward, reload, dialog_accept/dismiss, **keydown/keyup**, eval, scroll,
screenshot, snapshot). Keyboard modifiers (keydown/keyup) **were** in that
surface → implemented (Bug I). The rest below are **library gaps polymux does
not currently use**, left as deliberate non-goals:

- **HTML5 native drag-and-drop** (`draggable=true` dragstart/drop) — synthetic
  mouse events don't trigger the DnD API; would need `Input.dispatchDragEvent`.
  Pointer/mouse-based draggables work (Bug H fix), which covers polymux's `drag`.
- **Network interception/routing** — not in polymux's tool surface; no public
  Route API on `NetworkManager`.
- **storage-state save/load bundle** — not in polymux's tool surface; cookies
  work, but there's no aggregate localStorage+cookies bundle.
- **CLI cross-process modifier state** — the stateless CLI can't carry held
  modifiers across separate `keydown`/`press`/`keyup` invocations (would need
  session-file persistence). The `tools.BoundService` path polymux uses has no
  such limit.

When a future regression test pins a new defect, use the `knownBug(t, ...)`
convention (see `e2e/README.md`) so the suite stays green while the bug is
tracked, then drop the line once fixed.
