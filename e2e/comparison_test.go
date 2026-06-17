package e2e

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// TestCrossToolComparison drives midas, playwright-cli, and Vercel's
// agent-browser through the same battery of website scenarios and builds a
// correctness/capability matrix. Each tool runs via its CLI against identical
// local fixtures; the outcome of every scenario is verified through that tool's
// own `eval` so the check is engine-agnostic.
//
// Opt-in (it spawns three real browsers and shells out hundreds of times):
//
//	MIDAS_COMPARE=1 go test ./e2e/ -run TestCrossToolComparison -v -timeout 600s
//
// Requires playwright-cli and agent-browser on PATH (plus a Chromium for midas).
func TestCrossToolComparison(t *testing.T) {
	if os.Getenv("MIDAS_COMPARE") == "" {
		t.Skip("set MIDAS_COMPARE=1 to run the cross-tool comparison")
	}
	chrome := resolveChromePath()
	if chrome == "" {
		t.Skip("no Chromium binary for midas")
	}
	requireOnPath(t, "playwright-cli")
	requireOnPath(t, "agent-browser")

	server := newFixtureServer()
	defer server.Close()
	base := server.URL

	midasBin := midasBinary(t)
	abSocket, _ := os.MkdirTemp("", "ab-sock")
	defer os.RemoveAll(abSocket)
	midasSessions, _ := os.MkdirTemp("", "midas-cmp")
	defer os.RemoveAll(midasSessions)

	tools := []*cliTool{
		{
			label:     "midas",
			bin:       midasBin,
			env:       []string{"MIDAS_SESSIONS_DIR=" + midasSessions, "MIDAS_CHROME_PATH=" + chrome},
			sessArgs:  []string{"-s=cmp"},
			session:   "cmp",
			startArgs: []string{"open"},
			stopArgs:  []string{"close"},
			gotoOp:    func(u string) []string { return []string{"goto", u} },
			evalArgs:  func(js string) []string { return []string{"eval", js} },
			parseEval: firstNonEmptyLine,
			translate: midasTranslate,
		},
		{
			// Same midas binary, but with a warm background daemon holding one
			// CDP connection for the session — the production deployment mode.
			// Isolates how much of the stateless CLI's latency is per-command
			// re-attach (paid by the "midas" row above) vs genuine work.
			label:      "midas-daemon",
			bin:        midasBin,
			env:        []string{"MIDAS_SESSIONS_DIR=" + midasSessions, "MIDAS_CHROME_PATH=" + chrome},
			sessArgs:   []string{"-s=cmpd"},
			session:    "cmpd",
			startArgs:  []string{"open"},
			daemonArgs: []string{"daemon"},
			stopArgs:   []string{"close"},
			gotoOp:     func(u string) []string { return []string{"goto", u} },
			evalArgs:   func(js string) []string { return []string{"eval", js} },
			parseEval:  firstNonEmptyLine,
			translate:  midasTranslate,
		},
		{
			label:     "playwright",
			bin:       "playwright-cli",
			dir:       "/home/polymux/code/polymux", // playwright-cli keeps session state under cwd/.playwright-cli
			sessArgs:  []string{"-s=cmp"},
			startArgs: []string{"open"},
			stopArgs:  []string{"close"},
			gotoOp:    func(u string) []string { return []string{"goto", u} },
			evalArgs:  func(js string) []string { return []string{"eval", "() => (" + js + ")"} },
			parseEval: playwrightResult,
			translate: playwrightTranslate,
		},
		{
			label:     "agent-browser",
			bin:       "agent-browser",
			env:       []string{"AGENT_BROWSER_SOCKET_DIR=" + abSocket},
			sessArgs:  []string{"--session", "cmp"},
			startArgs: nil, // agent-browser auto-launches on the first `open`
			stopArgs:  []string{"close", "--all"},
			gotoOp:    func(u string) []string { return []string{"open", u} },
			evalArgs:  func(js string) []string { return []string{"eval", js} },
			parseEval: firstNonEmptyLine,
			translate: abTranslate,
		},
	}

	scenarios := comparisonScenarios()

	// results[scenario][tool] = status
	results := make(map[string]map[string]string)
	timings := make(map[string]time.Duration)
	// sceneTimings[scenario][tool] = wall time for that scenario (nav + steps +
	// verify). Used to localise WHERE a tool spends time — e.g. to separate the
	// wait-dependent scenarios (quality) from uniform per-command overhead.
	sceneTimings := make(map[string]map[string]time.Duration)
	for _, s := range scenarios {
		results[s.name] = make(map[string]string)
		sceneTimings[s.name] = make(map[string]time.Duration)
	}

	for _, tl := range tools {
		t.Logf("=== running %s ===", tl.label)
		start := time.Now()
		openStart := time.Now()
		if len(tl.startArgs) > 0 {
			if _, err := tl.run(tl.startArgs...); err != nil {
				t.Logf("%s start failed: %v", tl.label, err)
			}
		}
		openMs := time.Since(openStart).Milliseconds()
		// Bring up a warm daemon if this tool uses one; per-command runs then
		// route to it automatically (the CLI dials the session socket).
		var daemonProc *exec.Cmd
		var daemonMs int64
		if len(tl.daemonArgs) > 0 {
			dStart := time.Now()
			dp, err := startBackgroundDaemon(tl, midasSessions)
			daemonMs = time.Since(dStart).Milliseconds()
			if err != nil {
				t.Logf("%s daemon start failed (falling back to stateless): %v", tl.label, err)
			} else {
				daemonProc = dp
			}
		}
		t.Logf("%s startup: open=%dms daemon-start=%dms", tl.label, openMs, daemonMs)
		for _, s := range scenarios {
			scStart := time.Now()
			results[s.name][tl.label] = runScenario(tl, base, s)
			sceneTimings[s.name][tl.label] = time.Since(scStart)
		}
		_, _ = tl.run(tl.stopArgs...)
		if daemonProc != nil {
			_ = daemonProc.Process.Kill()
			_, _ = daemonProc.Process.Wait()
		}
		timings[tl.label] = time.Since(start)
	}

	report := renderMatrix(scenarios, tools, results, timings)
	t.Log("\n" + report)
	out := "/home/polymux/code/polymux/midas/docs/tool_comparison.md"
	if err := os.WriteFile(out, []byte(report), 0o644); err != nil {
		t.Logf("could not write report: %v", err)
	} else {
		t.Logf("wrote comparison report to %s", out)
	}

	// Per-scenario timing dump (log only) to localise where time goes.
	var tb strings.Builder
	tb.WriteString("\nper-scenario ms (nav+steps+verify):\n")
	fmt.Fprintf(&tb, "%-24s", "scenario")
	for _, tl := range tools {
		fmt.Fprintf(&tb, " %14s", tl.label)
	}
	tb.WriteString("  result(md/ab)\n")
	for _, s := range scenarios {
		fmt.Fprintf(&tb, "%-24s", s.name)
		for _, tl := range tools {
			fmt.Fprintf(&tb, " %12dms", sceneTimings[s.name][tl.label].Milliseconds())
		}
		fmt.Fprintf(&tb, "  %s/%s\n", results[s.name]["midas-daemon"], results[s.name]["agent-browser"])
	}
	t.Log(tb.String())
}

