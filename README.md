# internal

## Purpose

`internal/` contains the starter implementation of a native browser stack intended to replace the current Playwright-based path over time.

It is a high-level porting effort from Stagehand v3 concepts into this repo, not the primary runtime path used by `main.go` today.

## What Lives Here

- `browser/`: CDP-backed browser, page, frame, locator, snapshot, and network primitives
- `cdp/`: low-level CDP connection and session handling
- `launch/`: local and kernel browser launch flows
- `session/`: internal browser session wrapper
- `tools/`: tool registry bound to the internal browser context
- `cache/`: cache and self-heal helpers
- `debug/`: debug logging utilities

## Current Status

Use this directory as the emerging replacement path for browser automation internals. The repo still wires the top-level runtime through `browser/`, not through `internal/` directly.
