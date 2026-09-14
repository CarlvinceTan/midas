import test from "node:test";
import assert from "node:assert/strict";
import { createRequire } from "node:module";
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, symlinkSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { TaskBoard, git, type Task } from "./board.ts";
import { runTask, mergeTask } from "./runner.ts";

// Identity for disposable fixture commits only; never modifies Git configuration.
process.env.GIT_AUTHOR_NAME = process.env.GIT_COMMITTER_NAME = "Midas tests";
process.env.GIT_AUTHOR_EMAIL = process.env.GIT_COMMITTER_EMAIL = "tests@example.invalid";

const require = createRequire(import.meta.url);
/** The real dependency store, wherever this test is loaded from. */
const NODE_MODULES = dirname(dirname(require.resolve("typescript/package.json")));

function fixture(t: { after(fn: () => void): void }, options: { deps?: boolean } = {}): { cwd: string; board: TaskBoard } {
  const cwd = mkdtempSync(join(tmpdir(), "midas-merge-safety-"));
  t.after(() => rmSync(cwd, { recursive: true, force: true }));
  git(cwd, "init", "-b", "main");
  writeFileSync(join(cwd, ".gitignore"), "node_modules/\ndist/\n");
  writeFileSync(join(cwd, "base.txt"), "original\n");
  writeFileSync(join(cwd, "old.txt"), "keep\n");
  git(cwd, "add", ".");
  git(cwd, "commit", "-m", "fixture");
  // An explicitly provisioned dependency store, mirroring a real checkout.
  if (options.deps) symlinkSync(NODE_MODULES, join(cwd, "node_modules"), "dir");
  return { cwd, board: new TaskBoard(cwd) };
}

const contract = { title: "Merge safety", instructions: "Add result.txt", checks: ["test -f result.txt"] };

function read(cwd: string, path: string): string {
  return readFileSync(join(cwd, path), "utf8");
}

/**
 * Two tasks that both add result.txt identically but edit base.txt differently,
 * so merging the second conflicts on base.txt. `secondChecks` lets a test make
 * the integration check fail after resolution.
 */
async function conflictingPair(board: TaskBoard, secondChecks: string[] = contract.checks): Promise<{ first: Task; second: Task }> {
  const first = board.add(contract);
  const second = board.add({ ...contract, checks: secondChecks });
  for (const [task, value] of [[first, "first"], [second, "second"]] as const) {
    await runTask(board, task.id, async (_, attempt) => {
      writeFileSync(join(attempt.worktree, "result.txt"), "shared\n");
      writeFileSync(join(attempt.worktree, "base.txt"), `${value}\n`);
    });
  }
  return { first, second };
}

test("a clean merge validates the exact candidate tree and advances the target to it", async (t) => {
  const { cwd, board } = fixture(t);
  const task = board.add(contract);
  await runTask(board, task.id, async (_, attempt) => writeFileSync(join(attempt.worktree, "result.txt"), "done\n"));
  const attempt = board.get(task.id).attempts.at(-1)!;
  const base = git(cwd, "rev-parse", "HEAD");
  const expectedTree = git(cwd, "merge-tree", "--write-tree", base, attempt.result!).split("\n")[0]!;

  await mergeTask(board, task.id);

  assert.equal(board.get(task.id).merge, "merged");
  const merged = board.get(task.id).mergedCommit!;
  assert.equal(git(cwd, "rev-parse", "HEAD"), merged);
  assert.equal(git(cwd, "rev-parse", "main"), merged);
  assert.equal(git(cwd, "rev-parse", `${merged}^{tree}`), expectedTree, "committed tree must be the validated merge tree");
  assert.deepEqual(git(cwd, "rev-list", "--parents", "-n", "1", merged).split(/\s+/).slice(1), [base, attempt.result]);
  assert.equal(read(cwd, "result.txt"), "done\n");
  // The disposable integration worktree never persists.
  assert.ok(!git(cwd, "worktree", "list", "--porcelain").includes("midas-integration"));
});

