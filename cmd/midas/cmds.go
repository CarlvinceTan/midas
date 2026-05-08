package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/PolymuxOrg/midas/browser"
	"github.com/PolymuxOrg/midas/humanize"
	"github.com/PolymuxOrg/midas/launch"
	"github.com/PolymuxOrg/midas/session"
)

func dispatch(ctx context.Context, opts globalOpts, cmd string, args []string) error {
	switch cmd {
	case "open":
		return cmdOpen(ctx, opts.Session, args)
	case "attach":
		return cmdAttach(ctx, opts.Session, args)
	case "close":
		return cmdClose(ctx, opts.Session)
	case "list":
		return cmdList()
	case "close-all":
		return cmdCloseAll(ctx)
	case "humanize":
		return cmdHumanizeToggle(opts.Session, args)
	}

	// Everything below requires an attached session.
	rec, err := loadSession(opts.Session)
	if err != nil {
		return err
	}
	sess, err := attachSession(ctx, rec)
	if err != nil {
		return err
	}
	defer sess.Close()

	page := sess.Context().ActivePage()
	if page == nil {
		// Some commands (goto, eval) implicitly need a page; create one.
		var err error
		page, err = sess.Context().NewPage(ctx, "about:blank")
		if err != nil {
			return fmt.Errorf("no active page and NewPage failed: %w", err)
		}
	}

	// Apply per-session humanize state, plus optional --human override.
	if rec.Name != "" {
		applyHumanizePref(page, rec, opts.Human)
	}

	switch cmd {
	case "goto":
		return cmdGoto(ctx, page, args)
	case "go-back":
		return cmdGoBack(ctx, page)
	case "go-forward":
		return cmdGoForward(ctx, page)
	case "reload":
		return cmdReload(ctx, page)
	case "click":
		return cmdClick(ctx, page, args)
	case "dblclick":
		return cmdDblClick(ctx, page, args)
	case "hover":
		return cmdHover(ctx, page, args)
	case "mousemove":
		return cmdMouseMove(ctx, page, args)
	case "mousewheel":
		return cmdMouseWheel(ctx, page, args)
	case "type":
		return cmdType(ctx, page, args)
	case "fill":
		return cmdFill(ctx, page, args)
	case "press":
		return cmdPress(ctx, page, args)
	case "keydown":
		return cmdKeyDown(ctx, page, args)
	case "keyup":
		return cmdKeyUp(ctx, page, args)
	case "snapshot":
		return cmdSnapshot(ctx, page)
	case "screenshot":
		return cmdScreenshot(ctx, page, args)
	case "eval":
		return cmdEval(ctx, page, args)
	case "wait":
		return cmdWait(ctx, args)
	case "wait-for":
		return cmdWaitFor(ctx, page, args)
	case "delete-data":
		return cmdDeleteData(ctx, sess.Context(), page)
	default:
		return fmt.Errorf("unknown command %q (try `midas --help`)", cmd)
	}
}

// ---------------------------------------------------------------- lifecycle

func cmdOpen(ctx context.Context, name string, args []string) error {
	url := ""
	headed := false
	for _, a := range args {
		switch {
		case a == "--headed":
			headed = true
		case !strings.HasPrefix(a, "-") && url == "":
			url = a
		}
	}

	headless := !headed
	// We want chromium to outlive this CLI invocation. Disable both the
	// signal-cleanup goroutine and the parent-PID-watching supervisor process
	// the launcher would otherwise spawn — both kill chromium on parent exit.
	disableCleanup := false
	opts := launch.LaunchLocalOptions{
		Headless:           &headless,
		HandleSignals:      &disableCleanup,
		EnableCrashCleanup: &disableCleanup,
	}
	if cp := os.Getenv("MIDAS_CHROME_PATH"); cp != "" {
		opts.ChromePath = cp
	}

	result, err := launch.LaunchLocalChrome(ctx, opts)
	if err != nil {
		return fmt.Errorf("launch browser: %w", err)
	}
	// Do NOT close result.Resource — leaving Chromium running is the whole
	// point of `open`. Subsequent commands reattach via the recorded WS URL.

	pid := 0
	userData := ""
	if result.Chrome != nil {
		userData = result.Chrome.UserDataDir
		if result.Chrome.Cmd != nil && result.Chrome.Cmd.Process != nil {
			pid = result.Chrome.Cmd.Process.Pid
		}
	}

	if url != "" {
		// Attach via CDP just for this invocation to perform the initial goto.
		sess, err := session.New(ctx, session.Options{
			WSURL:                   result.WS,
			EnsureFirstTopLevelPage: true,
		})
		if err != nil {
			return fmt.Errorf("attach to launched browser: %w", err)
		}
		// Closing only the CDP connection (not the chromium process) — it will
		// be reopened by the next command via attachSession.
		defer sess.Close()
		page := sess.Context().ActivePage()
		if page == nil {
			page, err = sess.Context().NewPage(ctx, url)
			if err != nil {
				return fmt.Errorf("open: NewPage: %w", err)
			}
		} else {
			if _, err := page.Goto(ctx, url); err != nil {
				return fmt.Errorf("open: goto %q: %w", url, err)
			}
		}
	}

	rec := SessionRecord{
		Name:      name,
		WSURL:     result.WS,
		ChromePID: pid,
		UserData:  userData,
		CreatedAt: time.Now(),
	}
	if err := saveSession(rec); err != nil {
		return err
	}
	fmt.Printf("opened session %q (ws=%s pid=%d)\n", name, result.WS, pid)
	return nil
}

