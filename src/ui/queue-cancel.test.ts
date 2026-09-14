import assert from "node:assert/strict";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { after, test } from "node:test";
import type { Editor, Terminal } from "@earendil-works/pi-tui";
import type { PromptAttachment } from "../lib/attachments.ts";
import type { FileChip, FrozenFilePayload } from "./app.ts";

/**
 * Midas UI queue-cancellation tests. These drive the real global key handler,
 * the real queue-hold/admission logic and the real prompt/steer paths against a
 * fake controller, fake terminal and synthetic files. They prove that Esc (and
 * Ctrl+C, /undo, shell cancellation) holds queued follow-ups instead of leaking
 * them into the context, that only explicit work can release the hold, and that
 * cmd+enter dequeues exactly one item. Nothing here calls a real model, server,
 * clipboard, board or goal tool.
 */

// Isolate every path the module graph could consult before it is imported.
const sandbox = mkdtempSync(join(tmpdir(), "midas-queue-cancel-"));
const ISOLATED_ENV = [
  "HOME",
  "MIDAS_CONFIG_DIR",
  "PI_CONFIG_DIR",
  "XDG_CONFIG_HOME",
  "XDG_DATA_HOME",
  "XDG_CACHE_HOME",
  "XDG_STATE_HOME",
  "OPENCODE_GOAL_STATE_PATH",
  "MIDAS_NO_UPDATE",
] as const;
const savedEnv = new Map<string, string | undefined>();
for (const key of ISOLATED_ENV) savedEnv.set(key, process.env[key]);
process.env.HOME = join(sandbox, "home");
process.env.MIDAS_CONFIG_DIR = join(sandbox, "midas");
process.env.PI_CONFIG_DIR = join(sandbox, "pi");
process.env.XDG_CONFIG_HOME = join(sandbox, "config");
process.env.XDG_DATA_HOME = join(sandbox, "data");
process.env.XDG_CACHE_HOME = join(sandbox, "cache");
process.env.XDG_STATE_HOME = join(sandbox, "state");
process.env.OPENCODE_GOAL_STATE_PATH = join(sandbox, "goals.json");
process.env.MIDAS_NO_UPDATE = "1";

const { MidasApp } = await import("./app.ts");
const { Transcript } = await import("../state/transcript.ts");
const { readSessionState, writeSessionState } = await import("../lib/session-state.ts");
const { initTheme } = await import("../theme/theme.ts");
const { initTheme: initPiTheme } = await import("@earendil-works/pi-coding-agent");

initPiTheme(undefined, false);
initTheme(undefined);

const workDir = join(sandbox, "files");
mkdirSync(workDir, { recursive: true });

after(() => {
  for (const [key, value] of savedEnv) {
    if (value === undefined) delete process.env[key];
    else process.env[key] = value;
  }
  rmSync(sandbox, { recursive: true, force: true });
});

// --- fixtures -------------------------------------------------------------

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

interface PromptCall {
  text: string;
  attachments: PromptAttachment[];
}

interface QueueShape {
  text: string;
  attachments: PromptAttachment[];
  files?: FileChip[];
  frozenFiles?: FrozenFilePayload[];
  needsReattach?: FileChip[];
}

interface DraftShape {
  text: string;
  attachments?: Array<{ marker: string; path: string }>;
  files?: FileChip[];
}

type AbortMode = "idle" | "throw" | "silent";

interface AppInternals {
  editor: Editor;
  queue: QueueShape[];
  queueHold: boolean;
  queueFlushTimer: ReturnType<typeof setTimeout> | undefined;
  queueDispatchedAt: number;
  maybeGenerateTitle(...args: unknown[]): void;
  fail(message: string): void;
  warn(message: string): void;
  handleGlobalKey(data: string): { consume?: boolean } | undefined;
  syncQueueDelivery(): void;
  maybeFlushQueue(): void;
  scheduleQueueFlush(): void;
  handleSubmit(text: string, images?: Array<{ marker: string; path: string }>, files?: FileChip[]): Promise<void>;
  restoreSessionShellState(id: string | undefined): Promise<void>;
  persistSessionState(): void;
  currentDraft(): DraftShape;
  isRunActive(): boolean;
  holdQueueDelivery(): void;
  runShellCommand(command: string, exclude: boolean): Promise<void>;
}

