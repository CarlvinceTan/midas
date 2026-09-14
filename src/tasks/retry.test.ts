import test from "node:test";
import assert from "node:assert/strict";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { TaskBoard, git } from "./board.ts";
import { runTask, mergeTask, classifyFailure } from "./runner.ts";
import { TaskDispatcher } from "./dispatcher.ts";

// Identity for disposable fixture commits only; never modifies Git configuration.
process.env.GIT_AUTHOR_NAME = process.env.GIT_COMMITTER_NAME = "Midas tests";
process.env.GIT_AUTHOR_EMAIL = process.env.GIT_COMMITTER_EMAIL = "tests@example.invalid";

function fixture(t: { after(fn: () => void): void }): { cwd: string; board: TaskBoard } {
  const cwd = mkdtempSync(join(tmpdir(), "midas-retry-"));
  t.after(() => rmSync(cwd, { recursive: true, force: true }));
  git(cwd, "init", "-b", "main");
  writeFileSync(join(cwd, "base.txt"), "original\n");
  git(cwd, "add", ".");
  git(cwd, "commit", "-m", "fixture");
  return { cwd, board: new TaskBoard(cwd) };
}

const contract = { title: "Implement feature", instructions: "Add result.txt", checks: ["test -f result.txt"] };

test("transient failures re-queue and count up until the worker succeeds, then merge", async (t) => {
  const { board } = fixture(t);
  const task = board.add(contract);
  let calls = 0;
  const worker = async (_task: unknown, attempt: { worktree: string }): Promise<void> => {
    calls += 1;
    if (calls <= 2) throw new TypeError("fetch failed");
    writeFileSync(join(attempt.worktree, "result.txt"), "done\n");
  };

  await assert.rejects(runTask(board, task.id, worker), /fetch failed/);
  assert.equal(board.get(task.id).status, "new", "a transient failure re-queues the task");
  assert.equal(board.get(task.id).consecutiveFailures, 1);
  assert.equal(board.get(task.id).mergeBlocked, undefined);
  assert.match(board.get(task.id).detail ?? "", /retry 1\/2/);

  await assert.rejects(runTask(board, task.id, worker), /fetch failed/);
  assert.equal(board.get(task.id).status, "new");
  assert.equal(board.get(task.id).consecutiveFailures, 2);
  assert.match(board.get(task.id).detail ?? "", /retry 2\/2/);

  await runTask(board, task.id, worker);
  assert.equal(calls, 3);
  assert.equal(board.get(task.id).status, "completed");
  assert.equal(board.get(task.id).consecutiveFailures, undefined, "the count resets on success");
  assert.match(board.get(task.id).detail ?? "", /2 retries/);
  await mergeTask(board, task.id);
  assert.equal(board.get(task.id).merge, "merged");
});

test("a deterministic failure blocks after one attempt with no re-queue", async (t) => {
  const { board } = fixture(t);
  const task = board.add(contract);
  await assert.rejects(runTask(board, task.id, async () => {}), /Check failed/);
  const state = board.get(task.id);
  assert.equal(state.status, "blocked");
  assert.equal(state.attempts.length, 1);
  assert.equal(state.consecutiveFailures, undefined);
  assert.match(state.detail ?? "", /Check failed/);
});

test("a worker-reported blocker is deterministic, not retried", async (t) => {
  const { board } = fixture(t);
  const task = board.add(contract);
  await assert.rejects(
    runTask(board, task.id, async () => { throw new Error("Worker did not report completion\nI need a decision"); }),
    /did not report completion/,
  );
  assert.equal(board.get(task.id).status, "blocked");
  assert.equal(board.get(task.id).attempts.length, 1);
});

test("a transient failure past the budget is blocked with a clear detail", async (t) => {
  const { board } = fixture(t);
  const task = board.add(contract);
  const worker = async (): Promise<void> => { throw new TypeError("fetch failed"); };

  await assert.rejects(runTask(board, task.id, worker));
  assert.equal(board.get(task.id).status, "new");
  await assert.rejects(runTask(board, task.id, worker));
  assert.equal(board.get(task.id).status, "new");
  await assert.rejects(runTask(board, task.id, worker), /fetch failed/);

  const state = board.get(task.id);
  assert.equal(state.status, "blocked");
  assert.equal(state.attempts.length, 3);
  assert.equal(state.consecutiveFailures, 3);
  assert.match(state.detail ?? "", /retry budget exhausted/);
  assert.match(state.detail ?? "", /fetch failed/);
});

test("halt and cancel are never retried as transient failures", async (t) => {
  const { board } = fixture(t);
  const halted = board.add(contract);
  await runTask(board, halted.id, async () => { board.pause(halted.id); throw new TypeError("fetch failed"); });
  assert.equal(board.get(halted.id).status, "blocked");
  assert.equal(board.get(halted.id).consecutiveFailures, undefined);

  const cancelled = board.add(contract);
  await runTask(board, cancelled.id, async () => { board.cancel(cancelled.id); throw new TypeError("fetch failed"); });
  assert.equal(board.get(cancelled.id).status, "cancelled");
  assert.equal(board.get(cancelled.id).consecutiveFailures, undefined);
});

test("the failure classifier separates transient infrastructure from deterministic work", () => {
  const transient = [
    "fetch failed",
    "Backend did not create a worker session",
    "Worker event stream closed",
    "Worker exceeded 60 minutes",
    "connect ECONNRESET 127.0.0.1:4096",
    "fatal: unable to access 'https://example.invalid/repo/': Could not resolve host: example.invalid",
    "RPC failed; curl 56 early EOF",
    "socket hang up",
  ];
  for (const message of transient) assert.equal(classifyFailure(new Error(message)), "transient", message);

  const deterministic = [
    "Check failed (1): test -f result.txt",
    "Worker did not report completion\nI could not finish because a file was missing",
    "Worker needs interactive input; inspect its session before retrying",
    "Worker changed HEAD or branch; refusing automatic checkpoint",
    "Checks modified task files; refusing to checkpoint unvalidated changes",
    "Commit hooks changed validated content",
    "Worktree changed during commit; inspect before retrying",
  ];
  for (const message of deterministic) assert.equal(classifyFailure(new Error(message)), "deterministic", message);

  // Unrecognised failures are parked for a human rather than retried blindly.
  assert.equal(classifyFailure(new Error("something entirely unexpected")), "deterministic");
  assert.equal(classifyFailure("plain string failure"), "deterministic");
});

test("the dispatcher re-dispatches a transiently failed task without a human", async (t) => {
  const { board } = fixture(t);
  const task = board.add(contract);
  let calls = 0;
  const retryDetails: string[] = [];
  const dispatcher = new TaskDispatcher(board, {
    lease: false,
    intervalMs: 5,
    concurrency: 1,
    worker: async (_task, attempt) => {
      calls += 1;
      if (calls === 1) throw new TypeError("fetch failed");
      writeFileSync(join(attempt.worktree, "result.txt"), "done\n");
    },
    onEvent: (message) => {
      if (/fetch failed/.test(message)) retryDetails.push(board.get(task.id).detail ?? "");
    },
  });

  for (let i = 0; i < 30 && board.get(task.id).status !== "completed"; i++) {
    await dispatcher.tick();
    await dispatcher.drain();
    await new Promise((resolve) => setTimeout(resolve, 10));
  }
  dispatcher.stop();

  assert.ok(calls >= 2, "the transient failure was retried without human intervention");
  assert.equal(board.get(task.id).status, "completed");
  assert.match(retryDetails[0] ?? "", /retry 1\/2/);
});
