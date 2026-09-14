# Merge-agent evidence: real conflict resolution in isolation

Status: **real merge-agent proof complete.** This document claims only that the
production `mergeTask` path, with its default real merge-agent resolver, resolves
one benign fixture conflict under a disposable `mkdtemp` repository. It is **not**
a multi-worker throughput run; Lightrig supplies that.

## What this proves (and what it does not)

The existing `src/tasks/merge-safety.test.ts` suite and the disjoint Lightrig
merges only exercised `mergeTask` with an **injected** `ConflictResolver`. They
prove the integration pipeline, not the real `merge` subagent. This harness drives
the actual production entry points:

- `new TaskBoard(root)` and `board.add(contract)` in an independently
  `git init`-ed repository under `mkdtemp`;
- `runTask(board, id, deterministicWorker)` — the worker callback writes a tiny
  fixture change deterministically; **no model is used for the worker**;
- `advanceTarget(root)` — a deterministic, independent commit on the target
  branch *after* the task branch was cut, so the later merge genuinely conflicts;
- `mergeTask(board, id, output)` — called with **only three arguments**, so the
  default `mergeAgentResolve` (the real `merge` subagent) runs. The fourth
  argument is omitted entirely on the production path; only the deterministic
  unit tests ever inject a resolver.

It does **not** prove multi-worker parallelism, throughput, or the broader board
dispatcher; those are out of scope.

## Isolation

- The fixture is a fresh `mkdtemp` repository. Its Git operations run only there
  or in a throwaway detached probe worktree; the harness records the invoking
  worktree's HEAD and `git status --porcelain` before and after and asserts they
  are byte-identical.
- The conflict is manufactured only in the disposable fixture. No real app
  worktree is touched.
- The fixture links the real dependency store (`node_modules`) so the isolated
  integration worktree can resolve packages, and one check runs
  `node -e "require.resolve('typescript')"` — the same reproducibility path
  (`linkDependencies` in `src/tasks/integration-checks.ts`) the production
  integration uses. Root-only build output/config is deliberately **not** linked.
- `MIDAS_NO_UPDATE=1` is set. No global OpenCode/Midas config, board, goal state,
  browser/UI, deployment, or credentials are read or written by the harness. The
  model call uses the machine's existing authorized OpenCode backend; the harness
  does not sandbox `HOME`/`XDG_*`/`MIDAS_CONFIG_DIR`, because that would remove
  the authorized provider and change what is being proven. The merge agent's cwd
  is the disposable integration worktree, its shell is restricted to `git *`, and
  its edit scope is that checkout.

## Real model vs deterministic callbacks

