# Goal engine evidence: external plugin limits and restart behaviour

Status: **actual-engine proof complete; frontend bridge and manual/live acceptance
still pending.** This document does not claim `/goal` fully works end to end.

## What was tested

The real, published external engine — not a reimplementation — was imported through
its published export and exercised with a bounded fake OpenCode SDK/event adapter.

- Package: `@prevalentware/opencode-goal-plugin`
- Version: `0.1.48` (pinned exactly in `package.json`/`package-lock.json` as a dev
  dependency solely for this test)
- Entrypoint: `@prevalentware/opencode-goal-plugin/server` → `dist/server.js`
  (`exports["./server"]`)
- Real surfaces driven: V1 `server({ client }, options)` hooks/tools and V2
  `setup(context)` tools, commands, context hook, and event consumer.

The adapter only fakes the OpenCode SDK. All lifecycle, persistence, limit,
continuation, and scheduling behaviour is the plugin's own code.

## Artifact identity and version uncertainty

- Read-only audit found the global config references
  `@prevalentware/opencode-goal-plugin@0.1.48`
  (`~/.config/opencode/opencode.jsonc`) and a cached copy at
  `~/.cache/opencode/packages/@prevalentware/opencode-goal-plugin@0.1.48`.
- The test imports the npm-registry artifact resolved from this repository's
  `node_modules`, pinned by the lockfile. The manifest identity asserted by the
  suite is `name = @prevalentware/opencode-goal-plugin`, `version = 0.1.48`,
  with both `./server` and `./tui` exported.
- **Uncertainty:** OpenCode resolves and executes its own cached copy. Both the
  cached copy and the pinned artifact reported `0.1.48`, but the executed global
  copy was not byte-verified against this artifact. A future global version could
  differ without changing this repository; re-run the matrix after any plugin
  update.

No global OpenCode/Midas configuration was mutated, no user goal state was read,
and `node_modules` was not patched.

## Isolation

The suite creates a fresh temp sandbox per test and sets, before the plugin is
imported:

- `OPENCODE_GOAL_STATE_PATH` (private `goals.json`)
- `HOME`, `XDG_CONFIG_HOME`, `XDG_DATA_HOME`, `XDG_CACHE_HOME`, `XDG_STATE_HOME`
- `MIDAS_CONFIG_DIR`, `PI_CONFIG_DIR`, `MIDAS_NO_UPDATE=1`

No `opencode serve`, network/model call, real goal tool, recursive event loop,
unbounded polling, or board task is started.

## Evidence matrix

`pass` = asserted against the real engine; `FAIL (diagnostic)` = confirmed
upstream behaviour that conflicts with the requested invariant, captured by a
bounded diagnostic instead of a fabricated pass.

| # | Matrix row | Result | Evidence |
| --- | --- | --- | --- |
| 1 | Artifact identity (name/version/exports) | pass | `artifact identity` test |
| 2 | Create | pass | `create_goal` → active, trimmed objective, history "created" |
| 3 | Reuse | pass | same objective → `goal_reused`, `duplicate_goal_notice` |
| 4 | Conflict | pass | different objective → `goal_conflict`, original preserved |
| 5 | Objective edits | pass | `update_goal_objective` updates objective/status + "updated" history |
| 6 | Start/pause/resume/clear | pass | tool transitions + `clear_goal`; closed goal cannot resume |
| 7 | Complete requires evidence | pass | empty evidence rejected; goal stays active |
| 8 | Unmet requires blocker | pass | empty blocker rejected; blocker persisted |
| 9 | Token budget + one-shot handoff | pass | cumulative usage → `budgetLimited`, exactly one wrap-up prompt |
| 10 | Auto-turn limit + one-shot handoff | pass | `max_auto_turns` → `usageLimited`, exactly one wrap-up prompt |
| 11 | Elapsed-time limit | pass | `max_duration` reached → `usageLimited` |
| 12 | Paused duration exclusion | pass | elapsed time frozen while paused, not leaked on resume |
| 13 | Duplicate idle/event delivery | pass | repeated idle/status deliveries send exactly one prompt per reserved turn |
| 14 | Reload persistence/counters/pending attempt | pass | counters and `pendingAttempt` survive an engine restart on the same state file |
| 15 | Restricted plan-agent create/resume | pass | plan create stored paused; plan resume refused; opt-out allows |
| 16 | Orchestrator policy behaviour | pass | constant system reminder injected (V1) / context hook (V2) |
| 17 | V2 command/tool registration | pass | 9 tools and `goal`/`pause_goal`/`resume_goal` commands registered |
| 18 | V1 fast-turn wakeup inside minimum interval | **FAIL (diagnostic)** | see below |