// ---- scenario model ----

type cmpStep struct {
	op   string
	args []string
}

type cmpScenario struct {
	name   string
	path   string
	steps  []cmpStep
	verify string // JS boolean predicate, evaluated through each tool
}

func runScenario(tl *cliTool, base string, s cmpScenario) string {
	if _, err := tl.run(tl.gotoOp(base + s.path)...); err != nil {
		return "ERR(nav)"
	}
	for _, st := range s.steps {
		args, ok := tl.translate(st.op, st.args)
		if !ok {
			return "UNSUP"
		}
		if _, err := tl.run(args...); err != nil {
			return "FAIL(" + st.op + ")"
		}
	}
	val, err := tl.evalBool(s.verify)
	if err != nil {
		return "ERR(eval)"
	}
	if val {
		return "PASS"
	}
	return "FAIL"
}

func comparisonScenarios() []cmpScenario {
	return []cmpScenario{
		{"nav-title", "/button.html", nil, `document.title==='Button test'`},
		{"click", "/button.html", []cmpStep{{"click", []string{"button"}}}, `window.result==='Clicked'`},
		{"fill", "/form.html", []cmpStep{{"fill", []string{"#username", "filled"}}}, `document.querySelector('#username').value==='filled'`},
		{"form-submit", "/form.html", []cmpStep{
			{"fill", []string{"#username", "u"}},
			{"fill", []string{"#email", "e@x.com"}},
			{"click", []string{"#submit"}},
		}, `document.getElementById('status').textContent.indexOf('submitted:')===0`},
		{"dblclick", "/compare/dblclick.html", []cmpStep{{"dblclick", []string{"#btn"}}}, `document.getElementById('result').textContent==='1'`},
		{"checkbox-check", "/checkbox.html", []cmpStep{{"check", []string{"#check"}}}, `document.getElementById('check').checked===true`},
		{"checkbox-uncheck", "/checkbox.html", []cmpStep{{"uncheck", []string{"#checked-box"}}}, `document.getElementById('checked-box').checked===false`},
		{"radio", "/checkbox.html", []cmpStep{{"check", []string{"#radio2"}}}, `document.getElementById('radio2').checked===true`},
		{"select-by-value", "/select.html", []cmpStep{{"select", []string{"#single", "banana"}}}, `document.getElementById('single').value==='banana'`},
		{"select-by-label", "/select.html", []cmpStep{{"select", []string{"#single", "Cherry"}}}, `document.getElementById('single').value==='cherry'`},
		{"hover", "/compare/hover.html", []cmpStep{{"hover", []string{"#btn"}}}, `document.getElementById('result').textContent==='yes'`},
		{"shadow-click", "/shadow.html", []cmpStep{{"click", []string{"#shadow-button"}}}, `window.result==='Shadow clicked'`},
		{"dynamic-wait-click", "/compare/delayed.html", []cmpStep{{"click", []string{"#late"}}}, `document.getElementById('result').textContent==='late clicked'`},
		{"scroll-into-view-click", "/scrollable.html", []cmpStep{{"click", []string{"#bottom-button"}}}, `window.result==='Bottom clicked'`},
		{"disabled-then-enabled", "/disabled.html?enable", []cmpStep{{"click", []string{"#btn"}}}, `window.result==='Clicked'`},
		{"overlay-clears", "/overlay.html?temp", []cmpStep{{"click", []string{"#target"}}}, `window.result==='Clicked'`},
		{"keycombo-select-all", "/textarea.html", []cmpStep{
			{"fill", []string{"#input", "select me"}},
			{"click", []string{"#input"}},
			{"keycombo", []string{"Control+a"}},
		}, `(()=>{const el=document.getElementById('input');return el.value.length>0 && (el.selectionEnd-el.selectionStart)===el.value.length})()`},
		{"drag-drop", "/compare/dnd.html", []cmpStep{{"drag", []string{"#src", "#dst"}}}, `document.getElementById('result').textContent==='dropped'`},
		{"contenteditable-fill", "/textarea.html", []cmpStep{{"fill", []string{"#editable", "rich"}}}, `document.getElementById('editable').textContent==='rich'`},
	}
}