test("unrelated dirty root state is preserved byte- and index-identical", async (t) => {
  const { cwd, board } = fixture(t);
  const task = board.add(contract);
  await runTask(board, task.id, async (_, attempt) => writeFileSync(join(attempt.worktree, "result.txt"), "done\n"));

  writeFileSync(join(cwd, "staged.txt"), "staged user\n");
  git(cwd, "add", "staged.txt");
  git(cwd, "mv", "old.txt", "renamed old.txt");
  writeFileSync(join(cwd, "base.txt"), "unstaged user\n");
  writeFileSync(join(cwd, "notes with spaces.txt"), "scratch\n");

  const before = {
    status: git(cwd, "status", "--porcelain"),
    staged: read(cwd, "staged.txt"),
    renamed: read(cwd, "renamed old.txt"),
    notes: read(cwd, "notes with spaces.txt"),
    baseBytes: read(cwd, "base.txt"),
    stagedIndex: git(cwd, "rev-parse", ":staged.txt"),
    renameIndex: git(cwd, "rev-parse", ":renamed old.txt"),
    baseIndex: git(cwd, "rev-parse", ":base.txt"),
  };

  await mergeTask(board, task.id);

  assert.equal(board.get(task.id).merge, "merged");
  assert.equal(board.get(task.id).mergeBlocked, undefined);
  assert.equal(read(cwd, "staged.txt"), before.staged);
  assert.equal(read(cwd, "renamed old.txt"), before.renamed);
  assert.equal(read(cwd, "notes with spaces.txt"), before.notes);
  assert.equal(read(cwd, "base.txt"), before.baseBytes);
  assert.equal(git(cwd, "rev-parse", ":staged.txt"), before.stagedIndex);
  assert.equal(git(cwd, "rev-parse", ":renamed old.txt"), before.renameIndex);
  assert.equal(git(cwd, "rev-parse", ":base.txt"), before.baseIndex, "unrelated index entries must be untouched");
  assert.equal(git(cwd, "status", "--porcelain"), before.status, "user-visible working state must be unchanged");
  // Only the merge content is committed; unrelated staged files stay staged.
  const stagedNames = git(cwd, "diff", "--cached", "--name-only");
  assert.match(stagedNames, /staged\.txt/);
  assert.doesNotMatch(stagedNames, /result\.txt/);
  assert.equal(read(cwd, "result.txt"), "done\n");
});

test("a merge that deletes and renames applies only those tracked changes", async (t) => {
  const { cwd, board } = fixture(t);
  const task = board.add({ ...contract, checks: ["test -f new.txt", "test ! -f old.txt", "test ! -f base.txt"] });
  await runTask(board, task.id, async (_, attempt) => {
    git(attempt.worktree, "mv", "old.txt", "new.txt");
    rmSync(join(attempt.worktree, "base.txt"));
  });
  writeFileSync(join(cwd, "notes.txt"), "keep\n");

  await mergeTask(board, task.id);

  assert.equal(board.get(task.id).merge, "merged");
  assert.equal(read(cwd, "new.txt"), "keep\n");
  assert.equal(existsSync(join(cwd, "old.txt")), false);
  assert.equal(existsSync(join(cwd, "base.txt")), false);
  assert.equal(read(cwd, "notes.txt"), "keep\n");
});

test("integration checks do not see unrelated untracked root files", async (t) => {
  const { cwd, board } = fixture(t);
  writeFileSync(join(cwd, "root-only.txt"), "root\n");
  // Would fail if the checks ran in the human's checkout; passes when isolated.
  const task = board.add({ ...contract, checks: ["test -f result.txt", "test ! -f root-only.txt"] });
  await runTask(board, task.id, async (_, attempt) => writeFileSync(join(attempt.worktree, "result.txt"), "done\n"));

  await mergeTask(board, task.id);

  assert.equal(board.get(task.id).merge, "merged");
  assert.equal(read(cwd, "root-only.txt"), "root\n");
});

test("a check that rewrites root-only state from its cwd fails validation and leaves root bytes unchanged", async (t) => {
  const { cwd, board } = fixture(t, { deps: true });
  writeFileSync(join(cwd, "root-only.txt"), "root\n");
  // `../node_modules` exists only in the integration worktree, so the mutation
  // runs during validation but not during the worker's own checks.
  const task = board.add({ ...contract, checks: ["test -f result.txt", "test ! -d ../node_modules || printf tampered > root-only.txt"] });
  await runTask(board, task.id, async (_, attempt) => writeFileSync(join(attempt.worktree, "result.txt"), "done\n"));
  assert.equal(board.get(task.id).status, "completed");

  await assert.rejects(mergeTask(board, task.id), /Integration checks modified the candidate tree/);

  assert.equal(board.get(task.id).merge, "failed");
  assert.equal(read(cwd, "root-only.txt"), "root\n");
});

