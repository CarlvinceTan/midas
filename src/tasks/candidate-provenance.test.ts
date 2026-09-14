import test from "node:test";
import assert from "node:assert/strict";
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { git, type Task } from "./board.ts";
import { applyValidatedCandidate, prepareCandidate } from "./integration-checks.ts";
import type { OutputSink } from "./runner.ts";

/**
 * Deterministic regressions for the isolated integration path's provenance.
 *
 * Every fixture is an independent temporary repository; the harness only writes
 * there. No board process, real merge agent or root checkout is involved. The
 * apply seam and the injected resolver/check callbacks are the bounded seams
 * that let concurrent changes be simulated without racing a real user.
 */

// Identity for disposable fixture commits only; never modifies Git configuration.
process.env.GIT_AUTHOR_NAME = process.env.GIT_COMMITTER_NAME = "Midas provenance tests";
process.env.GIT_AUTHOR_EMAIL = process.env.GIT_COMMITTER_EMAIL = "provenance@example.invalid";

const output: OutputSink = () => {};

function fixture(t: { after(fn: () => void): void }): string {
  const cwd = mkdtempSync(join(tmpdir(), "midas-candidate-provenance-"));
  t.after(() => rmSync(cwd, { recursive: true, force: true }));
  git(cwd, "init", "-b", "main");
  writeFileSync(join(cwd, "a.txt"), "a0\n");
  writeFileSync(join(cwd, "b.txt"), "b0\n");
  git(cwd, "add", ".");
  git(cwd, "commit", "-m", "base");
  return cwd;
}

function task(): Task {
  return { id: "T1", title: "provenance", instructions: "exercised by the fixture", checks: ["true"],
    status: "completed", merge: "not-merged", target: "main", attempts: [], revision: 1 };
}

interface MergeInputs { cwd: string; base: string; resultCommit: string }

/** `result` edits b.txt only, so merging it into `base` is clean. */
function cleanInputs(t: { after(fn: () => void): void }): MergeInputs {
  const cwd = fixture(t);
  const base = git(cwd, "rev-parse", "HEAD");
  git(cwd, "checkout", "-b", "result");
  writeFileSync(join(cwd, "b.txt"), "b1\n");
  git(cwd, "add", "b.txt");
  git(cwd, "commit", "-m", "result");
  const resultCommit = git(cwd, "rev-parse", "HEAD");
  git(cwd, "checkout", "main");
  return { cwd, base, resultCommit };
}

/** Both branches edit a.txt differently, so merging `result` conflicts. */
function conflictingInputs(t: { after(fn: () => void): void }): MergeInputs {
  const cwd = fixture(t);
  git(cwd, "checkout", "-b", "result");
  writeFileSync(join(cwd, "a.txt"), "result\n");
  git(cwd, "add", "a.txt");
  git(cwd, "commit", "-m", "result");
  const resultCommit = git(cwd, "rev-parse", "HEAD");
  git(cwd, "checkout", "main");
  writeFileSync(join(cwd, "a.txt"), "target\n");
  git(cwd, "add", "a.txt");
  git(cwd, "commit", "-m", "target");
  return { cwd, base: git(cwd, "rev-parse", "HEAD"), resultCommit };
}

function read(cwd: string, path: string): string {
  return readFileSync(join(cwd, path), "utf8");
}

test("a clean candidate validates exactly and reports two-parent provenance", async (t) => {
  const { cwd, base, resultCommit } = cleanInputs(t);

  let checks = 0;
  const candidate = await prepareCandidate({
    repoRoot: cwd, base, resultCommit, task: task(), output,
    runChecks: async () => { checks += 1; },
    resolveConflict: async () => { throw new Error("the resolver must not run for a clean merge"); },
  });

  assert.equal(checks, 1, "the checks must run exactly once");
  const parents = git(cwd, "rev-list", "--parents", "-n", "1", candidate.commit).split(/\s+/).slice(1);
  assert.deepEqual(parents, [base, resultCommit], "the candidate is the merge of exactly the two validated inputs");
  assert.equal(git(cwd, "rev-parse", `${candidate.commit}^{tree}`), candidate.tree);
  assert.equal(candidate.tree, git(cwd, "merge-tree", "--write-tree", base, resultCommit).split("\n")[0],
    "the validated tree is the exact merge tree");
  // The disposable worktree is detached; the human checkout is untouched.
  assert.equal(git(cwd, "rev-parse", "HEAD"), base);
  assert.equal(git(cwd, "status", "--porcelain"), "");
});