// ---- per-tool op translation ----

func midasTranslate(op string, a []string) ([]string, bool) {
	switch op {
	case "click", "dblclick", "hover", "check", "uncheck":
		return []string{op, a[0]}, true
	case "fill":
		return []string{"fill", a[0], a[1]}, true
	case "select":
		return []string{"select", a[0], a[1]}, true
	case "drag":
		return []string{"drag", a[0], a[1]}, true
	case "keycombo":
		return []string{"press", a[0]}, true // KeyPress parses "Control+a"
	}
	return nil, false
}

func playwrightTranslate(op string, a []string) ([]string, bool) {
	switch op {
	case "click", "dblclick", "hover", "check", "uncheck":
		return []string{op, a[0]}, true
	case "fill":
		return []string{"fill", a[0], a[1]}, true
	case "select":
		return []string{"select", a[0], a[1]}, true
	case "drag":
		return []string{"drag", a[0], a[1]}, true
	case "keycombo":
		return []string{"press", a[0]}, true
	}
	return nil, false
}

func abTranslate(op string, a []string) ([]string, bool) {
	switch op {
	case "click", "dblclick", "hover", "check", "uncheck":
		return []string{op, a[0]}, true
	case "fill":
		return []string{"fill", a[0], a[1]}, true
	case "select":
		return []string{"select", a[0], a[1]}, true
	case "drag":
		return []string{"drag", a[0], a[1]}, true
	case "keycombo":
		return []string{"press", a[0]}, true
	}
	return nil, false
}

// ---- CLI tool adapter ----

type cliTool struct {
	label     string
	bin       string
	dir       string
	env       []string
	sessArgs  []string
	startArgs []string
	stopArgs  []string
	// daemonArgs, when set, makes the harness launch a background daemon for
	// this tool after startArgs (e.g. midas's `daemon` command). Subsequent
	// per-command invocations then route to the warm daemon instead of paying a
	// fresh attach each time. session is the bare session name used to locate
	// the daemon's unix socket under MIDAS_SESSIONS_DIR.
	daemonArgs []string
	session    string
	gotoOp     func(url string) []string
	evalArgs   func(js string) []string
	parseEval  func(out string) string
	translate  func(op string, args []string) ([]string, bool)
}

