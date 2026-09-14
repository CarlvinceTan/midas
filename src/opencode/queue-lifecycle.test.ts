import assert from "node:assert/strict";
import { mkdirSync, mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { after, test } from "node:test";
import type { Editor, Terminal } from "@earendil-works/pi-tui";
import type { PromptAttachment } from "../lib/attachments.ts";

/**
 * Midas queue lifecycle tests. These drive the REAL SessionController (fed by a
 * deterministic fake SDK event stream) together with the real MidasApp queue
 * hold/admission logic, so the release of a cancelled queue is proven by genuine
 * backend completion evidence -- not by a bare busy/idle guess.
 *
 * The covered races:
 *   - an old cancelled run's delayed idle landing after the fresh explicit run
 *     was observed busy must not send anything;
 *   - completion requires BOTH a terminal assistant message for the explicit
 *     turn's own user message AND an idle, in either arrival order;
 *   - older/aborted/tool-step completions and wrong session/parent messages are
 *     ignored;
 *   - a late abort acknowledgement cannot reset a newer turn to idle;
 *   - a reconnect clears completion ownership and keeps the queue held.
 *
 * Nothing here calls a real model, server, board, goal tool, UI or clipboard.
 */

// Isolate every path the module graph could consult before it is imported.
const sandbox = mkdtempSync(join(tmpdir(), "midas-queue-lifecycle-"));
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

const { MidasApp } = await import("../ui/app.ts");
const { SessionController } = await import("./session.ts");
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

// --- fake SDK -------------------------------------------------------------

const SESSION = "session-1";

interface SdkMessage {
  info: Record<string, unknown>;
  parts: unknown[];
}

interface FakeSdk {
  client: Record<string, unknown>;
  promptBodies: Array<Record<string, unknown>>;
  commandBodies: Array<Record<string, unknown>>;
  history: SdkMessage[];
  send(event: unknown): void;
  setAbort(fn: () => Promise<void>): void;
}

function makeSdk(): FakeSdk {
  const queue: unknown[] = [];
  const waiters: Array<() => void> = [];
  const stream = (async function* () {
    for (;;) {
      if (queue.length === 0) await new Promise<void>((resolve) => waiters.push(resolve));
      const next = queue.shift();
      if (next === undefined) return;
      yield next;
    }
  })();
  let abortFn: () => Promise<void> = async () => {};
  const promptBodies: Array<Record<string, unknown>> = [];
  const commandBodies: Array<Record<string, unknown>> = [];
  const history: SdkMessage[] = [];
  const record = {
    id: SESSION,
    projectID: "p",
    directory: workDir,
    title: "test session",
    version: "1",
    time: { created: 0, updated: 0 },
  };
  const client = {
    session: {
      create: async () => record,
      get: async () => record,
      messages: async () => history.slice(),
      promptAsync: async (options: { body?: Record<string, unknown> }) => {
        promptBodies.push(options.body ?? {});
      },
      command: async (options: { body?: Record<string, unknown> }) => {
        commandBodies.push(options.body ?? {});
      },
      abort: () => abortFn(),
    },
    event: { subscribe: async () => ({ stream }) },
  };
  return {
    client,
    promptBodies,
    commandBodies,
    history,
    send(event: unknown) {
      queue.push(event);
      waiters.shift()?.();
    },
    setAbort(fn: () => Promise<void>) {
      abortFn = fn;
    },
  };
}

function userMessage(id: string, sessionID = SESSION): Record<string, unknown> {
  return { id, sessionID, role: "user", agent: "main", time: { created: Date.now() } };
}

function assistantMessage(
  id: string,
  parentID: string,
  options: { finish?: string; error?: boolean; sessionID?: string } = {},
): Record<string, unknown> {
  return {
    id,
    sessionID: options.sessionID ?? SESSION,
    role: "assistant",
    parentID,
    mode: "main",
    providerID: "test",
    modelID: "test-model",
    cost: 0,
    tokens: { input: 0, output: 0, reasoning: 0, cache: { read: 0, write: 0 } },
    time: { created: Date.now(), completed: Date.now() },
    ...(options.finish ? { finish: options.finish } : {}),
    ...(options.error ? { error: { name: "MessageAbortedError", data: { message: "aborted" } } } : {}),
  };
}

function messageUpdated(info: Record<string, unknown>): unknown {
  return { type: "message.updated", properties: { info } };
}

function sessionIdle(): unknown {
  return { type: "session.idle", properties: { sessionID: SESSION } };
}

// --- app harness ----------------------------------------------------------

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

interface QueueShape {
  text: string;
  attachments: PromptAttachment[];
}

interface AppInternals {
  editor: Editor;
  queue: QueueShape[];
  queueHold: boolean;
  queueRearmActive: boolean;
  queueFlushTimer: ReturnType<typeof setTimeout> | undefined;
  syncQueueDelivery(): void;
  handleGlobalKey(data: string): { consume?: boolean } | undefined;
  handleSubmit(text: string): Promise<void>;
  maybeFlushQueue(): void;
  maybeGenerateTitle(...args: unknown[]): void;
  fail(message: string): void;
  warn(message: string): void;
}

interface Harness {
  app: InstanceType<typeof MidasApp>;
  controller: InstanceType<typeof SessionController>;
  sdk: FakeSdk;
  internals: AppInternals;
  failures: string[];
  warnings: string[];
  close(): void;
}

function makeHarness(sdk: FakeSdk = makeSdk()): Harness {
  const controller = new SessionController({ client: sdk.client as never, cwd: workDir });
  const app = new MidasApp({
    controller,
    cwd: workDir,
    settings: { tuiMode: "regular", voicePreload: false },
    agent: "main",
    model: { providerID: "test", modelID: "test-model", name: "Test Model", providerName: "Test" },
    terminal: new TestTerminal(),
  });
  const raw = app as unknown as AppInternals;
  raw.maybeGenerateTitle = () => {};
  const failures: string[] = [];
  const warnings: string[] = [];
  raw.fail = (message: string) => { failures.push(message); };
  raw.warn = (message: string) => { warnings.push(message); };
  return {
    app,
    controller,
    sdk,
    internals: raw,
    failures,
    warnings,
    close(): void {
      try { app.quit(); } catch { /* teardown is best-effort */ }
    },
  };
}

const ESC = "\x1b";

async function flush(): Promise<void> {
  for (let i = 0; i < 5; i++) await new Promise<void>((resolve) => setImmediate(resolve));
}

async function settle(): Promise<void> {
  for (let i = 0; i < 5; i++) await new Promise<void>((resolve) => setTimeout(resolve, 0));
}

/** Wait past the 400ms debounce so a scheduled flush would have fired. */
async function settleFlush(): Promise<void> {
  await new Promise<void>((resolve) => setTimeout(resolve, 450));
  await flush();
}

function promptText(body: Record<string, unknown>): string {
  const parts = (body as { parts?: Array<{ type?: string; text?: string }> }).parts ?? [];
  return parts.find((part) => part.type === "text")?.text ?? "";
}

function promptTexts(sdk: FakeSdk): string[] {
  return sdk.promptBodies.map(promptText);
}

/**
 * Hold a busy session with one queued follow-up, then submit explicit fresh work
 * and leave the session busy on that fresh run.
 */
async function holdAndRearm(h: Harness): Promise<void> {
  h.controller.transcript.setPhase("busy");
  await h.internals.handleSubmit("A");
  assert.equal(h.internals.queue.length, 1);
  h.internals.handleGlobalKey(ESC);
  await flush();
  assert.equal(h.internals.queueHold, true, "Esc holds the queue");
  await h.internals.handleSubmit("fresh work");
  assert.equal(h.internals.queueRearmActive, true, "explicit work re-arms delivery");
  assert.equal(h.controller.transcript.phase, "busy", "the fresh run is busy");
  assert.deepEqual(promptTexts(h.sdk), ["fresh work"]);
}

function syncIdle(h: Harness): void {
  h.internals.syncQueueDelivery();
}

// --- the previously acknowledged race -------------------------------------

test("an old cancelled idle after fresh busy cannot release, and a matching terminal then releases one", async (t) => {
  const h = makeHarness();
  t.after(() => h.close());
  await h.controller.resume(SESSION);

  h.controller.transcript.setPhase("busy");
  await h.internals.handleSubmit("A");
  await h.internals.handleSubmit("B");
  assert.deepEqual(h.internals.queue.map((item) => item.text), ["A", "B"]);

  // Esc aborts the running turn and holds the queue; the abort resolves idle.
  h.internals.handleGlobalKey(ESC);
  await flush();
  assert.equal(h.internals.queueHold, true);
  assert.equal(h.controller.transcript.phase, "idle");

  // Explicit fresh work is submitted and observed busy.
  await h.internals.handleSubmit("fresh work");
  assert.equal(h.controller.transcript.phase, "busy");
  assert.deepEqual(promptTexts(h.sdk), ["fresh work"]);

  // The OLD cancelled run's delayed idle lands *after* the fresh busy was seen.
  h.sdk.send(sessionIdle());
  await settle();
  syncIdle(h);
  await settleFlush();
  assert.deepEqual(promptTexts(h.sdk), ["fresh work"], "the stale idle sent nothing");
  assert.deepEqual(h.internals.queue.map((item) => item.text), ["A", "B"], "the queue stayed private");
  assert.equal(h.internals.queueHold, true, "the hold survived the stale idle");

  // The fresh run's own user message binds its identity, and a tool step is not
  // terminal; only the matching terminal assistant message proves completion.
  h.sdk.send(messageUpdated(userMessage("u-fresh")));
  h.sdk.send(messageUpdated(assistantMessage("a-step", "u-fresh", { finish: "tool-calls" })));
  await settle();
  syncIdle(h);
  await settleFlush();
  assert.deepEqual(promptTexts(h.sdk), ["fresh work"], "a tool step alone proved nothing");

  h.sdk.send(messageUpdated(assistantMessage("a-final", "u-fresh", { finish: "stop" })));
  await settle();
  syncIdle(h);
  await settleFlush();
  assert.deepEqual(promptTexts(h.sdk), ["fresh work", "A"], "exactly one follow-up was released");
  assert.deepEqual(h.internals.queue.map((item) => item.text), ["B"], "the rest stayed queued");
  assert.equal(h.internals.queueHold, false, "the proven completion released the hold");

  // Duplicate completion/idle events must not drain the next item.
  h.sdk.send(messageUpdated(assistantMessage("a-final", "u-fresh", { finish: "stop" })));
  h.sdk.send(sessionIdle());
  await settle();
  syncIdle(h);
  await settleFlush();
  assert.deepEqual(promptTexts(h.sdk), ["fresh work", "A"], "duplicates drained nothing more");
  assert.deepEqual(h.internals.queue.map((item) => item.text), ["B"]);
});

// --- terminal/idle ordering ------------------------------------------------

test("completion releases only after both the matching terminal message and idle, in either order", async (t) => {
  // Terminal first, idle second.
  const a = makeHarness();
  t.after(() => a.close());
  await a.controller.resume(SESSION);
  await holdAndRearm(a);
  a.sdk.send(messageUpdated(userMessage("u-fresh")));
  a.sdk.send(messageUpdated(assistantMessage("a-final", "u-fresh", { finish: "stop" })));
  await settle();
  syncIdle(a);
  await settleFlush();
  assert.equal(a.internals.queueHold, true, "terminal alone (still busy) did not release");

  a.sdk.send(sessionIdle());
  await settle();
  syncIdle(a);
  await settleFlush();
  assert.deepEqual(promptTexts(a.sdk), ["fresh work", "A"], "idle after terminal released one");

  // Idle first, terminal second.
  const b = makeHarness();
  t.after(() => b.close());
  await b.controller.resume(SESSION);
  await holdAndRearm(b);
  b.sdk.send(sessionIdle());
  await settle();
  syncIdle(b);
  await settleFlush();
  assert.equal(b.internals.queueHold, true, "idle alone did not release");

  b.sdk.send(messageUpdated(userMessage("u-fresh")));
  b.sdk.send(messageUpdated(assistantMessage("a-final", "u-fresh", { finish: "stop" })));
  await settle();
  syncIdle(b);
  await settleFlush();
  assert.deepEqual(promptTexts(b.sdk), ["fresh work", "A"], "terminal after idle released one");
});

// --- bogus evidence --------------------------------------------------------

test("older, wrong-parent and wrong-session completions never release", async (t) => {
  const h = makeHarness();
  t.after(() => h.close());
  await h.controller.resume(SESSION);
  await holdAndRearm(h);

  // The explicit turn's user message binds its identity.
  h.sdk.send(messageUpdated(userMessage("u-fresh")));
  await settle();

  // An older turn's terminal (wrong parent) and a wrong-session terminal.
  h.sdk.send(messageUpdated(assistantMessage("a-old", "u-old", { finish: "stop" })));
  h.sdk.send(messageUpdated(assistantMessage("a-other", "u-fresh", { finish: "stop", sessionID: "other-session" })));
  await settle();
  h.sdk.send(sessionIdle());
  await settle();
  syncIdle(h);
  await settleFlush();
  assert.equal(h.internals.queueHold, true, "old/wrong-session completions were ignored");
  assert.deepEqual(promptTexts(h.sdk), ["fresh work"]);

  // A genuine terminal message for the right parent still releases, proving the
  // bogus evidence was rejected rather than merely arriving too early.
  h.sdk.send(messageUpdated(assistantMessage("a-final", "u-fresh", { finish: "stop" })));
  await settle();
  syncIdle(h);
  await settleFlush();
  assert.deepEqual(promptTexts(h.sdk), ["fresh work", "A"]);
  assert.equal(h.internals.queueHold, false);
});

test("a tool-step is not terminal, but an abort permanently fails the turn closed", async (t) => {
  // Only a tool step so far: completion is not proven, but a later terminal step
  // for the same turn still releases.
  const stepped = makeHarness();
  t.after(() => stepped.close());
  await stepped.controller.resume(SESSION);
  await holdAndRearm(stepped);
  stepped.sdk.send(messageUpdated(userMessage("u-fresh")));
  stepped.sdk.send(messageUpdated(assistantMessage("a-step", "u-fresh", { finish: "tool-calls" })));
  stepped.sdk.send(sessionIdle());
  await settle();
  syncIdle(stepped);
  await settleFlush();
  assert.equal(stepped.internals.queueHold, true, "a tool step alone is not completion");
  assert.deepEqual(promptTexts(stepped.sdk), ["fresh work"]);

  stepped.sdk.send(messageUpdated(assistantMessage("a-final", "u-fresh", { finish: "stop" })));
  await settle();
  syncIdle(stepped);
  await settleFlush();
  assert.deepEqual(promptTexts(stepped.sdk), ["fresh work", "A"], "the terminal step released");
  assert.equal(stepped.internals.queueHold, false);

  // An aborted turn can never prove completion again, even if a later terminal
  // message for the same parent arrives.
  const aborted = makeHarness();
  t.after(() => aborted.close());
  await aborted.controller.resume(SESSION);
  await holdAndRearm(aborted);
  aborted.sdk.send(messageUpdated(userMessage("u-fresh")));
  aborted.sdk.send(messageUpdated(assistantMessage("a-abort", "u-fresh", { error: true })));
  aborted.sdk.send(sessionIdle());
  await settle();
  syncIdle(aborted);
  aborted.sdk.send(messageUpdated(assistantMessage("a-final", "u-fresh", { finish: "stop" })));
  await settle();
  syncIdle(aborted);
  await settleFlush();
  assert.equal(aborted.internals.queueHold, true, "an aborted turn fails closed");
  assert.deepEqual(promptTexts(aborted.sdk), ["fresh work"]);
});

// --- abort acknowledgement guard ------------------------------------------

test("a late abort acknowledgement cannot reset a newer prompt to idle or release the queue", async (t) => {
  const h = makeHarness();
  t.after(() => h.close());
  await h.controller.resume(SESSION);

  let releaseAbort!: () => void;
  h.sdk.setAbort(
    () =>
      new Promise<void>((resolve) => {
        releaseAbort = resolve;
      }),
  );

  h.controller.transcript.setPhase("busy");
  await h.internals.handleSubmit("A");
  h.internals.handleGlobalKey(ESC);
  await flush();
  assert.equal(h.internals.queueHold, true);

  // The abort ack is still pending; the old idle event makes the session idle so
  // the fresh submission sends instead of queueing.
  h.sdk.send(sessionIdle());
  await settle();
  await h.internals.handleSubmit("fresh work");
  assert.equal(h.controller.transcript.phase, "busy");

  // The abort's HTTP acknowledgement finally lands after the newer prompt.
  releaseAbort();
  await flush();
  assert.equal(h.controller.transcript.phase, "busy", "the late ack did not reset the newer turn to idle");

  // A stale idle still cannot release without matching terminal evidence.
  h.sdk.send(sessionIdle());
  await settle();
  syncIdle(h);
  await settleFlush();
  assert.deepEqual(promptTexts(h.sdk), ["fresh work"], "no queued item leaked");
  assert.equal(h.internals.queueHold, true);
});

// --- reconnect -------------------------------------------------------------

test("a reconnect clears completion ownership and keeps the queue held", async (t) => {
  const h = makeHarness();
  t.after(() => h.close());
  await h.controller.resume(SESSION);
  await holdAndRearm(h);

  // Bind the explicit turn before reconnecting.
  h.sdk.send(messageUpdated(userMessage("u-fresh")));
  await settle();

  // The reconnect reloads history that already contains a terminal completion
  // for that same parent, but no live run owns it.
  const next = makeSdk();
  next.history.push(
    { info: userMessage("u-fresh"), parts: [] },
    { info: assistantMessage("a-final", "u-fresh", { finish: "stop" }), parts: [] },
  );
  await h.controller.reconnect({ client: next.client as never });
  next.send(sessionIdle());
  await settle();
  syncIdle(h);
  await settleFlush();

  assert.deepEqual(promptTexts(h.sdk), ["fresh work"], "the original client sent nothing new");
  assert.deepEqual(promptTexts(next), [], "the reconnected client sent nothing");
  assert.equal(h.internals.queueHold, true, "unknown completion ownership fails closed");
  assert.deepEqual(h.internals.queue.map((item) => item.text), ["A"]);
});
