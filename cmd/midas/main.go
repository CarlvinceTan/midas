// midas-cli is a thin command-line front-end for the midas browser library.
// It is shaped to overlap with playwright-cli where possible — same command
// names and argument order — so workflows wired against playwright-cli can
// be exercised against the midas runtime with a one-binary substitution.
//
// Sessions persist as JSON files under $TMPDIR/midas-sessions; non-lifecycle
// commands attach to a previously-launched Chromium via CDP, run, and exit.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
)

const usage = `midas-cli — drive the midas browser library from the terminal

Usage: midas [-s=<session>] <command> [args]

Lifecycle:
  open [url] [--headed]       launch a Chromium and store its WS URL
  attach <ws-url>             register an externally-launched Chromium
  close                       close the active session
  list                        list known sessions
  close-all                   close every known session

Navigation:
  goto <url>                  navigate the active page
  go-back                     go back in history
  go-forward                  go forward in history
  reload                      reload the active page

Mouse:
  click <selector> [--double] perform a click (humanized when --human is set)
  hover <selector>            hover over an element
  mousemove <x> <y>           move the mouse
  mousewheel <dx> <dy>        dispatch a wheel event

Keyboard:
  type <text>                 type into the focused element
  fill <selector> <text>      focus + set value via DOM
  press <key>                 press a single key (Enter, Escape, …)
  keydown <key>               press a key down
  keyup <key>                 release a key

Capture:
  snapshot                    print the accessibility tree
  screenshot [--filename=p]   capture screenshot (PNG)

Eval / wait:
  eval <expression>           run JS, print returned JSON
  wait-for <selector> [--timeout-ms=N]
  wait <duration-ms>          sleep N milliseconds

Storage:
  delete-data                 wipe cookies, HTTP cache, and per-origin web
                              storage (localStorage, sessionStorage, IndexedDB,
                              service workers) across all open tabs

Humanize (per-session toggle, persisted in session file):
  humanize on|off|careful     enable / disable humanized input

Global flags:
  -s=<session> | --session=<session>   session name (default: "default")
  --human                              one-shot humanize for this command

Pass --help to print this message.`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}

	args, opts := parseGlobalArgs(os.Args[1:])
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}

	cmd := args[0]
	rest := args[1:]

	if cmd == "help" || cmd == "--help" || cmd == "-h" {
		fmt.Println(usage)
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := dispatch(ctx, opts, cmd, rest); err != nil {
		if errors.Is(err, context.Canceled) {
			os.Exit(130)
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

type globalOpts struct {
	Session string
	Human   bool
}

// parseGlobalArgs strips global flags (-s=..., --session=..., --human) from
// args, returning the remaining positional args + parsed opts. Order is
// preserved so subcommand-local flags reach the dispatcher untouched.
func parseGlobalArgs(in []string) ([]string, globalOpts) {
	opts := globalOpts{Session: "default"}
	out := make([]string, 0, len(in))
	for _, a := range in {
		switch {
		case strings.HasPrefix(a, "-s="):
			opts.Session = strings.TrimPrefix(a, "-s=")
		case strings.HasPrefix(a, "--session="):
			opts.Session = strings.TrimPrefix(a, "--session=")
		case a == "--human":
			opts.Human = true
		default:
			out = append(out, a)
		}
	}
	return out, opts
}