interface Harness {
  controller: Record<string, unknown>;
  transcript: InstanceType<typeof Transcript>;
  editor: Editor;
  promptCalls: PromptCall[];
  runCommands: Array<{ name: string; args: string }>;
  failures: string[];
  warnings: string[];
  abortCalls: number;
  internals: AppInternals;
  /** Set the abort behaviour for the next cancellation. */
  setAbortMode(mode: AbortMode): void;
  setPhaseOnPrompt(value: boolean): void;
  /**
   * Record the controller-side proof that the re-armed explicit turn genuinely
   * completed. Without it the queue stays held, mirroring the real controller's
   * fail-closed contract; the proof is consumed on release.
   */
  proveCompletion(): void;
  close(): void;
}

function makeHarness(
  options: { busy?: boolean; phaseOnPrompt?: boolean; abort?: AbortMode } = {},
): Harness {
  const transcript = new Transcript();
  if (options.busy) transcript.setPhase("busy");
  const promptCalls: PromptCall[] = [];
  const runCommands: Array<{ name: string; args: string }> = [];
  const failures: string[] = [];
  const warnings: string[] = [];
  let abortCalls = 0;
  let abortMode: AbortMode = options.abort ?? "idle";
  let phaseOnPrompt = options.phaseOnPrompt ?? true;
  // A completion proof is only ever produced by `proveCompletion`, so a bare
  // busy/idle or stale cancellation idle can never release the hold.
  let completionProven = false;

  const controller: Record<string, unknown> = {
    id: "session-1",
    title: undefined,
    transcript,
    async prompt(text: string, attachments: PromptAttachment[] = []): Promise<void> {
      promptCalls.push({ text, attachments });
      if (phaseOnPrompt) transcript.setPhase("busy");
    },
    async runCommand(name: string, args: string): Promise<void> {
      runCommands.push({ name, args });
      transcript.setPhase("busy");
    },
    async abort(): Promise<void> {
      abortCalls += 1;
      if (abortMode === "throw") throw new Error("abort failed");
      if (abortMode === "idle") transcript.setPhase("idle");
    },
    // Consumed once, exactly like the real SessionController.
    consumeQueueCompletion(): boolean {
      if (!completionProven) return false;
      completionProven = false;
      return true;
    },
    setCwd(): void {},
    setAgent(): void {},
    setModel(): void {},
    setVariant(): void {},
    dispose(): void {},
    async listAgents(): Promise<unknown[]> { return []; },
    async listModels(): Promise<unknown[]> { return []; },
    async listCommands(): Promise<unknown[]> { return []; },
    async mcpServerNames(): Promise<string[]> { return []; },
    async defaultModel(): Promise<undefined> { return undefined; },
  };

  const app = new MidasApp({
    controller: controller as never,
    cwd: workDir,
    settings: { tuiMode: "regular", voicePreload: false },
    agent: "main",
    model: { providerID: "test", modelID: "test-model", name: "Test Model", providerName: "Test" },
    terminal: new TestTerminal(),
  });

  const raw = app as unknown as AppInternals;
  raw.maybeGenerateTitle = () => {};
  raw.fail = (message: string) => { failures.push(message); };
  raw.warn = (message: string) => { warnings.push(message); };

  return {
    controller,
    transcript,
    editor: raw.editor,
    promptCalls,
    runCommands,
    failures,
    warnings,
    get abortCalls(): number { return abortCalls; },
    internals: raw,
    setAbortMode(mode: AbortMode): void { abortMode = mode; },
    setPhaseOnPrompt(value: boolean): void { phaseOnPrompt = value; },
    proveCompletion(): void { completionProven = true; },
    close(): void {
      try { app.quit(); } catch { /* teardown is best-effort */ }
    },
  } as Harness;
}