test("a resolver that leaves a tracked side effect fails before checks run", async (t) => {
  const { cwd, base, resultCommit } = conflictingInputs(t);

  let checksRan = false;
  await assert.rejects(
    prepareCandidate({
      repoRoot: cwd, base, resultCommit, task: task(), output,
      runChecks: async () => { checksRan = true; },
      resolveConflict: async (_task, dir) => {
        writeFileSync(join(dir, "a.txt"), "resolved\n");
        // Unrelated tracked path, modified but never staged or committed.
        writeFileSync(join(dir, "b.txt"), "tampered\n");
      },
    }),
    /Integration checks modified the candidate tree/,
  );

  assert.equal(checksRan, false, "checks must never validate a tree the returned commit excludes");
  assert.equal(git(cwd, "status", "--porcelain"), "", "the human checkout is untouched");
});

test("a resolver that leaves an untracked side effect fails before checks run", async (t) => {
  const { cwd, base, resultCommit } = conflictingInputs(t);

  let checksRan = false;
  await assert.rejects(
    prepareCandidate({
      repoRoot: cwd, base, resultCommit, task: task(), output,
      runChecks: async () => { checksRan = true; },
      resolveConflict: async (_task, dir) => {
        writeFileSync(join(dir, "a.txt"), "resolved\n");
        // Untracked side effect: the old staging baseline would have validated it.
        writeFileSync(join(dir, "probe.txt"), "one\n");
      },
    }),
    /Integration checks modified the candidate tree/,
  );

  assert.equal(checksRan, false);
  assert.equal(git(cwd, "status", "--porcelain"), "");
});

test("checks that move the candidate HEAD are rejected", async (t) => {
  const { cwd, base, resultCommit } = cleanInputs(t);

  await assert.rejects(
    prepareCandidate({
      repoRoot: cwd, base, resultCommit, task: task(), output,
      runChecks: async (dir) => { git(dir, "commit", "--allow-empty", "-m", "head drift"); },
      resolveConflict: async () => {},
    }),
    /moved the candidate HEAD/,
  );
  assert.equal(git(cwd, "rev-parse", "HEAD"), base);
});

test("checks that change the candidate tree are rejected", async (t) => {
  const { cwd, base, resultCommit } = cleanInputs(t);

  await assert.rejects(
    prepareCandidate({
      repoRoot: cwd, base, resultCommit, task: task(), output,
      runChecks: async (dir) => { writeFileSync(join(dir, "b.txt"), "drift\n"); },
      resolveConflict: async () => {},
    }),
    /Integration checks modified the candidate tree/,
  );
  assert.equal(git(cwd, "status", "--porcelain"), "");
});

test("a concurrent target advance during apply is never overwritten", async (t) => {
  const cwd = fixture(t);
  const base = git(cwd, "rev-parse", "HEAD");
  git(cwd, "checkout", "-b", "candidate");
  writeFileSync(join(cwd, "a.txt"), "a1\n");
  writeFileSync(join(cwd, "b.txt"), "b1\n");
  git(cwd, "add", ".");
  git(cwd, "commit", "-m", "candidate");
  const candidate = git(cwd, "rev-parse", "HEAD");
  git(cwd, "checkout", "main");
  // The target advances after validation but before the branch is advanced, and
  // itself edits one of the merge paths.
  writeFileSync(join(cwd, "a.txt"), "target-newer\n");
  writeFileSync(join(cwd, "unrelated.txt"), "advance\n");
  git(cwd, "add", "a.txt", "unrelated.txt");
  git(cwd, "commit", "-m", "concurrent advance");
  const advanced = git(cwd, "rev-parse", "HEAD");

  await assert.rejects(applyValidatedCandidate(cwd, "main", base, candidate), /cannot lock ref|expected/i);

  assert.equal(git(cwd, "rev-parse", "main"), advanced, "the concurrent commit must survive");
  assert.equal(git(cwd, "rev-parse", "HEAD"), advanced);
  // The paths we wrote are restored to the current tip, not resurrected from base.
  assert.equal(read(cwd, "a.txt"), "target-newer\n", "the newer committed bytes win over the stale base");
  assert.equal(read(cwd, "b.txt"), "b0\n");
  assert.equal(read(cwd, "unrelated.txt"), "advance\n");
  assert.equal(git(cwd, "status", "--porcelain"), "");
});

