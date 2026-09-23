# Working on Midas

Native Go coding agent. `pkg/` is the reusable core (`ai`, `agent`), everything
Midas-specific is under `internal/`, and `cmd/` holds the two binaries: `midas`
(TUI) and `midas-server` (headless).
Do not edit this instruction file unless explicitly asked.

## Build and install

`midas` and `midas-server` on `PATH` are `~/.local/bin/*`, built from this
checkout, so a change is not finished until they are rebuilt:

```sh
go test ./... && go vet ./...
go build -o "$TMPDIR/midas" ./cmd/midas && mv -f "$TMPDIR/midas" "$HOME/.local/bin/midas"
go build -o "$TMPDIR/midas-server" ./cmd/server && mv -f "$TMPDIR/midas-server" "$HOME/.local/bin/midas-server"
```

- Never install a build whose tests have not run. `go install` writes to
  `$(go env GOPATH)/bin`, which `PATH` resolves *after* `~/.local/bin`, so it
  leaves the stale binary in place.
- Build to a temporary path and rename it over the installed binary: writing
  straight to `~/.local/bin/midas` truncates a running Midas and can kill that
  session.
- Rebuild after every later change and confirm `ls -l "$HOME/.local/bin/midas"`
  is newer than your last edit. A running Midas keeps the build it started with.
- `./midas` and `./cmd/midas/midas` are gitignored scratch builds, not the
  product.

## Verification

Match the check to the change: drive the real TUI for TUI work, make a real
request for provider or cache work, and update any test that no longer describes
current behavior.
