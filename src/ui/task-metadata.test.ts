import assert from "node:assert/strict";
import { mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";
import { initTheme as initPiTheme } from "@earendil-works/pi-coding-agent";
import type { Terminal } from "@earendil-works/pi-tui";
import { initTheme } from "../theme/theme.ts";
import { Transcript } from "../state/transcript.ts";
import type { Attempt, Task, TaskBoard } from "../tasks/board.ts";
import { MidasApp, MetadataBackoff, taskMetaSnapshot, taskMetaWriteAllowed } from "./app.ts";

initPiTheme(undefined, false);
initTheme(undefined);

class TestTerminal implements Terminal {
  columns = 120;
  rows = 40;
  kittyProtocolActive = false;
  started = false;

  start(): void { this.started = true; }
  stop(): void { this.started = false; }
  async drainInput(): Promise<void> {}
  write(): void {}
  moveBy(): void {}
  hideCursor(): void {}
  showCursor(): void {}
  clearLine(): void {}
  clearFromCursor(): void {}
  clearScreen(): void {}
  setTitle(): void {}
  setProgress(): void {}
}

function deferred<T>(): { promise: Promise<T>; resolve(value: T): void } {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => { resolve = done; });
  return { promise, resolve };
}

interface MetaDoubleOptions {
  generateTaskTitle?: () => Promise<string | undefined>;
  generateTaskStatus?: () => Promise<string | undefined>;
}

/** A MidasApp in orchestrator mode whose helper agents are test doubles. */
function makeApp(options: MetaDoubleOptions = {}): { app: MidasApp; transcript: Transcript } {
  const transcript = new Transcript();
  const controller = {
    id: undefined,
    title: undefined,
    transcript,
    listAgents: async () => [],
    listModels: async () => [],
    listCommands: async () => [],
    mcpServerNames: async () => [],
    defaultModel: async () => undefined,
    prompt: async () => {},
    setAgent(): void {},
    setModel(): void {},
    setVariant(): void {},
    dispose(): void {},
    generateTaskTitle: options.generateTaskTitle ?? (async () => undefined),
    generateTaskStatus: options.generateTaskStatus ?? (async () => undefined),
  };
  const app = new MidasApp({
    controller: controller as never,
    cwd: process.cwd(),
    settings: { tuiMode: "regular", voicePreload: false },
    agent: "orchestrator",
    model: { providerID: "openai", modelID: "gpt-5.6-sol", name: "GPT", providerName: "OpenAI" },
    terminal: new TestTerminal(),
  });
  return { app, transcript };
}

interface MetaAppInternals {
  refreshTaskMeta(board: TaskBoard): Promise<void>;
  pollDispatchLog(path: string, board: TaskBoard): void;
  progressSignatures: Map<string, string>;
  taskMetaBackoff: MetadataBackoff;
  queue: Array<{ text: string }>;
}

function internals(app: MidasApp): MetaAppInternals {
  return app as unknown as MetaAppInternals;
}

/** In-memory board: `read`/`update` see the same task objects mutations touch. */
function fakeBoard(tasks: Task[]): TaskBoard {
  return {
    read: () => ({ version: 1, tasks }),
    update: (id: string, fn: (task: Task) => void) => {
      const task = tasks.find((candidate) => candidate.id === id);
      if (!task) throw new Error(`Unknown task: ${id}`);
      fn(task);
    },
  } as unknown as TaskBoard;
}

function task(overrides: Partial<Task> = {}): Task {
  return {
    id: "T1",
    title: "Implement feature",
    instructions: "Add result.txt",
    checks: ["true"],
    status: "new",
    merge: "not-merged",
    target: "main",
    revision: 1,
    attempts: [],
    ...overrides,
  };
}

function attempt(index = 0): Attempt {
  return { id: `a${index}`, worktree: `/tmp/w${index}`, branch: `midas/t${index}`, base: "main" };
}

/** Let the fire-and-forget metadata pass settle before asserting. */
async function flush(): Promise<void> {
  await new Promise<void>((resolve) => setImmediate(resolve));
  await new Promise<void>((resolve) => setImmediate(resolve));
}