let fileCounter = 0;
function makeFile(content: string): string {
  const path = join(workDir, `qfile-${++fileCounter}.txt`);
  writeFileSync(path, content);
  return path;
}

const ESC = "\x1b";
const CTRL_C = "\x03";
const SUPER_ENTER = "\x1b[13;9u";

async function flush(): Promise<void> {
  for (let i = 0; i < 3; i++) await new Promise<void>((resolve) => setImmediate(resolve));
}

/** Wait past the 400ms debounce so a scheduled flush would have fired. */
async function settleFlush(): Promise<void> {
  await new Promise<void>((resolve) => setTimeout(resolve, 450));
  await flush();
}

async function enqueue(h: Harness, text: string): Promise<void> {
  h.transcript.setPhase("busy");
  await h.internals.handleSubmit(text);
}

/** Queue a real file prompt through the real prepare/freeze path. */
async function enqueueFile(h: Harness, path: string): Promise<void> {
  h.transcript.setPhase("busy");
  h.editor.insertFileAttachment(path);
  await h.internals.handleSubmit(h.editor.getText());
}

function idle(h: Harness): void {
  h.transcript.setPhase("idle");
  h.internals.syncQueueDelivery();
}

function busy(h: Harness): void {
  h.transcript.setPhase("busy");
  h.internals.syncQueueDelivery();
}

function localUserMessages(h: Harness): number {
  return h.transcript.messages.filter((message) => message.role === "user").length;
}

// --- cancellation holds the queue -----------------------------------------

test("Esc holds a busy session's queued follow-ups and a draft through repeated stale idles", async (t) => {
  const h = makeHarness({ busy: true });
  t.after(() => h.close());

  await enqueue(h, "queued A");
  await enqueue(h, "queued B");
  assert.equal(h.internals.queue.length, 2);
  h.editor.setText("unsent draft");

  h.internals.handleGlobalKey(ESC);
  await flush();
  assert.equal(h.abortCalls, 1, "Esc aborts exactly once");
  assert.equal(h.internals.queueHold, true, "the queue is held");
  assert.equal(h.transcript.phase, "idle", "abort's idle landed");

  // Delayed/repeated idle and status churn must not release the hold.
  for (let i = 0; i < 4; i++) h.internals.syncQueueDelivery();
  busy(h);
  idle(h);
  idle(h);
  await settleFlush();

  assert.equal(h.promptCalls.length, 0, "nothing was sent");
  assert.equal(localUserMessages(h), 0, "nothing was added to the context");
  assert.deepEqual(h.internals.queue.map((item) => item.text), ["queued A", "queued B"], "FIFO order is intact");
  assert.equal(h.editor.getText(), "unsent draft", "the draft survives");
  assert.equal(h.internals.queueHold, true, "the hold survives stale events");
});

test("a failed abort leaves the queue held and sends nothing", async (t) => {
  const h = makeHarness({ busy: true, abort: "throw" });
  t.after(() => h.close());

  await enqueue(h, "A");
  await enqueue(h, "B");
  h.internals.handleGlobalKey(ESC);
  await flush();
  assert.equal(h.abortCalls, 1);

  // The server later reports idle anyway (a stale/recovering status).
  idle(h);
  await settleFlush();

  assert.equal(h.promptCalls.length, 0);
  assert.deepEqual(h.internals.queue.map((item) => item.text), ["A", "B"]);
  assert.equal(h.internals.queueHold, true);
});

test("Esc during the idle settle window cancels the armed flush", async (t) => {
  const h = makeHarness({ busy: true });
  t.after(() => h.close());

  await enqueue(h, "A");
  await enqueue(h, "B");
  idle(h);
  assert.notEqual(h.internals.queueFlushTimer, undefined, "a flush is armed after a natural completion");

  h.internals.handleGlobalKey(ESC);
  assert.equal(h.internals.queueFlushTimer, undefined, "Esc cancels the armed flush");
  assert.equal(h.internals.queueHold, true);
  await settleFlush();

  assert.equal(h.promptCalls.length, 0);
  assert.deepEqual(h.internals.queue.map((item) => item.text), ["A", "B"]);
});