test("a byte change by checks with unchanged status still fails validation", async (t) => {
  const { cwd, board } = fixture(t);
  const { first, second } = await conflictingPair(board, ["test -f result.txt", "test ! -f probe.txt || printf two > probe.txt"]);
  await mergeTask(board, first.id);
  const base = git(cwd, "rev-parse", "HEAD");
  // The resolver leaves an untracked probe; the check rewrites it, so its
  // porcelain status (`?? probe.txt`) is identical before and after.
  await assert.rejects(
    mergeTask(board, second.id, undefined, async (_task, dir) => {
      writeFileSync(join(dir, "base.txt"), "resolved\n");
      writeFileSync(join(dir, "probe.txt"), "one\n");
    }),
    /Integration checks modified the candidate tree/,
  );

  assert.equal(git(cwd, "rev-parse", "HEAD"), base);
  assert.equal(board.get(second.id).merge, "failed");
  assert.equal(git(cwd, "status", "--porcelain"), "");
});

test("concurrent target-branch movement during validation defers without clobbering", async (t) => {
  const { cwd, board } = fixture(t);
  const { first, second } = await conflictingPair(board);
  await mergeTask(board, first.id);
  writeFileSync(join(cwd, "notes.txt"), "keep\n");

  await mergeTask(board, second.id, undefined, async (_task, dir) => {
    writeFileSync(join(dir, "base.txt"), "resolved\n");
    git(cwd, "commit", "--allow-empty", "-m", "concurrent target commit");
  });

  assert.equal(board.get(second.id).merge, "not-merged");
  assert.match(board.get(second.id).mergeBlocked ?? "", /moved during integration checks/);
  assert.equal(git(cwd, "log", "-1", "--format=%s"), "concurrent target commit", "the concurrent commit is preserved");
  assert.equal(read(cwd, "notes.txt"), "keep\n");
  assert.equal(git(cwd, "status", "--porcelain"), "?? notes.txt");
});

test("concurrent local changes to a merge path during validation defer without clobbering", async (t) => {
  const { cwd, board } = fixture(t);
  const { first, second } = await conflictingPair(board);
  await mergeTask(board, first.id);
  const base = git(cwd, "rev-parse", "HEAD");
  const indexBefore = git(cwd, "rev-parse", ":base.txt");

  await mergeTask(board, second.id, undefined, async (_task, dir) => {
    writeFileSync(join(dir, "base.txt"), "resolved\n");
    writeFileSync(join(cwd, "base.txt"), "concurrent edit\n");
  });

  assert.equal(board.get(second.id).merge, "not-merged");
  assert.match(board.get(second.id).mergeBlocked ?? "", /base\.txt/);
  assert.equal(git(cwd, "rev-parse", "HEAD"), base);
  assert.equal(read(cwd, "base.txt"), "concurrent edit\n");
  assert.equal(git(cwd, "rev-parse", ":base.txt"), indexBefore, "the human's index must be untouched");
});

test("a concurrent edit made while checks run defers instead of clobbering", async (t) => {
  const { cwd, board } = fixture(t, { deps: true });
  // Runs only in the isolated worktree, where the dependency link is visible,
  // and edits a path the merge would overwrite in the human's checkout.
  const task = board.add({ ...contract, checks: ["test -f result.txt", "test ! -d ../node_modules || printf concurrent > " + JSON.stringify(join(cwd, "result.txt"))] });
  await runTask(board, task.id, async (_, attempt) => writeFileSync(join(attempt.worktree, "result.txt"), "done\n"));
  const base = git(cwd, "rev-parse", "HEAD");

  await mergeTask(board, task.id);

  assert.equal(board.get(task.id).merge, "not-merged");
  assert.match(board.get(task.id).mergeBlocked ?? "", /result\.txt/);
  assert.equal(git(cwd, "rev-parse", "HEAD"), base);
  assert.equal(read(cwd, "result.txt"), "concurrent");
});