test("taskMetaWriteAllowed rejects revision, state, attempt, and identity changes", () => {
  const snapshot = taskMetaSnapshot(task());
  assert.equal(taskMetaWriteAllowed(snapshot, task()), true);
  assert.equal(taskMetaWriteAllowed(snapshot, task({ revision: 2 })), false);
  assert.equal(taskMetaWriteAllowed(snapshot, task({ status: "blocked" })), false);
  assert.equal(taskMetaWriteAllowed(snapshot, task({ merge: "merged" })), false);
  assert.equal(taskMetaWriteAllowed(snapshot, task({ attempts: [attempt()] })), false);
  assert.equal(taskMetaWriteAllowed(snapshot, task({ id: "T2" })), false);
});

test("metadata backoff doubles the delay and caps it", () => {
  let now = 0;
  const backoff = new MetadataBackoff(100, 400, () => now);
  assert.equal(backoff.ready(), true);

  backoff.recordFailure();
  assert.equal(backoff.ready(), false);
  now += 100;
  assert.equal(backoff.ready(), true);

  backoff.recordFailure();
  now += 100;
  assert.equal(backoff.ready(), false, "the second failure must wait longer");
  now += 100;
  assert.equal(backoff.ready(), true);

  const capped = new MetadataBackoff(100, 400, () => now);
  for (let i = 0; i < 20; i += 1) capped.recordFailure();
  now += 399;
  assert.equal(capped.ready(), false, "the delay must not grow past the cap");
  now += 1;
  assert.equal(capped.ready(), true);

  capped.reset();
  assert.equal(capped.ready(), true);
});

test("current helper results are written to the board", async () => {
  const { app } = makeApp({
    generateTaskTitle: async () => "Generated title",
    generateTaskStatus: async () => "running checks",
  });
  const tasks = [task()];
  const board = fakeBoard(tasks);

  await internals(app).refreshTaskMeta(board);

  assert.equal(tasks[0]!.title, "Generated title");
  assert.equal(tasks[0]!.titledRevision, 1);
  assert.equal(tasks[0]!.progress, "running checks");
  app.quit();
});

test("a delayed title cannot overwrite a newer edit", async () => {
  const pending = deferred<string | undefined>();
  const { app } = makeApp({ generateTaskTitle: () => pending.promise });
  const tasks = [task()];
  const board = fakeBoard(tasks);

  const running = internals(app).refreshTaskMeta(board);
  // The contract is edited while the title request is in flight.
  tasks[0]!.title = "Human title";
  tasks[0]!.revision = 2;
  pending.resolve("Stale generated title");
  await running;

  assert.equal(tasks[0]!.title, "Human title");
  assert.equal(tasks[0]!.titledRevision, undefined, "the stale revision must stay untitled");
  app.quit();
});

for (const mutation of [
  { name: "cancel", apply: (t: Task) => { t.status = "cancelled"; } },
  { name: "merge", apply: (t: Task) => { t.merge = "merged"; } },
]) {
  test(`a delayed title cannot overwrite a concurrent ${mutation.name}`, async () => {
    const pending = deferred<string | undefined>();
    const { app } = makeApp({ generateTaskTitle: () => pending.promise });
    const tasks = [task()];
    const board = fakeBoard(tasks);

    const running = internals(app).refreshTaskMeta(board);
    mutation.apply(tasks[0]!);
    pending.resolve("Stale generated title");
    await running;

    assert.notEqual(tasks[0]!.title, "Stale generated title");
    assert.equal(tasks[0]!.titledRevision, undefined);
    app.quit();
  });
}

test("a delayed title survives task removal", async () => {
  const pending = deferred<string | undefined>();
  const { app } = makeApp({ generateTaskTitle: () => pending.promise });
  const tasks = [task()];
  const board = fakeBoard(tasks);

  const running = internals(app).refreshTaskMeta(board);
  tasks.splice(0, 1);
  pending.resolve("Stale generated title");
  await assert.doesNotReject(running);
  assert.equal(tasks.length, 0);
  app.quit();
});