// --- natural completion still works ---------------------------------------

test("natural completion dispatches exactly one item and a transient idle cannot drain the rest", async (t) => {
  const h = makeHarness({ busy: true });
  t.after(() => h.close());

  await enqueue(h, "A");
  await enqueue(h, "B");
  // The run genuinely completes (no cancellation involved).
  idle(h);
  await settleFlush();
  assert.deepEqual(h.promptCalls.map((call) => call.text), ["A"], "one follow-up is dispatched");
  assert.deepEqual(h.internals.queue.map((item) => item.text), ["B"]);

  // A transient between-step idle before A's user message lands must not drain B.
  busy(h);
  idle(h);
  await settleFlush();
  assert.equal(h.promptCalls.length, 1, "the admission guard blocks the transient idle");

  // Once A's user message lands, the next genuine idle dispatches B.
  h.transcript.addLocalUserMessage("A", false);
  idle(h);
  await settleFlush();
  assert.deepEqual(h.promptCalls.map((call) => call.text), ["A", "B"]);
  assert.deepEqual(h.internals.queue, []);
});

// --- cmd+enter explicit dequeue -------------------------------------------

test("empty cmd+enter consumes exactly one item while busy, then the next on a second press", async (t) => {
  const h = makeHarness({ busy: true });
  t.after(() => h.close());

  await enqueue(h, "A");
  await enqueue(h, "B");

  h.internals.handleGlobalKey(SUPER_ENTER);
  await flush();
  assert.deepEqual(h.promptCalls.map((call) => call.text), ["A"]);
  assert.deepEqual(h.internals.queue.map((item) => item.text), ["B"], "only the top item leaves");

  h.internals.handleGlobalKey(SUPER_ENTER);
  await flush();
  assert.deepEqual(h.promptCalls.map((call) => call.text), ["A", "B"]);
  assert.deepEqual(h.internals.queue, []);
});

test("empty cmd+enter dequeues the top item even after a cancelled idle", async (t) => {
  const h = makeHarness({ busy: true });
  t.after(() => h.close());

  await enqueue(h, "A");
  await enqueue(h, "B");
  h.internals.handleGlobalKey(ESC);
  await flush();
  assert.equal(h.internals.queueHold, true);

  h.internals.handleGlobalKey(SUPER_ENTER);
  await flush();
  assert.deepEqual(h.promptCalls.map((call) => call.text), ["A"], "the held top item is delivered");
  assert.deepEqual(h.internals.queue.map((item) => item.text), ["B"], "the rest stay queued");

  // The explicit fresh run re-arms delivery; a second press steers B.
  busy(h);
  h.internals.handleGlobalKey(SUPER_ENTER);
  await flush();
  assert.deepEqual(h.promptCalls.map((call) => call.text), ["A", "B"]);
  assert.deepEqual(h.internals.queue, []);
});

test("non-empty cmd+enter consumes no queue item", async (t) => {
  const h = makeHarness({ busy: true });
  t.after(() => h.close());

  await enqueue(h, "A");
  await enqueue(h, "B");

  h.editor.setText("typed steer");
  h.internals.handleGlobalKey(SUPER_ENTER);
  await flush();
  assert.deepEqual(h.promptCalls.map((call) => call.text), ["typed steer"]);
  assert.deepEqual(h.internals.queue.map((item) => item.text), ["A", "B"], "the queue is untouched");

  // Same after a cancellation while idle: typed text becomes fresh work.
  h.internals.handleGlobalKey(ESC);
  await flush();
  h.editor.setText("fresh after cancel");
  h.internals.handleGlobalKey(SUPER_ENTER);
  await flush();
  assert.equal(h.promptCalls.at(-1)?.text, "fresh after cancel");
  assert.deepEqual(h.internals.queue.map((item) => item.text), ["A", "B"]);
});