func cmdAttach(_ context.Context, name string, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("attach requires a CDP WS URL argument")
	}
	rec := SessionRecord{
		Name:      name,
		WSURL:     args[0],
		CreatedAt: time.Now(),
	}
	if err := saveSession(rec); err != nil {
		return err
	}
	fmt.Printf("attached session %q (ws=%s)\n", name, rec.WSURL)
	return nil
}

func cmdClose(ctx context.Context, name string) error {
	rec, err := loadSession(name)
	if err != nil {
		return err
	}
	if sess, attachErr := attachSession(ctx, rec); attachErr == nil {
		// Polite-close all pages, then drop the CDP connection. The chromium
		// process is reaped by SIGTERM below using the recorded PID.
		for _, p := range sess.Context().Pages() {
			_ = p.Close(ctx)
		}
		_ = sess.Close()
	}
	if rec.ChromePID > 0 {
		// Best-effort cleanup of the launcher we spawned.
		if proc, perr := os.FindProcess(rec.ChromePID); perr == nil {
			_ = proc.Signal(syscall.SIGTERM)
		}
	}
	if err := deleteSession(name); err != nil {
		return err
	}
	fmt.Printf("closed session %q\n", name)
	return nil
}

func cmdList() error {
	recs, err := listSessions()
	if err != nil {
		return err
	}
	if len(recs) == 0 {
		fmt.Println("(no sessions)")
		return nil
	}
	for _, r := range recs {
		fmt.Printf("%-20s ws=%s pid=%d created=%s\n", r.Name, r.WSURL, r.ChromePID, r.CreatedAt.Format(time.RFC3339))
	}
	return nil
}

func cmdCloseAll(ctx context.Context) error {
	recs, err := listSessions()
	if err != nil {
		return err
	}
	for _, r := range recs {
		_ = cmdClose(ctx, r.Name)
	}
	return nil
}

// ---------------------------------------------------------------- nav

func cmdGoto(ctx context.Context, page *browser.Page, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("goto requires a URL")
	}
	resp, err := page.Goto(ctx, args[0])
	if err != nil {
		return err
	}
	if resp != nil {
		fmt.Printf("loaded %s status=%d\n", page.URL(), resp.Status())
	} else {
		fmt.Printf("loaded %s\n", page.URL())
	}
	return nil
}

func cmdGoBack(ctx context.Context, page *browser.Page) error {
	_, err := page.GoBack(ctx, browser.LoadStateDOMContentLoaded, 15*time.Second)
	return err
}

func cmdGoForward(ctx context.Context, page *browser.Page) error {
	_, err := page.GoForward(ctx, browser.LoadStateDOMContentLoaded, 15*time.Second)
	return err
}

func cmdReload(ctx context.Context, page *browser.Page) error {
	_, err := page.Reload(ctx)
	return err
}

// ---------------------------------------------------------------- mouse

func cmdClick(ctx context.Context, page *browser.Page, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("click requires a selector")
	}
	double := false
	selector := ""
	for _, a := range args {
		switch {
		case a == "--double":
			double = true
		case selector == "":
			selector = a
		}
	}
	if selector == "" {
		return fmt.Errorf("click requires a selector")
	}
	loc := page.Locator(selector)
	if double {
		return loc.DblClick(ctx)
	}
	return loc.Click(ctx)
}