test("a delayed status cannot overwrite a newer stage", async () => {
  const pending = deferred<string | undefined>();
  const { app } = makeApp({ generateTaskStatus: () => pending.promise });
  const tasks = [task({ titledRevision: 1 })];
  const board = fakeBoard(tasks);

  const running = internals(app).refreshTaskMeta(board);
  // The task is cancelled while the status request is in flight.
  tasks[0]!.status = "cancelled";
  tasks[0]!.progress = undefined;
  pending.resolve("Stale status");
  await running;

  assert.equal(tasks[0]!.progress, undefined);
  assert.equal(internals(app).progressSignatures.has("T1"), false, "a stale stage must be regenerated");
  app.quit();
});

test("a rejected helper never escapes as an unhandled rejection", async () => {
  const unhandled: unknown[] = [];
  const onUnhandled = (reason: unknown): void => { unhandled.push(reason); };
  process.on("unhandledRejection", onUnhandled);
  try {
    const { app } = makeApp({ generateTaskTitle: () => Promise.reject(new Error("helper rejected")) });
    const tasks = [task()];
    const board = fakeBoard(tasks);

    await assert.doesNotReject(internals(app).refreshTaskMeta(board));
    await flush();
    assert.deepEqual(unhandled, []);
    app.quit();
  } finally {
    process.off("unhandledRejection", onUnhandled);
  }
});

test("a helper timeout is contained and polling resumes after a bounded backoff", async () => {
  let now = 0;
  let calls = 0;
  const unhandled: unknown[] = [];
  const onUnhandled = (reason: unknown): void => { unhandled.push(reason); };
  process.on("unhandledRejection", onUnhandled);
  try {
    const { app } = makeApp({
      generateTaskTitle: () => {
        calls += 1;
        if (calls === 1) {
          return new Promise<string | undefined>((_resolve, reject) => {
            setTimeout(() => reject(new Error("helper timed out")), 0);
          });
        }
        return Promise.resolve("Recovered title");
      },
    });
    internals(app).taskMetaBackoff = new MetadataBackoff(500, 5_000, () => now);
    const tasks = [task()];
    const board = fakeBoard(tasks);

    await internals(app).refreshTaskMeta(board);
    await flush();
    assert.equal(calls, 1);
    assert.deepEqual(unhandled, [], "a helper timeout must not become an unhandled rejection");

    // The backoff pauses the next attempt, then lets polling resume.
    await internals(app).refreshTaskMeta(board);
    assert.equal(calls, 1, "the backoff must not hammer a failing helper");
    now += 500;
    await internals(app).refreshTaskMeta(board);
    assert.equal(calls, 2, "polling must resume after the backoff");
    assert.equal(tasks[0]!.title, "Recovered title");
    app.quit();
  } finally {
    process.off("unhandledRejection", onUnhandled);
  }
});

test("an unchanged dispatch log still surfaces a newly blocked task once", async () => {
  const dir = mkdtempSync(join(tmpdir(), "midas-task-meta-"));
  const log = join(dir, "dispatch.log");
  writeFileSync(log, "2026-01-01T00:00:00Z task started\n");

  const { app, transcript } = makeApp();
  // Keep the debounced queue flush from draining the audit prompt mid-test.
  transcript.setPhase("busy");
  const tasks = [task()];
  const board = fakeBoard(tasks);
  const poll = internals(app).pollDispatchLog.bind(app);

  poll(log, board);
  await flush();
  assert.equal(noticeCount(transcript), 0);

  // A task becomes blocked without the daemon writing anything new to the log.
  tasks[0]!.status = "clarify";
  poll(log, board);
  await flush();
  assert.equal(noticeCount(transcript), 1);
  assert.equal(internals(app).queue.length, 1);

  // A quiet follow-up tick must not repeat the notice or the queued prompt.
  poll(log, board);
  await flush();
  assert.equal(noticeCount(transcript), 1);
  assert.equal(internals(app).queue.length, 1);
  app.quit();
});

function noticeCount(transcript: Transcript): number {
  return transcript.messages.filter((message) => message.notice).length;
}