test("a check failure during integration rolls back without touching target or WIP", async (t) => {
  const { cwd, board } = fixture(t);
  const { first, second } = await conflictingPair(board, ["test -f result.txt", "test \"$(cat base.txt)\" = second"]);
  await mergeTask(board, first.id);
  const base = git(cwd, "rev-parse", "HEAD");
  writeFileSync(join(cwd, "notes.txt"), "keep\n");

  await assert.rejects(
    mergeTask(board, second.id, undefined, async (_task, dir) => { writeFileSync(join(dir, "base.txt"), "wrong\n"); }),
    /Check failed/,
  );

  assert.equal(board.get(second.id).merge, "failed");
  assert.equal(git(cwd, "rev-parse", "HEAD"), base);
  assert.equal(read(cwd, "base.txt"), "first\n");
  assert.equal(read(cwd, "notes.txt"), "keep\n");
  assert.equal(read(cwd, "result.txt"), "shared\n");
});

test("conflict resolver success merges the resolved content and never disturbs WIP", async (t) => {
  const { cwd, board } = fixture(t);
  const { first, second } = await conflictingPair(board);
  await mergeTask(board, first.id);
  const firstMerged = board.get(first.id).mergedCommit!;
  writeFileSync(join(cwd, "notes.txt"), "keep\n");

  await mergeTask(board, second.id, undefined, async (_task, dir) => { writeFileSync(join(dir, "base.txt"), "resolved\n"); });

  assert.equal(board.get(second.id).merge, "merged");
  assert.equal(read(cwd, "base.txt"), "resolved\n");
  assert.equal(read(cwd, "notes.txt"), "keep\n");
  const merged = board.get(second.id).mergedCommit!;
  assert.deepEqual(git(cwd, "rev-list", "--parents", "-n", "1", merged).split(/\s+/).slice(1), [firstMerged, board.get(second.id).attempts.at(-1)!.result]);
});

test("conflict resolver failure marks the merge failed and preserves target and WIP", async (t) => {
  const { cwd, board } = fixture(t);
  const { first, second } = await conflictingPair(board);
  await mergeTask(board, first.id);
  const base = git(cwd, "rev-parse", "HEAD");
  writeFileSync(join(cwd, "notes.txt"), "keep\n");

  await assert.rejects(
    mergeTask(board, second.id, undefined, async () => { throw new Error("resolver gave up"); }),
    /Merge conflict unresolved/,
  );

  assert.equal(board.get(second.id).merge, "failed");
  assert.equal(git(cwd, "rev-parse", "HEAD"), base);
  assert.equal(read(cwd, "base.txt"), "first\n");
  assert.equal(read(cwd, "notes.txt"), "keep\n");
});

test("checks cannot silently rely on root-only build output or config", async (t) => {
  const { cwd, board } = fixture(t);
  mkdirSync(join(cwd, "dist"), { recursive: true });
  writeFileSync(join(cwd, "dist/root-build.txt"), "built\n");
  writeFileSync(join(cwd, "config.local.json"), "{}\n");
  // Relative traversal reaches the human's root from the worker worktree, but
  // the disposable integration worktree is elsewhere, so these are not provided.
  const task = board.add({ ...contract, checks: ["test -f ../../../../dist/root-build.txt", "test -f ../../../../config.local.json"] });
  await runTask(board, task.id, async (_, attempt) => writeFileSync(join(attempt.worktree, "result.txt"), "done\n"));
  assert.equal(board.get(task.id).status, "completed");

  await assert.rejects(mergeTask(board, task.id), /Check failed/);

  assert.equal(board.get(task.id).merge, "failed");
  assert.equal(read(cwd, "config.local.json"), "{}\n");
  assert.equal(read(cwd, "dist/root-build.txt"), "built\n");
});

test("dependencies are explicitly available to isolated integration checks", async (t) => {
  const { cwd, board } = fixture(t, { deps: true });
  const task = board.add({ ...contract, checks: ["test -f result.txt", "node -e \"require.resolve('typescript')\""] });
  await runTask(board, task.id, async (_, attempt) => writeFileSync(join(attempt.worktree, "result.txt"), "done\n"));

  await mergeTask(board, task.id);

  assert.equal(board.get(task.id).merge, "merged");
  assert.equal(board.get(task.id).mergeBlocked, undefined);
});