func cmdDblClick(ctx context.Context, page *browser.Page, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("dblclick requires a selector")
	}
	return page.Locator(args[0]).DblClick(ctx)
}

func cmdHover(ctx context.Context, page *browser.Page, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("hover requires a selector")
	}
	return page.Locator(args[0]).Hover(ctx)
}

func cmdMouseMove(ctx context.Context, page *browser.Page, args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("mousemove requires <x> <y>")
	}
	x, err := strconv.ParseFloat(args[0], 64)
	if err != nil {
		return err
	}
	y, err := strconv.ParseFloat(args[1], 64)
	if err != nil {
		return err
	}
	return page.Hover(ctx, x, y)
}

func cmdMouseWheel(ctx context.Context, page *browser.Page, args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("mousewheel requires <dx> <dy>")
	}
	dx, err := strconv.ParseFloat(args[0], 64)
	if err != nil {
		return err
	}
	dy, err := strconv.ParseFloat(args[1], 64)
	if err != nil {
		return err
	}
	if page.HumanizeEnabled() {
		_, err := page.HumanizeScroll(ctx, dy)
		_ = dx
		return err
	}
	return page.Scroll(ctx, 0, 0, dx, dy)
}

// ---------------------------------------------------------------- keyboard

func cmdType(ctx context.Context, page *browser.Page, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("type requires text")
	}
	text := strings.Join(args, " ")
	if page.HumanizeEnabled() {
		// Humanize.Type doesn't focus on its own — caller is responsible.
		// We can't focus without a selector, so we rely on the page already
		// having focus on the right element.
		raw := page.HumanizeRawKeyboard()
		cfg := page.HumanizeConfig()
		if cfg == nil {
			return fmt.Errorf("humanize config unavailable")
		}
		return humanize.Type(ctx, page, raw, text, *cfg)
	}
	return page.Type(ctx, text, 0)
}

func cmdFill(ctx context.Context, page *browser.Page, args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("fill requires <selector> <text>")
	}
	return page.Locator(args[0]).Fill(ctx, strings.Join(args[1:], " "))
}

func cmdPress(ctx context.Context, page *browser.Page, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("press requires a key")
	}
	return page.KeyPress(ctx, args[0])
}

func cmdKeyDown(ctx context.Context, page *browser.Page, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("keydown requires a key")
	}
	return page.SendCDP(ctx, "Input.dispatchKeyEvent", map[string]any{
		"type": "keyDown",
		"key":  args[0],
	}, nil)
}

func cmdKeyUp(ctx context.Context, page *browser.Page, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("keyup requires a key")
	}
	return page.SendCDP(ctx, "Input.dispatchKeyEvent", map[string]any{
		"type": "keyUp",
		"key":  args[0],
	}, nil)
}

// ---------------------------------------------------------------- capture

func cmdSnapshot(ctx context.Context, page *browser.Page) error {
	res, err := page.Snapshot(ctx)
	if err != nil {
		return err
	}
	fmt.Println(res.FormattedTree)
	return nil
}

func cmdScreenshot(ctx context.Context, page *browser.Page, args []string) error {
	filename := ""
	for _, a := range args {
		if strings.HasPrefix(a, "--filename=") {
			filename = strings.TrimPrefix(a, "--filename=")
		}
	}
	data, err := page.Screenshot(ctx, browser.ScreenshotOptions{})
	if err != nil {
		return err
	}
	if filename == "" {
		_, err = os.Stdout.Write(data)
		return err
	}
	return os.WriteFile(filename, data, 0o644)
}

// ---------------------------------------------------------------- eval/wait

func cmdEval(ctx context.Context, page *browser.Page, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("eval requires an expression")
	}
	expr := strings.Join(args, " ")
	var raw any
	if err := page.Evaluate(ctx, expr, &raw); err != nil {
		return err
	}
	out, err := json.Marshal(raw)
	if err != nil {
		return err
	}
	fmt.Println(string(out))
	return nil
}

func cmdWait(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("wait requires <duration-ms>")
	}
	ms, err := strconv.Atoi(args[0])
	if err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(time.Duration(ms) * time.Millisecond):
		return nil
	}
}