| Portion | Real or deterministic |
| --- | --- |
| Fixture creation, `.gitignore`, dependency link, baseline commit | deterministic Git |
| Task worker (`runTask` callback) | deterministic callback (no model) |
| Target-branch advance + conflict precondition | deterministic Git |
| Conflict-existed probe (`probeConflict`) | deterministic detached worktree; aborted/removed |
| Integration validation checks (including dependency resolution) | real shell checks via the production pipeline |
| Conflict resolution (`mergeTask`'s omitted resolver) | **real `merge` subagent** (one attempt, no retries) |
| Fixture/cleanup, ancestry/root assertions | deterministic |

## Observed run

Command (also the controller's required check):

```bash
MIDAS_NO_UPDATE=1 MIDAS_RUN_MERGE_AGENT_SMOKE=1 ./node_modules/.bin/tsx scripts/merge-agent-smoke.ts
```

Result: `merge-agent smoke: PASS`, one conflict-resolution attempt, ~37.3 s
(merge phase ~36.7 s). Key evidence captured by the harness:

- Conflict really existed (deterministic probe, before any model call):

  ```text
  # Independent settings. A correct merge keeps every value.
  <<<<<<< HEAD
  profile=prod retries=3 timeout=30
  =======
  profile=default retries=5 timeout=30
  >>>>>>> c8fd879d4f50801114f00c3b05593f7a570163b9
  ```

- Real resolver ran with default wiring: `resolver.defaultResolver = true`,
  `resolver.mode = "real-merge-agent"`; the merge agent's own words were captured
  (excerpt):

  > A line-level merge flagged the whole settings line as conflicting, but the two
  > sides changed *different* settings. … `main`/HEAD changed only `profile`:
  > `default` → `prod`; task branch T1 changed only `retries`: `3` → `5`; …
  > I combined both edits instead of picking a side:
  > `profile=prod retries=5 timeout=30`.

- Resulting combined content in the fixture root:

  ```text
  # Independent settings. A correct merge keeps every value.
  profile=prod retries=5 timeout=30
  ```

  `tokensPresent = { "profile=prod": true, "retries=5": true, "timeout=30": true }`,
  `markersAbsent = true`. The integration checks also enforce this: once
  `target.marker` exists, `grep -q 'profile=prod'` must pass, so a resolution that
  drops either side fails validation.

- Validated merge ancestry (two parents, exact inputs):

  ```text
  base         67bff77fe5a6ea83bf4c58adde61a6897b092353
  result       c8fd879d4f50801114f00c3b05593f7a570163b9  (deterministic worker)
  mergedCommit 4ebea340902ec9664240eca19e8b55ddc0f92893
  parents      [67bff77fe5a6ea83bf4c58adde61a6897b092353,
                c8fd879d4f50801114f00c3b05593f7a570163b9]
  ```

- Root/status isolation:
  - invoking (source) worktree HEAD and `git status --porcelain` unchanged:
    `source.unchanged = true`; the only entries are the new, uncommitted task
    files (`?? scripts/merge-agent-smoke.ts`,
    `?? src/tasks/merge-agent-smoke.test.ts`);
  - the deterministic conflict probe left the fixture root's HEAD/status
    unchanged (`conflict.rootUnchangedByProbe = true`);
  - after the intended merge the fixture root's status is clean
    (`root.clean = true`) and its HEAD is exactly the validated merge commit
    (`root.advanced = true`). The fixture root HEAD *does* advance by design — the
    production merge lands the validated candidate on the target branch; "HEAD
    unchanged" refers to the probe and to the invoking source worktree.

### Provenance available and its limit

The harness captures the real merge agent's textual resolution and the backend's
behavioural output, and knows the resolver was the default one because the CLI
path never supplies a resolver. It does **not** expose the OpenCode session id:
`mergeAgentResolve` does not surface it, and the source-allowed files for this
task cannot add an export to `src/tasks/runner.ts`. No session id is therefore
claimed. The resolver output quoting the fixture's merge base (`760b8d0`) and
per-side changes is the strongest available provenance that the model actually
inspected this fixture.

## Deadline and failure handling

The real smoke is opt-in. Without `MIDAS_RUN_MERGE_AGENT_SMOKE=1` the CLI prints
an `unavailable` message and exits nonzero (2) without any model call.

When enabled, the top-level process spawns the model run as a **detached child in
its own process group** and enforces an independent wall-clock deadline
(`MIDAS_MERGE_AGENT_SMOKE_DEADLINE_MS`, default 300 s). On expiry it kills the
whole process group, including the `opencode serve` backend, so no continuing
agent is left behind. There is exactly one conflict-resolution attempt and no
retry loop.

## Deterministic tests (no model calls)

`./node_modules/.bin/tsx --test src/tasks/merge-agent-smoke.test.ts` — 6 tests,
6 pass:

1. the fixture is an independently initialized, clean repository;
2. a deterministic resolver produces the combined content, validated two-parent
   ancestry, clean root and cleaned-up fixture;
3. a resolution that drops the target setting fails integration validation;
4. a resolution that leaves conflict markers is rejected;
5. a thrown resolver failure marks the merge failed, preserves the target branch
   and cleans up;
6. the real smoke is opt-in and exits nonzero with an `unavailable` message.

## Reproduction

```bash
npm ci --ignore-scripts
./node_modules/.bin/tsx --test src/tasks/merge-agent-smoke.test.ts
npm run typecheck
MIDAS_NO_UPDATE=1 MIDAS_RUN_MERGE_AGENT_SMOKE=1 ./node_modules/.bin/tsx scripts/merge-agent-smoke.ts
test -s docs/merge-agent-evidence.md
```