test("rollback never overwrites a human edit made to an applied path", async (t) => {
  const cwd = fixture(t);
  const base = git(cwd, "rev-parse", "HEAD");
  git(cwd, "checkout", "-b", "candidate");
  writeFileSync(join(cwd, "a.txt"), "a1\n");
  writeFileSync(join(cwd, "b.txt"), "b1\n");
  git(cwd, "add", ".");
  git(cwd, "commit", "-m", "candidate");
  const candidate = git(cwd, "rev-parse", "HEAD");
  git(cwd, "checkout", "main");
  // Advance the target so the compare-and-swap fails and rollback runs.
  writeFileSync(join(cwd, "unrelated.txt"), "advance\n");
  git(cwd, "add", "unrelated.txt");
  git(cwd, "commit", "-m", "concurrent advance");
  const advanced = git(cwd, "rev-parse", "HEAD");
  // Unrelated user work that rollback must not touch.
  writeFileSync(join(cwd, "user-staged.txt"), "staged\n");
  git(cwd, "add", "user-staged.txt");
  writeFileSync(join(cwd, "user-scratch.txt"), "scratch\n");
  const stagedIndex = git(cwd, "rev-parse", ":user-staged.txt");

  await assert.rejects(
    applyValidatedCandidate(cwd, "main", base, candidate, {
      onPathApplied: (path) => {
        // A human edits the path right after this attempt wrote it.
        if (path === "a.txt") writeFileSync(join(cwd, "a.txt"), "human\n");
      },
    }),
  );

  assert.equal(read(cwd, "a.txt"), "human\n", "a human edit must never be clobbered by rollback");
  assert.equal(read(cwd, "b.txt"), "b0\n", "a path still holding the candidate content is restored");
  assert.equal(git(cwd, "rev-parse", "main"), advanced, "the branch tip is unchanged");
  assert.equal(read(cwd, "user-staged.txt"), "staged\n");
  assert.equal(git(cwd, "rev-parse", ":user-staged.txt"), stagedIndex, "unrelated staged entries are preserved");
  assert.equal(read(cwd, "user-scratch.txt"), "scratch\n");
  assert.match(git(cwd, "status", "--porcelain"), /a\.txt/);
});

test("rollback leaves a file a human recreated over a deleted path", async (t) => {
  const cwd = fixture(t);
  const base = git(cwd, "rev-parse", "HEAD");
  git(cwd, "checkout", "-b", "candidate");
  rmSync(join(cwd, "b.txt"));
  git(cwd, "add", "--all");
  git(cwd, "commit", "-m", "candidate deletes b");
  const candidate = git(cwd, "rev-parse", "HEAD");
  git(cwd, "checkout", "main");
  writeFileSync(join(cwd, "unrelated.txt"), "advance\n");
  git(cwd, "add", "unrelated.txt");
  git(cwd, "commit", "-m", "concurrent advance");
  const advanced = git(cwd, "rev-parse", "HEAD");

  await assert.rejects(
    applyValidatedCandidate(cwd, "main", base, candidate, {
      onPathApplied: (path) => {
        // An untracked file cannot be seen by `git diff`; existence must gate it.
        if (path === "b.txt") writeFileSync(join(cwd, "b.txt"), "human-recreated\n");
      },
    }),
  );

  assert.equal(read(cwd, "b.txt"), "human-recreated\n", "a recreated untracked file must never be removed");
  assert.equal(git(cwd, "rev-parse", "main"), advanced);
});

test("a clean success advances the target to the validated commit", async (t) => {
  const cwd = fixture(t);
  const base = git(cwd, "rev-parse", "HEAD");
  git(cwd, "checkout", "-b", "candidate");
  writeFileSync(join(cwd, "a.txt"), "a1\n");
  git(cwd, "add", "a.txt");
  git(cwd, "commit", "-m", "candidate");
  const candidate = git(cwd, "rev-parse", "HEAD");
  git(cwd, "checkout", "main");
  // Unrelated WIP must survive the apply.
  writeFileSync(join(cwd, "notes.txt"), "keep\n");

  await applyValidatedCandidate(cwd, "main", base, candidate);

  assert.equal(git(cwd, "rev-parse", "main"), candidate);
  assert.equal(git(cwd, "rev-parse", "HEAD"), candidate);
  assert.equal(read(cwd, "a.txt"), "a1\n");
  assert.equal(read(cwd, "notes.txt"), "keep\n");
  assert.equal(git(cwd, "status", "--porcelain"), "?? notes.txt");
});