func (tl *cliTool) run(args ...string) (string, error) {
	full := append(append([]string{}, tl.sessArgs...), args...)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, tl.bin, full...)
	if tl.dir != "" {
		cmd.Dir = tl.dir
	}
	cmd.Env = append(os.Environ(), tl.env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// startBackgroundDaemon launches the tool's daemon as a detached process and
// waits for its unix socket to appear (so subsequent per-command runs route to
// it). The caller is responsible for killing the returned process.
func startBackgroundDaemon(tl *cliTool, sessionsDir string) (*exec.Cmd, error) {
	full := append(append([]string{}, tl.sessArgs...), tl.daemonArgs...)
	cmd := exec.Command(tl.bin, full...)
	if tl.dir != "" {
		cmd.Dir = tl.dir
	}
	cmd.Env = append(os.Environ(), tl.env...)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start daemon: %w", err)
	}
	sock := filepath.Join(sessionsDir, tl.session+".sock")
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(sock); err == nil {
			return cmd, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()
	return nil, fmt.Errorf("daemon socket %s not ready after 15s", sock)
}

func (tl *cliTool) evalBool(js string) (bool, error) {
	out, err := tl.run(tl.evalArgs(js)...)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(tl.parseEval(out)) == "true", nil
}

// ---- output parsers ----

func firstNonEmptyLine(out string) string {
	for _, line := range strings.Split(out, "\n") {
		if s := strings.TrimSpace(line); s != "" {
			return s
		}
	}
	return ""
}

func playwrightResult(out string) string {
	lines := strings.Split(out, "\n")
	for i, line := range lines {
		if strings.TrimSpace(line) == "### Result" && i+1 < len(lines) {
			return strings.TrimSpace(lines[i+1])
		}
	}
	return ""
}

// ---- reporting ----

func requireOnPath(t *testing.T, bin string) {
	t.Helper()
	if _, err := exec.LookPath(bin); err != nil {
		t.Skipf("%s not on PATH; skipping comparison", bin)
	}
}

func renderMatrix(scenarios []cmpScenario, tools []*cliTool, results map[string]map[string]string, timings map[string]time.Duration) string {
	var b strings.Builder
	b.WriteString("# Cross-tool browser-automation comparison\n\n")
	b.WriteString("midas vs playwright-cli vs Vercel agent-browser, identical fixtures, ")
	b.WriteString("each scenario verified through the tool's own `eval`.\n\n")
	b.WriteString("Legend: PASS = outcome verified · FAIL = ran but wrong result · ")
	b.WriteString("UNSUP = command not in that CLI · ERR = nav/eval/step error.\n\n")

	// header
	b.WriteString("| scenario |")
	for _, tl := range tools {
		b.WriteString(" " + tl.label + " |")
	}
	b.WriteString("\n|" + strings.Repeat("---|", len(tools)+1) + "\n")

	tally := make(map[string]map[string]int) // tool -> status -> count
	for _, tl := range tools {
		tally[tl.label] = make(map[string]int)
	}
	for _, s := range scenarios {
		b.WriteString("| " + s.name + " |")
		for _, tl := range tools {
			st := results[s.name][tl.label]
			b.WriteString(" " + st + " |")
			tally[tl.label][bucket(st)]++
		}
		b.WriteString("\n")
	}

	// summary
	b.WriteString("\n## Summary\n\n")
	b.WriteString("| tool | PASS | FAIL | UNSUP | ERR | total time |\n|---|---|---|---|---|---|\n")
	for _, tl := range tools {
		tg := tally[tl.label]
		b.WriteString(fmt.Sprintf("| %s | %d | %d | %d | %d | %s |\n",
			tl.label, tg["PASS"], tg["FAIL"], tg["UNSUP"], tg["ERR"], timings[tl.label].Round(time.Millisecond)))
	}

	// disagreements
	var diffs []string
	for _, s := range scenarios {
		row := results[s.name]
		set := map[string]bool{}
		for _, tl := range tools {
			set[bucket(row[tl.label])] = true
		}
		if len(set) > 1 {
			parts := make([]string, 0, len(tools))
			for _, tl := range tools {
				parts = append(parts, tl.label+"="+row[tl.label])
			}
			diffs = append(diffs, "- "+s.name+": "+strings.Join(parts, ", "))
		}
	}
	sort.Strings(diffs)
	b.WriteString("\n## Scenarios where the tools diverge\n\n")
	if len(diffs) == 0 {
		b.WriteString("_none — all three behaved identically._\n")
	} else {
		b.WriteString(strings.Join(diffs, "\n") + "\n")
	}
	return b.String()
}

func bucket(status string) string {
	switch {
	case status == "PASS":
		return "PASS"
	case status == "UNSUP":
		return "UNSUP"
	case strings.HasPrefix(status, "ERR"):
		return "ERR"
	default:
		return "FAIL"
	}
}
