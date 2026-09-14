import test from "node:test";
import assert from "node:assert/strict";
import { mkdtempSync, mkdirSync, writeFileSync, readFileSync, rmSync, existsSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import type { Event } from "@opencode-ai/sdk";
import { stripTerminalSequences } from "@earendil-works/pi-tui";
import { TaskBoard, git, scopesOverlap } from "./board.ts";
import { runTask, mergeTask, cleanupTask, removeTask, revisionNotice } from "./runner.ts";
import { TaskDispatcher } from "./dispatcher.ts";
import { taskCli, appendDispatchLog, boardDrained } from "./cli.ts";
import { pathToFileURL } from "node:url";
import { eventSessionId } from "../opencode/session.ts";
import { findImagePaths, readImageAttachment } from "../lib/attachments.ts";
import { RoundedDialogFrame, PanelOverlay } from "../ui/rounded-frame.ts";
import { Toast, TOAST_STYLES, toastWidth, type ToastLevel } from "../ui/components/toast.ts";
import { TasksView, taskIcon } from "../ui/components/tasks-view.ts";
import { QueuedMessages } from "../ui/components/queued-messages.ts";

// Identity for disposable fixture commits only; never modifies Git configuration.
process.env.GIT_AUTHOR_NAME = process.env.GIT_COMMITTER_NAME = "Midas tests";
process.env.GIT_AUTHOR_EMAIL = process.env.GIT_COMMITTER_EMAIL = "tests@example.invalid";

function fixture(t: { after(fn: () => void): void }): { cwd: string; board: TaskBoard } {
  const cwd = mkdtempSync(join(tmpdir(), "midas-tasks-"));
  t.after(() => rmSync(cwd, { recursive: true, force: true }));
  git(cwd, "init", "-b", "main");
  writeFileSync(join(cwd, "base.txt"), "original\n");
  git(cwd, "add", ".");
  git(cwd, "commit", "-m", "fixture");
  return { cwd, board: new TaskBoard(cwd) };
}
const contract = { title: "Implement feature", group: "Feature", instructions: "Add result.txt", checks: ["test -f result.txt"] };

test("worktree lifecycle merges into the checked-out branch", async (t) => {
  const { cwd, board } = fixture(t);
  const task = board.add(contract);
  assert.equal(task.target, "main");
  await runTask(board, task.id, async (_, attempt, session) => {
    assert.notEqual(attempt.worktree, cwd);
    assert.equal(new TaskBoard(attempt.worktree).directory, board.directory);
    assert.equal(readFileSync(join(attempt.worktree, "base.txt"), "utf8"), "original\n");
    session("worker-session");
    writeFileSync(join(attempt.worktree, "result.txt"), "done\n");
    writeFileSync(join(attempt.worktree, "base.txt"), "task edits\n");
  });
  assert.equal(board.get(task.id).status, "completed");
  assert.equal(board.get(task.id).merge, "not-merged");
  assert.ok(board.get(task.id).attempts[0]!.checkedTree);
  // A dirty file the merge would overwrite defers it with a reason; the user's edit survives.
  writeFileSync(join(cwd, "base.txt"), "user edits\n");
  await mergeTask(board, task.id);
  assert.equal(board.get(task.id).merge, "not-merged");
  assert.match(board.get(task.id).mergeBlocked ?? "", /base\.txt/);
  assert.equal(readFileSync(join(cwd, "base.txt"), "utf8"), "user edits\n");
  // Once clean, the task merges straight onto the checked-out branch.
  git(cwd, "checkout", "--", "base.txt");
  await mergeTask(board, task.id);
  assert.equal(board.get(task.id).merge, "merged");
  assert.equal(board.get(task.id).mergeBlocked, undefined);
  assert.equal(readFileSync(join(cwd, "base.txt"), "utf8"), "task edits\n");
  assert.equal(readFileSync(join(cwd, "result.txt"), "utf8"), "done\n");
  await cleanupTask(board, task.id);
  assert.equal(board.get(task.id).attempts[0]!.cleaned, true);
  assert.equal(board.read().tasks.length, 1);
});

test("an unrelated dirty file does not block the merge and survives it byte-identical", async (t) => {
  const { cwd, board } = fixture(t);
  const task = board.add(contract);
  await runTask(board, task.id, async (_, attempt) => writeFileSync(join(attempt.worktree, "result.txt"), "done\n"));
  // base.txt is untouched by the task, so editing it must not defer the merge.
  writeFileSync(join(cwd, "base.txt"), "user edits\n");
  writeFileSync(join(cwd, "notes.txt"), "scratch\n");
  await mergeTask(board, task.id);
  assert.equal(board.get(task.id).merge, "merged");
  assert.equal(board.get(task.id).mergeBlocked, undefined);
  assert.equal(readFileSync(join(cwd, "base.txt"), "utf8"), "user edits\n");
  assert.equal(readFileSync(join(cwd, "notes.txt"), "utf8"), "scratch\n");
  assert.equal(readFileSync(join(cwd, "result.txt"), "utf8"), "done\n");
});

test("a post-merge check failure restores the branch without disturbing unrelated WIP", async (t) => {
  const { cwd, board } = fixture(t);
  const task = board.add({ ...contract, checks: ["test -f result.txt", "test ! -f blocker.txt"] });
  await runTask(board, task.id, async (_, attempt) => writeFileSync(join(attempt.worktree, "result.txt"), "done\n"));
  const base = git(cwd, "rev-parse", "HEAD");
  // Unrelated work-in-progress keeps the merge eligible but fails the check.
  writeFileSync(join(cwd, "base.txt"), "user edits\n");
  writeFileSync(join(cwd, "blocker.txt"), "keep\n");
  await assert.rejects(mergeTask(board, task.id), /Check failed/);
  assert.equal(git(cwd, "rev-parse", "HEAD"), base);
  assert.equal(board.get(task.id).merge, "failed");
  assert.equal(readFileSync(join(cwd, "base.txt"), "utf8"), "user edits\n");
  assert.equal(readFileSync(join(cwd, "blocker.txt"), "utf8"), "keep\n");
  assert.equal(existsSync(join(cwd, "result.txt")), false);
});

test("claims prevent duplicate workers; dependencies wait for merge", async (t) => {
  const { board } = fixture(t);
  const first = board.add(contract);
  const dependent = board.add({ ...contract, dependencies: [first.id] });
  await assert.rejects(runTask(board, dependent.id, async () => {}), /Waiting on/);
  let finish!: () => void;
  let started!: () => void;
  const began = new Promise<void>((resolve) => { started = resolve; });
  const running = runTask(board, first.id, async (_, attempt) => {
    writeFileSync(join(attempt.worktree, "result.txt"), "done");
    await new Promise<void>((resolve) => { finish = resolve; started(); });
  });
  await assert.rejects(runTask(board, first.id, async () => {}), /locked/);
  await began;
  finish();
  await running;
  await assert.rejects(runTask(board, dependent.id, async () => {}), /Waiting on/);
  await mergeTask(board, first.id);
  await runTask(board, dependent.id, async (_, attempt) => { assert.equal(readFileSync(join(attempt.worktree, "result.txt"), "utf8"), "done"); });
});

test("failed checks block completion and retries preserve attempts", async (t) => {
  const { board } = fixture(t);
  const task = board.add(contract);
  await assert.rejects(runTask(board, task.id, async () => {}), /Check failed/);
  assert.equal(board.get(task.id).status, "blocked");
  const oldTree = board.get(task.id).attempts[0]!.worktree;
  await assert.rejects(mergeTask(board, task.id), /validated result/);
  await runTask(board, task.id, async (_, attempt) => writeFileSync(join(attempt.worktree, "result.txt"), "done"));
  assert.equal(board.get(task.id).attempts.length, 2);
  assert.ok(existsSync(oldTree));
});

test("check output is captured, never written to stdout, and forwarded to the sink", async (t) => {
  const { board } = fixture(t);
  const task = board.add({ ...contract, checks: ["echo hello-check"] });
  const seen: string[] = [];
  const leaked = await captureStdout(() =>
    runTask(board, task.id, async (_, attempt) => writeFileSync(join(attempt.worktree, "result.txt"), "done"), undefined, { output: (chunk) => seen.push(chunk) }),
  );
  const output = seen.join("");
  assert.match(output, /Check: echo hello-check/);
  assert.match(output, /hello-check/);
  assert.doesNotMatch(leaked, /hello-check/, "subprocess output must not reach process.stdout");
  assert.equal(board.get(task.id).status, "completed");
});

test("runTask discards subprocess output when no sink is injected", async (t) => {
  const { board } = fixture(t);
  const task = board.add({ ...contract, checks: ["echo should-not-leak"] });
  const leaked = await captureStdout(() =>
    runTask(board, task.id, async (_, attempt) => writeFileSync(join(attempt.worktree, "result.txt"), "done")),
  );
  assert.doesNotMatch(leaked, /should-not-leak/);
  assert.equal(board.get(task.id).status, "completed");
});

test("a failed check folds its captured output into the task detail", async (t) => {
  const { board } = fixture(t);
  const task = board.add({ ...contract, checks: ["echo boom-detail; false"] });
  await assert.rejects(captureStdout(() => runTask(board, task.id, async () => {})), /Check failed/);
  assert.equal(board.get(task.id).status, "blocked");
  assert.match(board.get(task.id).detail ?? "", /boom-detail/);
});

test("worker branch changes and check mutations cannot be marked complete", async (t) => {
  const { board } = fixture(t);
  const task = board.add(contract);
  await assert.rejects(runTask(board, task.id, async (_, attempt) => { git(attempt.worktree, "checkout", "--detach"); }), /changed HEAD or branch/);
  const mutation = board.add({ ...contract, checks: ["printf changed > result.txt"] });
  await assert.rejects(runTask(board, mutation.id, async () => {}), /Checks modified/);
});

test("merge conflict preserves result and never advances target", async (t) => {
  const { cwd, board } = fixture(t);
  const a = board.add({ ...contract, checks: ["test -f base.txt"] });
  const b = board.add({ ...contract, checks: ["test -f base.txt"] });
  for (const task of [a, b]) await runTask(board, task.id, async (_, attempt) => writeFileSync(join(attempt.worktree, "base.txt"), task.id));
  await mergeTask(board, a.id);
  const before = git(cwd, "rev-parse", "HEAD");
  // No resolver -> the conflict is aborted and the task left for inspection.
  await assert.rejects(mergeTask(board, b.id, undefined, async () => { throw new Error("no merge agent"); }));
  assert.equal(board.get(b.id).status, "completed");
  assert.equal(board.get(b.id).merge, "failed");
  assert.equal(git(cwd, "rev-parse", "HEAD"), before);
  await assert.rejects(cleanupTask(board, b.id), /Only merged/);
});

test("merge conflict is resolved by the merge agent and committed", async (t) => {
  const { cwd, board } = fixture(t);
  const a = board.add({ ...contract, checks: ["test -f base.txt"] });
  const b = board.add({ ...contract, checks: ["test -f base.txt"] });
  for (const task of [a, b]) await runTask(board, task.id, async (_, attempt) => writeFileSync(join(attempt.worktree, "base.txt"), task.id));
  await mergeTask(board, a.id);
  await mergeTask(board, b.id, undefined, async (_task, dir) => { writeFileSync(join(dir, "base.txt"), "resolved\n"); });
  assert.equal(board.get(b.id).merge, "merged");
  assert.equal(readFileSync(join(cwd, "base.txt"), "utf8"), "resolved\n");
});

test("merge defers when the target branch is not checked out; merged cleanup force-removes", async (t) => {
  const { cwd, board } = fixture(t);
  const task = board.add(contract);
  await runTask(board, task.id, async (_, attempt) => writeFileSync(join(attempt.worktree, "result.txt"), "done"));
  git(cwd, "checkout", "-b", "other");
  await mergeTask(board, task.id);
  assert.equal(board.get(task.id).merge, "not-merged");
  assert.match(board.get(task.id).mergeBlocked ?? "", /target main is not checked out/);
  git(cwd, "checkout", "main");
  await mergeTask(board, task.id);
  assert.equal(board.get(task.id).merge, "merged");
  assert.equal(board.get(task.id).mergeBlocked, undefined);
  const worktree = board.get(task.id).attempts[0]!.worktree;
  // Ignored/local artifacts must not leave a merged task's worktree lingering.
  writeFileSync(join(worktree, "local.txt"), "keep");
  await cleanupTask(board, task.id);
  assert.equal(board.get(task.id).attempts[0]!.cleaned, true);
  assert.equal(existsSync(worktree), false);
});

test("board rejects invalid contracts, unknown dependencies, corrupt data and concurrent writers", (t) => {
  const { board } = fixture(t);
  assert.throws(() => board.add({ title: "incomplete" }), /Contract requires/);
  assert.throws(() => board.add({ ...contract, dependencies: ["T99"] }), /Unknown dependency/);
  const release = board.lock("board");
  assert.throws(() => board.add(contract), /locked/);
  release();
  board.add(contract);
  writeFileSync(join(board.directory, "board.json"), "invalid");
  assert.throws(() => board.read());
  assert.throws(() => board.add(contract));
  assert.equal(readFileSync(join(board.directory, "board.json"), "utf8"), "invalid");
});

test("nested session events are correctly attributed across concurrent workers", () => {
  for (const [type, properties] of [
    ["message.updated", { info: { sessionID: "worker" } }],
    ["message.part.updated", { part: { sessionID: "worker" } }],
    ["session.updated", { info: { id: "worker" } }],
    ["session.status", { sessionID: "worker" }],
  ] as const) assert.equal(eventSessionId({ type, properties } as Event), "worker");
});

async function settle(dispatcher: TaskDispatcher, rounds = 8): Promise<void> {
  for (let i = 0; i < rounds; i++) {
    await dispatcher.tick();
    await dispatcher.drain();
    await new Promise((resolve) => setTimeout(resolve, 5));
  }
}

test("dispatcher autonomously runs, merges, cleans up and unblocks dependents", async (t) => {
  const { board } = fixture(t);
  const first = board.add(contract);
  const second = board.add({ ...contract, dependencies: [first.id] });
  const dispatcher = new TaskDispatcher(board, {
    lease: false,
    intervalMs: 10,
    concurrency: 1,
    worker: async (task, attempt) => { writeFileSync(join(attempt.worktree, "result.txt"), task.id); },
  });
  await settle(dispatcher);
  dispatcher.stop();
  assert.equal(board.get(first.id).merge, "merged");
  assert.equal(board.get(first.id).attempts[0]!.cleaned, true);
  assert.equal(board.get(second.id).status, "completed");
  assert.equal(board.get(second.id).merge, "merged");
});

test("dispatcher respects concurrency and waits for dependency merges", async (t) => {
  const { board } = fixture(t);
  const make = (scope: string) => ({ title: "Parallel work", instructions: "Write a unique file", checks: ["true"], scope: [scope] });
  const a = board.add(make("a/"));
  const b = board.add(make("b/"));
  const dependent = board.add({ ...make("a/"), dependencies: [a.id] });
  let live = 0;
  let peak = 0;
  const dispatcher = new TaskDispatcher(board, {
    lease: false,
    concurrency: 2,
    worker: async (task, attempt) => {
      live += 1;
      peak = Math.max(peak, live);
      writeFileSync(join(attempt.worktree, `${task.id}.txt`), task.id);
      await new Promise((resolve) => setTimeout(resolve, 150));
      live -= 1;
    },
  });
  await dispatcher.tick();
  assert.equal(dispatcher.running, 2, "only the concurrency limit starts");
  assert.equal(board.get(dependent.id).status, "new", "dependent waits for a merge");
  await settle(dispatcher);
  dispatcher.stop();
  await dispatcher.drain();
  assert.equal(peak, 2);
  assert.equal(board.get(a.id).merge, "merged");
  assert.equal(board.get(b.id).merge, "merged");
  assert.equal(board.get(dependent.id).status, "completed");
});

test("dispatcher merges completed work onto the checked-out branch", async (t) => {
  const { cwd, board } = fixture(t);
  const task = board.add(contract);
  const dispatcher = new TaskDispatcher(board, {
    lease: false,
    intervalMs: 10,
    concurrency: 1,
    worker: async (_, attempt) => { writeFileSync(join(attempt.worktree, "result.txt"), "done"); },
  });
  await settle(dispatcher);
  dispatcher.stop();
  await dispatcher.drain();
  assert.equal(git(cwd, "show", "main:result.txt"), "done");
  assert.equal(board.get(task.id).merge, "merged");
});

test("dispatch daemon exits quietly when another leader holds the lease", async (t) => {
  const { cwd, board } = fixture(t);
  const release = board.lease("dispatch", 60_000);
  t.after(() => release());
  await quiet(() => taskCli(["--cwd", cwd, "dispatch"]));
  assert.equal(board.hasActiveDispatcher(), true);
});

test("dispatch log appends events and boardDrained reflects completion", (t) => {
  const { board } = fixture(t);
  const task = board.add(contract);
  assert.equal(boardDrained(board), false);
  appendDispatchLog(board.directory, "T1: started");
  const text = readFileSync(join(board.directory, "dispatch.log"), "utf8");
  assert.match(text, /T1: started/);
  board.update(task.id, (t) => { t.merge = "merged"; });
  assert.equal(boardDrained(board), true);
});

test("editing a running task notifies its worker once with the new contract", async (t) => {
  const { board } = fixture(t);
  const task = board.add(contract);
  const notices: string[] = [];
  await runTask(board, task.id, async (_task, attempt, _session, _signal, onRevision) => {
    onRevision?.((updated) => notices.push(revisionNotice(updated)));
    board.edit(task.id, { title: "Revised", instructions: "new brief" });
    await new Promise((resolve) => setTimeout(resolve, 40));
    writeFileSync(join(attempt.worktree, "result.txt"), "done");
  }, undefined, { revisionPollMs: 5 });
  assert.equal(board.get(task.id).status, "completed");
  assert.equal(notices.length, 1);
  assert.match(notices[0]!, /Revised/);
  assert.match(notices[0]!, /new brief/);
});

test("halt aborts a running worker and can be resumed", async (t) => {
  const { board } = fixture(t);
  const task = board.add(contract);
  let startedResolve!: () => void;
  const started = new Promise<void>((resolve) => { startedResolve = resolve; });
  const dispatcher = new TaskDispatcher(board, {
    lease: false, intervalMs: 10, concurrency: 1,
    worker: async (_task, _attempt, _session, signal) => {
      startedResolve();
      await new Promise<void>((_resolve, reject) => {
        if (signal?.aborted) reject(new Error("aborted"));
        else signal?.addEventListener("abort", () => reject(new Error("aborted")), { once: true });
      });
    },
  });
  await dispatcher.tick();
  await started;
  board.pause(task.id);
  await dispatcher.tick();
  await dispatcher.drain();
  dispatcher.stop();
  assert.equal(board.get(task.id).status, "blocked");
  assert.equal(board.get(task.id).requestedAction, undefined);
  board.resume(task.id);
  assert.equal(board.get(task.id).status, "new");
  assert.equal(board.get(task.id).merge, "not-merged");
});

test("cancel aborts a running worker and marks it cancelled", async (t) => {
  const { board } = fixture(t);
  const task = board.add(contract);
  let startedResolve!: () => void;
  const started = new Promise<void>((resolve) => { startedResolve = resolve; });
  const dispatcher = new TaskDispatcher(board, {
    lease: false, intervalMs: 10, concurrency: 1,
    worker: async (_task, _attempt, _session, signal) => {
      startedResolve();
      await new Promise<void>((_resolve, reject) => {
        if (signal?.aborted) reject(new Error("aborted"));
        else signal?.addEventListener("abort", () => reject(new Error("aborted")), { once: true });
      });
    },
  });
  await dispatcher.tick();
  await started;
  board.cancel(task.id);
  await dispatcher.tick();
  await dispatcher.drain();
  dispatcher.stop();
  assert.equal(board.get(task.id).status, "cancelled");
  assert.equal(board.get(task.id).requestedAction, undefined);
});

test("halt/clarify/resume/cancel validate task state", (t) => {
  const { board } = fixture(t);
  const task = board.add(contract);
  board.pause(task.id);
  assert.equal(board.get(task.id).status, "blocked");
  board.resume(task.id);
  assert.equal(board.get(task.id).status, "new");
  board.clarify(task.id, "which branch?");
  assert.equal(board.get(task.id).status, "clarify");
  assert.equal(board.get(task.id).detail, "which branch?");
  board.resume(task.id);
  assert.equal(board.get(task.id).status, "new");
  board.cancel(task.id);
  assert.equal(board.get(task.id).status, "cancelled");
  assert.throws(() => board.pause(task.id), /cannot be halted/);
  assert.throws(() => board.resume(task.id), /not blocked or awaiting clarification/);
  board.update(task.id, (t) => { t.merge = "merged"; });
  assert.throws(() => board.cancel(task.id), /already merged/);
});

test("scopesOverlap matches exact paths, directories and globs; empty means wildcard", () => {
  assert.equal(scopesOverlap(["src/a.ts"], ["src/a.ts"]), true);
  assert.equal(scopesOverlap(["src/a.ts"], ["src/b.ts"]), false);
  assert.equal(scopesOverlap(["src/voice/"], ["src/voice/tts.ts"]), true);
  assert.equal(scopesOverlap(["src/*.ts"], ["src/app.ts"]), true);
  assert.equal(scopesOverlap([], ["src/a.ts"]), true);
  assert.equal(scopesOverlap(["src/a.ts"], []), true);
});

test("dispatcher runs one task per lane and different lanes in parallel", async (t) => {
  const { board } = fixture(t);
  const mk = (group: string): ReturnType<typeof board.add> => board.add({ title: group, group, instructions: "x", checks: ["true"], scope: [`${group}/`] });
  mk("alpha");
  mk("alpha");
  mk("beta");
  let live = 0;
  let peak = 0;
  const worker = async (): Promise<void> => { live += 1; peak = Math.max(peak, live); await new Promise((resolve) => setTimeout(resolve, 120)); live -= 1; };
  const dispatcher = new TaskDispatcher(board, { lease: false, concurrency: 3, worker });
  await dispatcher.tick();
  assert.equal(dispatcher.running, 2, "one task per lane runs");
  await dispatcher.drain();
  dispatcher.stop();
  assert.ok(peak >= 2, "different lanes ran together");
});

test("scope-aware scheduling serialises overlapping scopes and runs disjoint ones together", async (t) => {
  const { board } = fixture(t);
  board.add({ title: "a", instructions: "x", checks: ["true"], scope: ["src/"] });
  board.add({ title: "b", instructions: "x", checks: ["true"], scope: ["src/a.ts"] });
  board.add({ title: "c", instructions: "x", checks: ["true"], scope: ["docs/"] });
  const worker = async (): Promise<void> => { await new Promise((resolve) => setTimeout(resolve, 50)); };
  const dispatcher = new TaskDispatcher(board, { lease: false, concurrency: 3, worker });
  await dispatcher.tick();
  // a and b overlap, so only one of them plus c runs.
  assert.equal(dispatcher.running, 2, "overlapping scopes must not run together");
  await dispatcher.drain();
  dispatcher.stop();
});

test("dispatcher defaults to unlimited concurrency so every ready task with disjoint scope starts", async (t) => {
  const { board } = fixture(t);
  for (let i = 0; i < 10; i++) board.add({ title: `Task ${i}`, instructions: "x", checks: ["true"], scope: [`t${i}/`] });
  let release!: () => void;
  const gate = new Promise<void>((resolve) => { release = resolve; });
  const dispatcher = new TaskDispatcher(board, {
    lease: false,
    worker: async () => { await gate; },
  });
  await dispatcher.tick();
  assert.equal(dispatcher.running, 10, "without an explicit cap every disjoint ready task dispatches");
  release();
  await dispatcher.drain();
  dispatcher.stop();
});

test("dispatcher still honors an explicit concurrency cap", async (t) => {
  const { board } = fixture(t);
  for (let i = 0; i < 10; i++) board.add({ title: `Task ${i}`, instructions: "x", checks: ["true"], scope: [`t${i}/`] });
  let release!: () => void;
  const gate = new Promise<void>((resolve) => { release = resolve; });
  const dispatcher = new TaskDispatcher(board, {
    lease: false,
    concurrency: 2,
    worker: async () => { await gate; },
  });
  await dispatcher.tick();
  assert.equal(dispatcher.running, 2, "an explicit concurrency is still a cap");
  release();
  await dispatcher.drain();
  dispatcher.stop();
});

test("dispatcher requeues an interrupted run instead of stranding it", async (t) => {
  const { cwd, board } = fixture(t);
  const task = board.add(contract);
  board.update(task.id, (t) => { t.status = "running"; t.attempts.push({ id: "ghost", worktree: cwd, branch: "ghost", base: "HEAD" }); });
  let release!: () => void;
  const gate = new Promise<void>((resolve) => { release = resolve; });
  const dispatcher = new TaskDispatcher(board, { lease: false, worker: async () => { await gate; } });
  await dispatcher.tick();
  // Reconcile put it back to new and the dispatcher immediately re-dispatched it.
  assert.equal(dispatcher.running, 1, "interrupted task was not requeued and re-dispatched");
  assert.notEqual(board.get(task.id).status, "blocked", "interrupted task was stranded as blocked");
  release();
  await dispatcher.drain();
  dispatcher.stop();
});

test("dispatcher honours a halt/cancel requested before a crash", async (t) => {
  const { cwd, board } = fixture(t);
  const halted = board.add(contract);
  const cancelled = board.add({ ...contract, title: "Cancelled" });
  board.update(halted.id, (t) => { t.status = "running"; t.requestedAction = "pause"; t.attempts.push({ id: "g1", worktree: cwd, branch: "g1", base: "HEAD" }); });
  board.update(cancelled.id, (t) => { t.status = "running"; t.requestedAction = "cancel"; t.attempts.push({ id: "g2", worktree: cwd, branch: "g2", base: "HEAD" }); });
  const dispatcher = new TaskDispatcher(board, { lease: false });
  await dispatcher.tick();
  dispatcher.stop();
  assert.equal(board.get(halted.id).status, "blocked");
  assert.equal(board.get(cancelled.id).status, "cancelled");
});

test("dispatcher lease elects one leader and recovers a stale one", (t) => {
  const { board } = fixture(t);
  const release = board.lease("dispatch", 60_000);
  assert.throws(() => board.lease("dispatch", 60_000), /already running/);
  release();
  const again = board.lease("dispatch", 60_000);
  again();
  // A crashed dispatcher leaves a stale heartbeat; the next one takes over.
  const lock = join(board.directory, "dispatch.lock");
  rmSync(lock, { recursive: true, force: true });
  mkdirSync(lock, { recursive: true });
  writeFileSync(join(lock, "owner.json"), JSON.stringify({ pid: 1, heartbeat: 0 }));
  const recovered = board.lease("dispatch", 30);
  recovered();
});

/** Silences CLI stdout while a test exercises the authoring boundary. */
async function quiet<T>(fn: () => Promise<T>): Promise<T> {
  const original = process.stdout.write;
  process.stdout.write = (() => true) as typeof process.stdout.write;
  try { return await fn(); } finally { process.stdout.write = original; }
}

/** Runs `fn` while recording everything written to process.stdout (forwarded on). */
async function captureStdout(fn: () => Promise<unknown>): Promise<string> {
  const original = process.stdout.write.bind(process.stdout);
  let captured = "";
  process.stdout.write = ((chunk: unknown, ...rest: unknown[]) => {
    captured += String(chunk);
    return (original as (...args: unknown[]) => boolean)(chunk, ...rest);
  }) as typeof process.stdout.write;
  try { await fn(); } finally { process.stdout.write = original; }
  return captured;
}

test("task authoring requires a live dispatcher lease", async (t) => {
  const { cwd, board } = fixture(t);
  const contractPath = join(cwd, "contract.json");
  writeFileSync(contractPath, JSON.stringify(contract));
  const lock = join(board.directory, "dispatch.lock");
  const owner = join(lock, "owner.json");

  // A missing lock is inactive.
  assert.equal(board.hasActiveDispatcher(), false);
  await assert.rejects(quiet(() => taskCli(["--cwd", cwd, "add", contractPath])), /No active orchestrator/);

  // A stale heartbeat is inactive.
  mkdirSync(lock, { recursive: true });
  writeFileSync(owner, JSON.stringify({ pid: 1, heartbeat: Date.now() - 60_000 }));
  assert.equal(board.hasActiveDispatcher(), false);
  await assert.rejects(quiet(() => taskCli(["--cwd", cwd, "add", contractPath])), /No active orchestrator/);

  // A malformed owner.json is inactive.
  writeFileSync(owner, "not json");
  assert.equal(board.hasActiveDispatcher(), false);
  await assert.rejects(quiet(() => taskCli(["--cwd", cwd, "add", contractPath])), /No active orchestrator/);
  writeFileSync(owner, JSON.stringify({ heartbeat: "soon" }));
  assert.equal(board.hasActiveDispatcher(), false);

  // A fresh heartbeat allows authoring; read-only commands are unaffected.
  writeFileSync(owner, JSON.stringify({ pid: process.pid, heartbeat: Date.now() }));
  assert.equal(board.hasActiveDispatcher(), true);
  await quiet(async () => {
    await taskCli(["--cwd", cwd, "list"]);
    await taskCli(["--cwd", cwd, "--help"]);
    await taskCli(["--cwd", cwd, "add", contractPath]);
  });
  assert.equal(board.read().tasks.length, 1);
});

test("TaskBoard.add is unaffected by the authoring gate (internal path)", (t) => {
  const { board } = fixture(t);
  // No dispatch lease exists, yet the board's own mutator is unchanged.
  const task = board.add(contract);
  assert.equal(task.title, contract.title);
  assert.equal(board.read().tasks.length, 1);
});

test("editing a task bumps its revision and re-queues finished work", (t) => {
  const { board } = fixture(t);
  const task = board.add(contract);
  assert.equal(task.revision, 1);
  const edited = board.edit(task.id, { title: "Revised", checks: ["test -f other.txt"] });
  assert.equal(edited.title, "Revised");
  assert.deepEqual(edited.checks, ["test -f other.txt"]);
  assert.equal(edited.revision, 2);
  // A blocked or completed-but-unmerged task re-queues so the edit actually runs.
  board.update(task.id, (t) => { t.status = "completed"; });
  board.edit(task.id, { instructions: "new instructions" });
  assert.equal(board.get(task.id).status, "new");
  assert.equal(board.get(task.id).merge, "not-merged");
  assert.equal(board.get(task.id).revision, 3);
});

test("edit validates fields and refuses merged/cancelled tasks", (t) => {
  const { board } = fixture(t);
  const a = board.add(contract);
  const b = board.add({ ...contract, title: "Second" });
  assert.throws(() => board.edit(a.id, { checks: [] }), /checks/);
  assert.throws(() => board.edit(a.id, { title: "" }), /title/);
  assert.throws(() => board.edit(a.id, { dependencies: ["T99"] }), /Unknown dependency/);
  assert.throws(() => board.edit(a.id, { dependencies: [a.id] }), /itself/);
  board.update(a.id, (t) => { t.merge = "merged"; });
  assert.throws(() => board.edit(a.id, { title: "x" }), /merged/);
  board.update(b.id, (t) => { t.status = "cancelled"; });
  assert.throws(() => board.edit(b.id, { title: "x" }), /cancelled/);
});

test("task updates require a live dispatcher lease", async (t) => {
  const { cwd, board } = fixture(t);
  const contractPath = join(cwd, "contract.json");
  writeFileSync(contractPath, JSON.stringify(contract));
  const task = board.add(contract);
  await assert.rejects(quiet(() => taskCli(["--cwd", cwd, "update", task.id, contractPath])), /No active orchestrator/);
  const lock = join(board.directory, "dispatch.lock");
  mkdirSync(lock, { recursive: true });
  writeFileSync(join(lock, "owner.json"), JSON.stringify({ pid: process.pid, heartbeat: Date.now() }));
  await quiet(() => taskCli(["--cwd", cwd, "update", task.id, contractPath]));
  assert.equal(board.get(task.id).revision, 2);
});

test("removing a task refuses dependents and never collides with an existing id", (t) => {
  const { board } = fixture(t);
  const a = board.add(contract); // T1
  const b = board.add(contract); // T2
  board.remove(a.id);
  // The next add must clear the highest existing id, not reuse T2.
  const c = board.add(contract);
  assert.equal(c.id, "T3");
  // A task others depend on cannot be removed.
  const dependent = board.add({ ...contract, dependencies: [c.id] }); // T4
  assert.throws(() => board.remove(c.id), /depends on/);
  assert.equal(board.read().tasks.length, 3);
  // A running task cannot be removed.
  board.update(dependent.id, (t) => { t.status = "running"; });
  assert.throws(() => board.remove(dependent.id), /running/);
});

test("removeTask cleans worktrees then deletes the task", async (t) => {
  const { board } = fixture(t);
  const task = board.add(contract);
  await runTask(board, task.id, async (_, attempt) => writeFileSync(join(attempt.worktree, "result.txt"), "done"));
  const worktree = board.get(task.id).attempts[0]!.worktree;
  assert.equal(existsSync(worktree), true);
  const removed = await removeTask(board, task.id);
  assert.equal(removed.id, task.id);
  assert.equal(existsSync(worktree), false);
  assert.equal(board.read().tasks.length, 0);
});

test("forced cleanup removes a cancelled task's worktree", async (t) => {
  const { board } = fixture(t);
  const task = board.add(contract);
  await runTask(board, task.id, async (_, attempt) => writeFileSync(join(attempt.worktree, "result.txt"), "done"));
  board.cancel(task.id);
  const worktree = board.get(task.id).attempts[0]!.worktree;
  await cleanupTask(board, task.id, true);
  assert.equal(existsSync(worktree), false);
  assert.equal(board.get(task.id).attempts[0]!.cleaned, true);
});

test("task removal requires a live dispatcher lease", async (t) => {
  const { cwd, board } = fixture(t);
  const task = board.add(contract);
  await assert.rejects(quiet(() => taskCli(["--cwd", cwd, "remove", task.id])), /No active orchestrator/);
});

test("dragged screenshot paths become readable image attachments", (t) => {
  const { cwd } = fixture(t);
  const shot = join(cwd, "Screenshot 2026-09-12 at 8.43.16 pm.png");
  writeFileSync(shot, Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==", "base64"));
  const matches = findImagePaths(`fix this ${shot} please`);
  assert.equal(matches.length, 1);
  assert.equal(matches[0]!.path, shot);
  const attachment = readImageAttachment(matches[0]!.path)!;
  assert.equal(attachment.mime, "image/png");
  assert.ok(attachment.url.startsWith("data:image/png;base64,"));
  // Quoted paths and file:// URLs both resolve.
  assert.equal(findImagePaths(`'${shot}'`)[0]?.path, shot);
  assert.equal(findImagePaths(`see ${pathToFileURL(shot).href}`)[0]?.path, shot);
  // Missing files and non-images are rejected rather than attached.
  assert.equal(readImageAttachment(join(cwd, "missing.png")), undefined);
  const text = join(cwd, "notes.txt");
  writeFileSync(text, "hi");
  assert.equal(findImagePaths(`read ${text}`).length, 0);
});

test("toast renders one styled line at the requested width", () => {
  const toast = new Toast("Reloaded!", "success");
  const lines = toast.render(20);
  assert.equal(lines.length, 1);
  assert.ok(lines[0]!.startsWith("\x1b[42m\x1b[30m "));
  assert.match(stripTerminalSequences(lines[0]!), /Reloaded!/);
  assert.equal(stripTerminalSequences(lines[0]!).length, 20);
});

test("toast width leaves exactly one space on each side of its message", () => {
  const text = "Reloaded!";
  const width = toastWidth(text, 80);
  const line = new Toast(text, "success").render(width)[0]!;
  assert.equal(stripTerminalSequences(line), ` ${text} `);
});

test("each toast level renders its own fixed background and foreground", () => {
  const cases: Array<{ level: ToastLevel; style: string }> = [
    { level: "success", style: "\x1b[42m\x1b[30m" },
    { level: "warning", style: "\x1b[43m\x1b[30m" },
    { level: "error", style: "\x1b[41m\x1b[97m" },
  ];
  const backgrounds = new Set<string>();
  for (const { level, style } of cases) {
    assert.equal(TOAST_STYLES[level], style, `${level} style`);
    const line = new Toast("Heads up", level).render(20)[0]!;
    assert.ok(line.startsWith(`${style} `), `${level} should open with ${JSON.stringify(style)}`);
    // The toast resets at the end so its colour cannot bleed into the terminal.
    assert.ok(line.endsWith("\x1b[0m"), `${level} should reset`);
    backgrounds.add(style.slice(0, 5));
  }
  // All three levels use distinct background colours and never the inverse style.
  assert.equal(backgrounds.size, 3);
  assert.ok(!backgrounds.has("\x1b[7m"));
});

test("multitask shows as a title on the input border", () => {
  const child = { invalidate() {}, render: () => ["──────", "hello", "──────"] };
  const active = new RoundedDialogFrame(() => 0, undefined, "Multitask");
  active.addChild(child);
  const plain = new RoundedDialogFrame(() => 0, undefined, undefined);
  plain.addChild(child);
  const lines = active.render(24).map(stripTerminalSequences);
  assert.match(lines[0]!, /^╭─ Multitask ─{10}╮$/);
  assert.ok(!plain.render(24).map(stripTerminalSequences)[0]!.includes("Multitask"));
  for (const line of active.render(24)) assert.equal(stripTerminalSequences(line).length, 24);
});

test("queued messages render as a numbered, indented preview above the input", () => {
  const queue = { current: [{ text: "First task" }, { text: "Second\nmultiline task" }] };
  const view = new QueuedMessages(() => queue.current);
  const lines = view.render(80);
  assert.equal(stripTerminalSequences(lines[0]!), "Queue:");
  assert.equal(stripTerminalSequences(lines[1]!), "1. First task");
  // A multiline message collapses to one row.
  assert.equal(stripTerminalSequences(lines[2]!), "2. Second multiline task");
  assert.equal(lines.length, 3);
  // Every preview row carries its own styling.
  for (const line of lines) assert.match(line, /\x1b\[/);
  // An empty queue renders no rows at all.
  queue.current = [];
  assert.deepEqual(view.render(80), []);
});

test("queued messages respect the transcript padding", () => {
  const view = new QueuedMessages(() => [{ text: "Padded" }], () => 4);
  const lines = view.render(80).map(stripTerminalSequences);
  assert.equal(lines[0], "    Queue:");
  assert.equal(lines[1], "    1. Padded");
});

test("queued rows keep the same gutter on the right as on the left", () => {
  const view = new QueuedMessages(() => [{ text: "x".repeat(200) }], () => 2);
  const row = stripTerminalSequences(view.render(40)[1]!);
  // Two columns of gutter each side: 38 columns total, 36 for the content.
  assert.equal(row.length, 38);
  assert.ok(row.startsWith("  1. "));
  assert.ok(row.endsWith("…"));
});

test("queued image chips are muted like the rest of the preview", () => {
  const view = new QueuedMessages(() => [{ text: "[Image: shot.png] hello" }]);
  const line = view.render(80)[1]!;
  assert.match(line, /\[Image: shot\.png\] hello/);
  assert.ok(!line.includes("\x1b[33m"), `queued chip should not be yellow: ${JSON.stringify(line)}`);
  // The whole row uses one muted span, exactly like a plain queued message.
  const ansi = (text: string): string[] => text.match(/\x1b\[[0-9;]*m/g) ?? [];
  const plain = new QueuedMessages(() => [{ text: "hello" }]).render(80)[1]!;
  assert.deepEqual(ansi(line), ansi(plain));
});

test("panel overlay insets content from the side borders", () => {
  const child = { invalidate() {}, render: () => ["hello", "world"] };
  const panel = new PanelOverlay("Tasks", child);
  const lines = panel.render(20).map(stripTerminalSequences);
  assert.ok(lines[0]!.includes("Tasks"));
  assert.equal(lines[1], "│ hello            │");
  for (const line of lines) assert.equal(line.length, 20);
});

test("tasks view renders flat rows with derived state icons", (t) => {
  const { board } = fixture(t);
  const task = board.add(contract);
  const view = new TasksView(() => {});
  view.tasks = [task];
  assert.equal(taskIcon(task, 0), "○");
  task.status = "running";
  assert.notEqual(taskIcon(task, 0), taskIcon(task, 1));
  task.status = "completed";
  assert.match(view.render(160).join("\n"), /●.*not merged/);
  task.merge = "merged";
  assert.match(view.render(160).join("\n"), /✓.*merged → /);
});

test("tasks view empty state is mode-aware and never prints a shell command", () => {
  const normal = stripTerminalSequences(new TasksView(() => {}).render(200).join("\n"));
  const multitask = stripTerminalSequences(new TasksView(() => {}, true).render(200).join("\n"));
  assert.ok(!normal.includes("midas task add"), `normal empty state leaked a command: ${normal}`);
  assert.ok(!multitask.includes("midas task add"), `multitask empty state leaked a command: ${multitask}`);
  assert.match(normal, /No tasks/);
  assert.match(multitask, /created automatically/);
});