Compaction context and generic-autocontinue suppression are additionally covered
by the `V1 compaction preserves the goal snapshot…` test.

## Confirmed upstream defect: V1 fast-turn wakeup

Requirement: a continuation that finishes inside
`min_continue_interval_seconds` must get a later wakeup without unrelated user
activity. V2 schedules this; V1 only returns.

Source evidence in `@prevalentware/opencode-goal-plugin@0.1.48` `dist/server.js`:

- V1 `reserveContinuation` returns `null` when inside the interval:
  `if (goal.lastContinuationAt && now - goal.lastContinuationAt < minIntervalSeconds) return null;`
  (line 897).
- V1 `runAutoContinue` then, on `null`, simply returns — no timer:
  lines 2354–2356.
- V2 `runAutoContinue` schedules a delayed continuation on the same `null`:
  `scheduleSettledContinuation(..., continuationDelayFromSnapshot(minInterval, waiting.lastContinuationAt), ...)`
  (lines 2953–2960), using `continuationDelayFromSnapshot` (lines 1580–1584).

Bounded reproduction (`V1 fast-turn gap vs V2…` test, ~4.6 s):

1. `min_continue_interval_seconds: 1`, create an active goal.
2. Deliver one idle → continuation prompt 1 is sent.
3. Emit a substantive assistant progress message, then deliver a second idle
   immediately (inside the interval).
4. Wait 2.2 s with no further events.

Observed with the real engine:

- V1: still **1** prompt after the interval; no wakeup was scheduled.
- V2: **2** prompts after the interval; the delayed wakeup fired.

This is outside Midas scope to fix here. It is recorded as a failed matrix row
and **must not** be patched in `node_modules`, worked around by silently
upgrading the global plugin, or reported as success. Any fix belongs to a
separate board/repo task against the upstream plugin source.

## Boundary of this evidence

- **Actual engine proof (this document + tests):** real plugin lifecycle,
  persistence, limits, plan restrictions, duplicate delivery, reload, and the
  V1/V2 scheduling divergence, with a bounded fake SDK.
- **Frontend bridge proof (still pending):** Midas surfaces `/goal`,
  `/pause_goal`, and `/resume_goal` via `src/ui/app.ts` → `runController(...)`
  and `src/index.ts`. Verifying that this bridge reaches the plugin requires a
  live OpenCode server; it is not proven here.
- **Manual/live acceptance (still pending):** no real model turn, OpenCode
  session, or endpoint was exercised.

## Reproduction

Exact suite (also the controller's required check):

```bash
npm ci --ignore-scripts
S=$(mktemp -d); MIDAS_NO_UPDATE=1 MIDAS_CONFIG_DIR="$S/midas" PI_CONFIG_DIR="$S/pi" \
  XDG_CONFIG_HOME="$S/config" XDG_DATA_HOME="$S/data" XDG_CACHE_HOME="$S/cache" \
  XDG_STATE_HOME="$S/state" OPENCODE_GOAL_STATE_PATH="$S/goals.json" \
  ./node_modules/.bin/tsx --test src/opencode/goal-engine.test.ts
npm run typecheck
test -s docs/goal-engine-evidence.md
```

Last run: 17 tests, 17 pass, 0 fail (~9 s), plus the printed diagnostic:

```text
CONFIRMED UPSTREAM DEFECT (V1): a continuation completing inside
min_continue_interval_seconds produced 1 prompt(s) after the interval with no
unrelated activity; V1 returned without scheduling a wakeup.
```
