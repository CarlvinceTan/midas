import test from "node:test";
import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { existsSync, mkdtempSync, mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { hostname, tmpdir } from "node:os";
import { join } from "node:path";
import { TaskBoard, git, type Attempt, type Task } from "./board.ts";
import { TaskDispatcher } from "./dispatcher.ts";
import { TRANSIENT_RETRY_BUDGET } from "./runner.ts";

// Identity for disposable fixture commits only; never modifies Git configuration.
process.env.GIT_AUTHOR_NAME = process.env.GIT_COMMITTER_NAME = "Midas tests";
process.env.GIT_AUTHOR_EMAIL = process.env.GIT_COMMITTER_EMAIL = "tests@example.invalid";

const contract = { title: "Recover me", instructions: "noop", checks: ["true"] };

function fixture(t: { after(fn: () => void): void }): { cwd: string; board: TaskBoard } {
  const cwd = mkdtempSync(join(tmpdir(), "midas-recovery-"));
  t.after(() => rmSync(cwd, { recursive: true, force: true }));
  git(cwd, "init", "-b", "main");
  writeFileSync(join(cwd, "base.txt"), "original\n");
  git(cwd, "add", ".");
  git(cwd, "commit", "-m", "fixture");
  return { cwd, board: new TaskBoard(cwd) };
}

/** True when `pid` names a live process (EPERM still means it exists). */
function alive(pid: number): boolean {
  try { process.kill(pid, 0); return true; }
  catch (error) { return (error as NodeJS.ErrnoException).code === "EPERM"; }
}

/**
 * A pid we can prove is dead: a real fixture subprocess that has already exited
 * and been reaped. Retried in case the OS immediately reuses the freed pid.
 */
function deadPid(): number {
  for (let i = 0; i < 5; i++) {
    const pid = spawnSync(process.execPath, ["-e", ""]).pid;
    if (pid && !alive(pid)) return pid;
  }
  throw new Error("could not obtain a dead pid");
}

/** Write a task lock by hand, exactly as a crashed worker would leave it. */
function plantLock(board: TaskBoard, id: string, owner?: unknown, raw = false): string {
  const path = join(board.directory, `task-${id}.lock`);
  mkdirSync(path, { recursive: true });
  if (owner !== undefined) writeFileSync(join(path, "owner.json"), raw ? String(owner) : JSON.stringify(owner));
  return path;
}

/**
 * A `running` task with a real git worktree and uncommitted bytes, as an
 * interrupted worker would leave the board. Never touches a real worker or
 * dispatcher: the only subprocess here creates the fixture repository.
 */
async function interrupted(board: TaskBoard): Promise<{ task: Task; attempt: Attempt; bytes: string }> {
  const task = board.add(contract);
  const attempt = await board.prepare(task.id);
  const bytes = "half-finished work\n";
  writeFileSync(join(attempt.worktree, "pending.txt"), bytes);
  return { task, attempt, bytes };
}

/** A dispatcher whose worker is stubbed and whose merge is a no-op. */
function dispatcherFor(board: TaskBoard, worker: () => Promise<void>, onEvent?: (message: string) => void): TaskDispatcher {
  return new TaskDispatcher(board, { lease: false, cleanup: false, merge: async () => {}, worker, onEvent });
}

test("a leftover dead-owner lock requeues exactly one replacement and preserves the interrupted worktree", async (t) => {
  const { board } = fixture(t);
  const { task, attempt, bytes } = await interrupted(board);
  const pid = deadPid();
  assert.equal(alive(pid), false, "the fixture pid must be reaped before it stands in for a crash");
  plantLock(board, task.id, { pid, host: hostname(), started: new Date().toISOString() });

  const events: string[] = [];
  let release!: () => void;
  const held = new Promise<void>((resolve) => { release = resolve; });
  let startedResolve!: () => void;
  const started = new Promise<void>((resolve) => { startedResolve = resolve; });
  let runs = 0;
  const dispatcher = dispatcherFor(board, async () => { runs += 1; startedResolve(); await held; }, (message) => events.push(message));

  await dispatcher.tick();
  await started;

  assert.equal(runs, 1, "the abandoned run is replaced exactly once");
  assert.equal(events.filter((event) => event === `${task.id}: interrupted; requeued`).length, 1);
  assert.equal(events.filter((event) => event === `${task.id}: started`).length, 1, "only one replacement starts");
  const recovered = board.get(task.id);
  assert.equal(recovered.attempts.length, 2, "the interrupted attempt and its replacement are both recorded");
  assert.equal(recovered.attempts[0]!.id, attempt.id, "the interrupted attempt is not dropped");
  assert.notEqual(recovered.attempts[1]!.id, attempt.id);
  assert.equal(recovered.attempts[0]!.cleaned, undefined, "the interrupted attempt is not marked cleaned");
  // The old worktree and its uncommitted bytes stay inspectable; nothing force-deletes them.
  assert.ok(existsSync(attempt.worktree), "the interrupted worktree is left on disk");
  assert.equal(readFileSync(join(attempt.worktree, "pending.txt"), "utf8"), bytes);

  release();
  await dispatcher.drain();
  dispatcher.stop();
  assert.equal(board.get(task.id).status, "completed");
});

test("a running task with no lock is requeued once and keeps its interrupted attempt", async (t) => {
  const { board } = fixture(t);
  const { task, attempt, bytes } = await interrupted(board);
  // No lock is planted: the crashed run never left one behind.
  assert.equal(existsSync(join(board.directory, `task-${task.id}.lock`)), false);

  const events: string[] = [];
  let release!: () => void;
  const held = new Promise<void>((resolve) => { release = resolve; });
  let startedResolve!: () => void;
  const started = new Promise<void>((resolve) => { startedResolve = resolve; });
  let runs = 0;
  const dispatcher = dispatcherFor(board, async () => { runs += 1; startedResolve(); await held; }, (message) => events.push(message));

  await dispatcher.tick();
  await started;

  assert.equal(runs, 1, "a missing lock is still treated as an abandoned run");
  assert.equal(events.filter((event) => event === `${task.id}: interrupted; requeued`).length, 1);
  assert.equal(board.get(task.id).attempts.length, 2);
  assert.equal(board.get(task.id).attempts[0]!.id, attempt.id);
  assert.ok(existsSync(attempt.worktree));
  assert.equal(readFileSync(join(attempt.worktree, "pending.txt"), "utf8"), bytes);

  release();
  await dispatcher.drain();
  dispatcher.stop();
  assert.equal(board.get(task.id).status, "completed");
});

// Every owner we cannot prove is dead must be left strictly alone.
const uncertainOwners: Array<{ name: string; plant: (board: TaskBoard, id: string) => void }> = [
  { name: "a live owner", plant: (board, id) => { plantLock(board, id, { pid: process.pid, host: hostname(), started: new Date().toISOString() }); } },
  { name: "a foreign-host owner", plant: (board, id) => { plantLock(board, id, { pid: deadPid(), host: "some-other-host.example", started: new Date().toISOString() }); } },
  { name: "a malformed owner record", plant: (board, id) => { plantLock(board, id, "not json", true); } },
  { name: "a missing owner record", plant: (board, id) => { plantLock(board, id); } },
  { name: "a non-numeric owner pid", plant: (board, id) => { plantLock(board, id, { pid: "123", host: hostname() }); } },
];

for (const { name, plant } of uncertainOwners) {
  test(`recovery does nothing when the lock has ${name}`, async (t) => {
    const { board } = fixture(t);
    const { task, attempt, bytes } = await interrupted(board);
    plant(board, task.id);

    let runs = 0;
    const dispatcher = dispatcherFor(board, async () => { runs += 1; });
    await dispatcher.tick();
    await dispatcher.drain();
    dispatcher.stop();

    assert.equal(runs, 0, "an uncertain owner must never be recovered or replaced");
    const current = board.get(task.id);
    assert.equal(current.status, "running", "the run is left for its owner to finish");
    assert.equal(current.attempts.length, 1, "no replacement attempt is recorded");
    assert.ok(existsSync(attempt.worktree));
    assert.equal(readFileSync(join(attempt.worktree, "pending.txt"), "utf8"), bytes);
  });
}

test("recovery honours a pending halt and preserves the interrupted attempt", async (t) => {
  const { board } = fixture(t);
  const { task, attempt, bytes } = await interrupted(board);
  board.pause(task.id);
  assert.equal(board.get(task.id).requestedAction, "pause");
  plantLock(board, task.id, { pid: deadPid(), host: hostname(), started: new Date().toISOString() });

  let runs = 0;
  const dispatcher = dispatcherFor(board, async () => { runs += 1; });
  await dispatcher.tick();
  await dispatcher.drain();
  dispatcher.stop();

  const recovered = board.get(task.id);
  assert.equal(recovered.status, "blocked");
  assert.equal(recovered.requestedAction, undefined, "the honoured halt clears the pending action");
  assert.equal(runs, 0, "a halt request before the crash is never replaced by a run");
  assert.equal(recovered.attempts.length, 1, "the interrupted attempt survives the halt");
  assert.ok(existsSync(attempt.worktree));
  assert.equal(readFileSync(join(attempt.worktree, "pending.txt"), "utf8"), bytes);
});

test("recovery honours a pending cancel and preserves the interrupted attempt", async (t) => {
  const { board } = fixture(t);
  const { task, attempt, bytes } = await interrupted(board);
  board.cancel(task.id);
  assert.equal(board.get(task.id).requestedAction, "cancel");
  plantLock(board, task.id, { pid: deadPid(), host: hostname(), started: new Date().toISOString() });

  let runs = 0;
  const dispatcher = dispatcherFor(board, async () => { runs += 1; });
  await dispatcher.tick();
  await dispatcher.drain();
  dispatcher.stop();

  const recovered = board.get(task.id);
  assert.equal(recovered.status, "cancelled");
  assert.equal(recovered.requestedAction, undefined, "the honoured cancel clears the pending action");
  assert.equal(runs, 0, "a cancel request before the crash is never replaced by a run");
  assert.equal(recovered.attempts.length, 1, "the interrupted attempt survives the cancel");
  assert.ok(existsSync(attempt.worktree));
  assert.equal(readFileSync(join(attempt.worktree, "pending.txt"), "utf8"), bytes);
});

test("repeated interruptions are bounded and the task is parked instead of looping", async (t) => {
  const { board } = fixture(t);
  const { task, attempt, bytes } = await interrupted(board);
  board.update(task.id, (t) => { t.consecutiveFailures = TRANSIENT_RETRY_BUDGET; });
  plantLock(board, task.id, { pid: deadPid(), host: hostname(), started: new Date().toISOString() });

  let runs = 0;
  const dispatcher = dispatcherFor(board, async () => { runs += 1; });
  await dispatcher.tick();
  await dispatcher.drain();
  dispatcher.stop();

  const recovered = board.get(task.id);
  assert.equal(runs, 0, "an exhausted retry budget stops automatic replacement");
  assert.equal(recovered.status, "blocked");
  assert.equal(recovered.attempts.length, 1, "no further attempt is recorded");
  assert.ok(existsSync(attempt.worktree));
  assert.equal(readFileSync(join(attempt.worktree, "pending.txt"), "utf8"), bytes);
});