test("empty enter, repeated Esc and key releases never consume the queue", async (t) => {
  const h = makeHarness({ busy: true });
  t.after(() => h.close());

  await enqueue(h, "A");
  await enqueue(h, "B");

  await h.internals.handleSubmit("");
  h.internals.handleGlobalKey(ESC);
  h.internals.handleGlobalKey(ESC);
  // A Kitty super+enter release and an escape release must not dequeue.
  h.internals.handleGlobalKey("\x1b[13;9:3u");
  h.internals.handleGlobalKey("\x1b[27;1:3u");
  await settleFlush();

  assert.equal(h.promptCalls.length, 0);
  assert.deepEqual(h.internals.queue.map((item) => item.text), ["A", "B"]);
});

// --- rearm after explicit work --------------------------------------------

test("a new explicit run releases the hold and resumes delivery when it completes", async (t) => {
  const h = makeHarness({ busy: true });
  t.after(() => h.close());

  await enqueue(h, "A");
  await enqueue(h, "B");
  h.internals.handleGlobalKey(ESC);
  await flush();

  // Explicitly submitted work starts a fresh run.
  await h.internals.handleSubmit("fresh work");
  busy(h);
  assert.equal(h.internals.queueHold, true, "the hold is not released merely by starting work");
  // Only the controller's proof of that turn's genuine completion releases it.
  h.proveCompletion();
  idle(h);
  await settleFlush();

  assert.deepEqual(h.promptCalls.map((call) => call.text), ["fresh work", "A"]);
  assert.deepEqual(h.internals.queue.map((item) => item.text), ["B"]);
  assert.equal(h.internals.queueHold, false, "the genuine completion released the hold");
});

test("a late idle from the cancelled run cannot drain the newly rearmed queue", async (t) => {
  const h = makeHarness({ busy: true, phaseOnPrompt: false });
  t.after(() => h.close());

  await enqueue(h, "A");
  await enqueue(h, "B");
  h.internals.handleGlobalKey(ESC);
  await flush();
  idle(h);
  await settleFlush();
  assert.equal(h.promptCalls.length, 0);

  // Explicit new work is submitted but a stale idle from the old run lands
  // before the new run is ever observed busy.
  await h.internals.handleSubmit("fresh work");
  idle(h);
  await settleFlush();
  assert.equal(h.promptCalls.length, 1, "only the explicit work was sent");
  assert.deepEqual(h.internals.queue.map((item) => item.text), ["A", "B"], "the held queue stayed private");

  // The new run genuinely runs to completion; only then may the queue resume.
  busy(h);
  h.proveCompletion();
  idle(h);
  await settleFlush();
  assert.deepEqual(h.promptCalls.map((call) => call.text), ["fresh work", "A"]);
  assert.deepEqual(h.internals.queue.map((item) => item.text), ["B"]);
});

// --- persistence and attachments ------------------------------------------

test("a persisted cancellation hold survives restoration and blocks stale idle flushes", async (t) => {
  writeSessionState("session-1", {
    queue: [
      { text: "restored A", attachments: [] },
      { text: "restored B", attachments: [] },
    ],
    queueHold: true,
  });

  const h = makeHarness();
  t.after(() => h.close());
  await h.internals.restoreSessionShellState("session-1");
  assert.equal(h.internals.queueHold, true, "the hold is restored");
  assert.deepEqual(h.internals.queue.map((item) => item.text), ["restored A", "restored B"]);

  for (let i = 0; i < 3; i++) idle(h);
  await settleFlush();
  assert.equal(h.promptCalls.length, 0, "an idle refresh cannot send held messages");
  assert.equal(readSessionState("session-1")?.queueHold, true);

  // Explicit cmd+enter still works from a restored hold.
  h.internals.handleGlobalKey(SUPER_ENTER);
  await flush();
  assert.deepEqual(h.promptCalls.map((call) => call.text), ["restored A"]);
  assert.deepEqual(h.internals.queue.map((item) => item.text), ["restored B"]);
});