func cmdWaitFor(ctx context.Context, page *browser.Page, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("wait-for requires a selector")
	}
	timeout := 30 * time.Second
	for _, a := range args[1:] {
		if strings.HasPrefix(a, "--timeout-ms=") {
			ms, err := strconv.Atoi(strings.TrimPrefix(a, "--timeout-ms="))
			if err != nil {
				return err
			}
			timeout = time.Duration(ms) * time.Millisecond
		}
	}
	ok, err := page.WaitForSelector(ctx, args[0], browser.WaitForSelectorOptions{Timeout: timeout})
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("selector %q not visible after %s", args[0], timeout)
	}
	return nil
}

// ---------------------------------------------------------------- storage

// cmdDeleteData wipes browsing data from the attached browser without
// restarting it. Three layers:
//
//   1. Browser-wide: Network.clearBrowserCookies + Network.clearBrowserCache
//   2. Per-origin persistent storage via Storage.clearDataForOrigin
//      (localStorage, IndexedDB, service workers, cache storage, etc.)
//      across every origin currently loaded in any tab
//   3. Per-page sessionStorage cleared via JS — CDP's Storage domain doesn't
//      cover sessionStorage because it's in-memory tab-scoped state, not a
//      persistent profile artifact
//
// `about:blank` / empty origins are skipped because Storage.clearDataForOrigin
// rejects them.
//
// This is the runtime equivalent of "wipe profile" — it does not delete the
// user-data-dir on disk. For that, close the session and rm -rf the recorded
// UserData path manually.
func cmdDeleteData(ctx context.Context, bctx *browser.Context, page *browser.Page) error {
	if err := page.SendCDP(ctx, "Network.clearBrowserCookies", nil, nil); err != nil {
		return fmt.Errorf("clear cookies: %w", err)
	}
	if err := page.SendCDP(ctx, "Network.clearBrowserCache", nil, nil); err != nil {
		return fmt.Errorf("clear cache: %w", err)
	}

	pages := bctx.Pages()

	origins := map[string]struct{}{}
	for _, p := range pages {
		var origin string
		if err := p.Evaluate(ctx, "location.origin", &origin); err != nil {
			continue
		}
		if origin == "" || origin == "null" {
			continue
		}
		origins[origin] = struct{}{}
	}

	cleared := 0
	for origin := range origins {
		err := page.SendCDP(ctx, "Storage.clearDataForOrigin", map[string]any{
			"origin":       origin,
			"storageTypes": "all",
		}, nil)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: clearDataForOrigin %s: %v\n", origin, err)
			continue
		}
		cleared++
	}

	// sessionStorage is in-memory and per-tab; clear it on every open page.
	for _, p := range pages {
		_ = p.Evaluate(ctx, "(() => { try { sessionStorage.clear(); } catch (_) {} return null; })()", nil)
	}

	fmt.Printf("cleared cookies + cache; cleared storage for %d origin(s) across %d tab(s)\n", cleared, len(pages))
	return nil
}

// ---------------------------------------------------------------- humanize

func cmdHumanizeToggle(name string, args []string) error {
	rec, err := loadSession(name)
	if err != nil {
		return err
	}
	mode := "on"
	if len(args) > 0 {
		mode = strings.ToLower(args[0])
	}
	rec.Humanize = mode
	if err := saveSession(rec); err != nil {
		return err
	}
	fmt.Printf("humanize=%s for session %q\n", mode, name)
	return nil
}

// applyHumanizePref enables / disables humanize on the page based on the
// session record. The --human one-shot flag overrides "off" upward to
// "default" but never downgrades.
func applyHumanizePref(page *browser.Page, rec SessionRecord, oneShot bool) {
	mode := rec.Humanize
	if oneShot && (mode == "" || mode == "off") {
		mode = "on"
	}
	switch mode {
	case "on", "default":
		cfg := humanize.DefaultConfig()
		page.EnableHumanize(&cfg)
	case "careful":
		cfg := humanize.CarefulConfig()
		page.EnableHumanize(&cfg)
	default:
		page.EnableHumanize(nil)
	}
}

// ---------------------------------------------------------------- attach

// attachSession opens a CDP connection to the already-running Chromium
// recorded in rec. Idempotent — closing the returned Session disconnects but
// does not kill the browser.
func attachSession(ctx context.Context, rec SessionRecord) (*session.Session, error) {
	if rec.WSURL == "" {
		return nil, fmt.Errorf("session %q has no WS URL", rec.Name)
	}
	return session.New(ctx, session.Options{
		WSURL:                   rec.WSURL,
		EnsureFirstTopLevelPage: true,
	})
}
