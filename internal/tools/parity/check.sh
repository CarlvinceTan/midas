#!/usr/bin/env bash
# Parity gate for the Go port.
#
# Verifies that every generated artefact is current and that the Go packages
# still reproduce the TypeScript reference byte for byte. Run this before
# trusting any Go-side TUI change.
#
#   tools/parity/check.sh          # check generated files, then test
#   tools/parity/check.sh --regen  # regenerate first, then check and test
set -euo pipefail

cd "$(dirname "$0")/../.."

if [[ "${1:-}" == "--regen" ]]; then
  echo "== regenerating =="
  node tools/parity/extract-pi-tui-sources.mjs
  node tools/parity/gen-unicode-tables.mjs
  node tools/parity/gen-text-goldens.mjs
  node tools/parity/gen-tui-core-goldens.mjs
  node tools/parity/gen-tui-main-screen-goldens.mjs
  node tools/parity/gen-tui-alt-screen-goldens.mjs
  node tools/parity/gen-key-goldens.mjs
fi

echo "== generated artefacts =="
node tools/parity/extract-pi-tui-sources.mjs --check
node tools/parity/gen-unicode-tables.mjs --check
node tools/parity/gen-text-goldens.mjs --check
node tools/parity/gen-tui-core-goldens.mjs --check
node tools/parity/gen-tui-main-screen-goldens.mjs --check
node tools/parity/gen-tui-alt-screen-goldens.mjs --check
node tools/parity/gen-key-goldens.mjs --check
node tools/parity/gen-tui-alt-screen-search-goldens.mjs --check
node tools/parity/gen-stdin-buffer-goldens.mjs --check
node tools/parity/gen-terminal-capabilities-goldens.mjs --check
node tools/parity/gen-process-terminal-goldens.mjs --check
node --import tsx tools/parity/gen-session-store-goldens.mjs --check
node --import tsx tools/parity/gen-transcript-goldens.mjs --check

# The vendored dist is upstream plus local patches. Recompiling the recovered
# sources and diffing proves the patch set has not changed, which is the one
# thing that could silently invalidate the port's source of truth.
echo "== vendored patch set =="
node tools/parity/find-dist-patches.mjs

echo "== go vet =="
go vet ./...

echo "== go test =="
go test ./...