test("a cancelled queue keeps its frozen file snapshot and never re-reads the changed disk", async (t) => {
  const h = makeHarness({ busy: true });
  t.after(() => h.close());

  const path = makeFile("ORIGINAL-BODY");
  await enqueueFile(h, path);
  assert.equal(h.internals.queue.length, 1);
  assert.equal(h.internals.queue[0]!.frozenFiles?.[0]?.content, "ORIGINAL-BODY");

  writeFileSync(path, "CHANGED-BODY");
  h.internals.handleGlobalKey(ESC);
  await settleFlush();
  assert.equal(h.promptCalls.length, 0, "Esc sends nothing");
  assert.equal(h.internals.queue[0]!.frozenFiles?.[0]?.content, "ORIGINAL-BODY", "the snapshot is frozen");

  // Explicit dequeue delivers the frozen bytes, not the changed disk file.
  h.internals.handleGlobalKey(SUPER_ENTER);
  await flush();
  assert.equal(h.promptCalls.length, 1);
  assert.ok(h.promptCalls[0]!.text.includes("ORIGINAL-BODY"));
  assert.ok(!h.promptCalls[0]!.text.includes("CHANGED-BODY"));
});

test("an unavailable queued snapshot stays blocked after cancellation", async (t) => {
  const chip = { marker: "[File: legacy.txt]", path: "/legacy/legacy.txt", id: "legacy", name: "legacy.txt" };
  writeSessionState("session-1", {
    queue: [{ text: "[File: legacy.txt]", attachments: [], files: [chip] }],
    queueHold: true,
  });

  const h = makeHarness();
  t.after(() => h.close());
  await h.internals.restoreSessionShellState("session-1");
  assert.equal(h.internals.queue[0]!.needsReattach?.length, 1);

  idle(h);
  await settleFlush();
  assert.equal(h.promptCalls.length, 0, "an automatic flush sends nothing");

  // The explicit dequeue also refuses instead of re-reading the disk.
  h.internals.handleGlobalKey(SUPER_ENTER);
  await flush();
  assert.equal(h.promptCalls.length, 0, "the explicit dequeue sends nothing");
  assert.equal(h.internals.queue.length, 1, "the blocked item stays queued");
  assert.match(h.failures.at(-1)!, /legacy\.txt.*unavailable/i);
});

test("a cancellation while an explicitly dequeued shell runs is not undone by its finally", async (t) => {
  const h = makeHarness({ busy: true });
  t.after(() => h.close());

  await enqueue(h, "! echo hi");
  await enqueue(h, "normal follow-up");
  h.internals.handleGlobalKey(ESC);
  await flush();
  assert.equal(h.internals.queueHold, true);

  // Dequeue the shell explicitly; it runs as a discrete job. Keep it pending.
  let finish!: () => void;
  const running = new Promise<void>((resolve) => { finish = resolve; });
  let started = 0;
  h.internals.runShellCommand = async () => {
    started += 1;
    await running;
  };
  h.internals.handleGlobalKey(SUPER_ENTER);
  await flush();
  assert.equal(started, 1, "the shell command was explicitly run");
  assert.deepEqual(h.internals.queue.map((item) => item.text), ["normal follow-up"]);

  // The user cancels that shell job; a new hold is taken.
  h.internals.holdQueueDelivery();
  finish();
  await flush();

  assert.equal(h.internals.queueHold, true, "the cancelled job's finally did not release the hold");
  assert.equal(h.promptCalls.length, 0, "the follow-up was not drained");
  assert.deepEqual(h.internals.queue.map((item) => item.text), ["normal follow-up"]);
});

test("queued commands are preserved and never run on cancellation", async (t) => {
  const h = makeHarness({ busy: true });
  t.after(() => h.close());

  await enqueue(h, "A");
  await enqueue(h, "/compact");
  await enqueue(h, "B");
  assert.deepEqual(h.internals.queue.map((item) => item.text), ["A", "/compact", "B"]);

  h.internals.handleGlobalKey(ESC);
  await settleFlush();
  assert.equal(h.runCommands.length, 0, "the queued command did not run");
  assert.equal(h.promptCalls.length, 0);
  assert.deepEqual(h.internals.queue.map((item) => item.text), ["A", "/compact", "B"], "FIFO including the command is intact");
});
