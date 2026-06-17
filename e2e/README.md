# midas e2e suite

Integration tests that drive midas against a **real headless Chromium**, modeled
on how Playwright, Stagehand v3, and Vercel's agent-browser test themselves, and
adapted to midas's CDP-native stack and its polymux/argus integration surface.

Until this package, every midas test ran against fake CDP sessions — no test ever
launched a browser and created a page over CDP. That gap hid a process-level
deadlock (see `docs/midas_findings.md`, bug 0) and a cluster of actionability
defects.

## Design

- **One browser per test binary.** `TestMain` + a lazy `requireHarness` launch a
  single Chromium; each test gets a fresh page via `newPage(t)` (closed on
  cleanup). The first test pays the ~13s launch; the rest run in tens of ms.
- **Local fixture server.** `fixtureServer` serves static pages from `assets/`
  and supports per-test dynamic routes — `SetContent`, `SetRoute`,
  `SetRedirect`, `WaitForRequest`, and a same-port `localhost`↔`127.0.0.1`
  `CrossOriginURL()` trick (ported from Playwright's `TestServer`).
- **Instrumented fixtures.** Pages record state into `window.result` / event
  logs that tests read back via `page.Evaluate` (Playwright's `button.html` /
  `keyboard.html` pattern).
- **Auto-skip when no browser.** Tests `t.Skip` if no Chromium binary is found.

## Running

```bash
cd /home/polymux/code/polymux/midas
go test ./e2e/                      # full suite
go test ./e2e/ -run TestClick -v    # one area
```

- `MIDAS_CHROME_PATH` (or `CHROMIUM_PATH`) — point at a specific binary; otherwise
  `google-chrome*` / `chromium*` on `PATH` is used.
- `MIDAS_E2E_HEADED=1` — watch the browser.
- `MIDAS_DEBUG_BROWSER=1` — verbose CDP launch/connect logging.
- `MIDAS_E2E_RUN_KNOWN_BUGS=1` — also run the documented-known-bug tests (to
  confirm a fix; they otherwise skip).

## Known-bug convention

Tests that document a confirmed, not-yet-fixed midas defect call
`knownBug(t, ref, detail)` on the first line. They **skip** so the suite stays
green as a regression baseline, while the test body is the executable spec for
the *desired* behavior. When the bug is fixed, delete the `knownBug(...)` line
and the test becomes enforcing.

List the backlog:

```bash
grep -rn "KNOWN-BUG\|knownBug(" e2e/
```

See `docs/midas_findings.md` for the full bug list with root causes.

## Coverage map

| File | Area |
|------|------|
| `harness_test.go` | browser harness, fixture server, helpers |
| `navigation_test.go` | goto / redirects / 404 / data: / back-forward / reload / cross-origin / load-state |
| `click_test.go` | click actionability: scroll-into-view, overlay wait, disabled, hidden, dblclick, coords, shadow, hover |
| `fill_test.go` | fill / type: inputs, textarea, contenteditable, readonly, replace, clear, events |
| `keyboard_test.go` | press / type: Enter-submits, printable keys, backspace, delay |
| `waitfor_test.go` | WaitForSelector states: attached/visible/hidden/detached, late-appear, timeout |
| `locator_misc_test.go` | count, nth/first, check/uncheck, radio, selectOption, text/html, boundingBox, isVisible |
| `dialog_test.go` | alert/confirm/prompt accept & dismiss, click-triggered |
| `frame_test.go` | iframe/OOPIF: FrameLocator read/click/fill, Frames/ChildFrames |
| `screenshot_test.go` | page PNG, full-page, element clip, region clip |
| `cookies_test.go` | add/read cookies, page visibility, clear |
| `interaction_extra_test.go` | file upload (single/multiple), drag-and-drop tool, multi-select, AddInitScript |
| `cache_replay_test.go` | cache replay against a live page: action sequence, variables, attr-match self-heal |
| `cli_test.go` | `cmd/midas` spawn-binary: open→goto→eval→fill→snapshot→list→close, help, errors |
| `cli_daemon_test.go` | daemon mode: routes commands over a unix socket, persists modifier state across separate CLI processes, `close` stops it |
| `keyboard_modifiers_test.go` | modifier combos (Control+A, Shift+ArrowRight) via library + `key_down`/`key_up` tools |
| `comparison_test.go` | **opt-in** (`MIDAS_COMPARE=1`) cross-tool matrix: midas vs playwright-cli vs agent-browser over 19 scenarios → `docs/tool_comparison.md` |
| `snapshot_test.go` | a11y tree shape, XPathMap/URLMap, ref→click/fill round-trip, ResolveXPathForLocation, shadow pierce |
| `tools_test.go` | `tools.BoundService` (polymux surface): specs, go_to/click/type/fill_form/extract/aria_tree/screenshot/wait/scroll/think + errors |

## Not covered — library gaps polymux doesn't use (deliberate non-goals)

HTML5 native drag-and-drop (`draggable=true`; needs `Input.dispatchDragEvent`;
pointer-based drag works) · network interception/routing (no public API) ·
storage-state save/load bundle (not implemented) · humanize timing.
