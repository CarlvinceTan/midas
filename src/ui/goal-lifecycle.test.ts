import assert from "node:assert/strict";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { after, test } from "node:test";
import type { Component, Terminal } from "@earendil-works/pi-tui";
import type { Transcript as TranscriptState } from "../state/transcript.ts";

/**
 * Midas UI goal-lifecycle tests. These drive the real `runSlashCommand` and the
 * real goal picker/dialog components against a fake controller, fake terminal
 * and faked non-goal boundaries (metadata, voice, shell, persistence,
 * dispatcher). They never call `app.run`, the midas CLI, an opencode server, a
 * model API, a goal tool or a real board. Nothing here proves the external goal
 * plugin's budget/continuation behaviour; it proves only that Midas routes the
 * command and picker correctly.
 */

// Isolate every path the module graph could consult before it is imported, so
// no test can read or write a real config, cache, session or goal state file.
const sandbox = mkdtempSync(join(tmpdir(), "midas-goal-lifecycle-"));
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

const { MidasApp, goalRoute } = await import("./app.ts");
const { Transcript } = await import("../state/transcript.ts");
const { initTheme } = await import("../theme/theme.ts");
const { initTheme: initPiTheme } = await import("@earendil-works/pi-coding-agent");

initPiTheme(undefined, false);
initTheme(undefined);

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

interface Dispatch {
  command: string;
  args: string;
}

type RouteHandler = (command: string, args: string) => Promise<void>;

/** The fake controller surface the app is allowed to touch, plus test hooks. */
interface ControllerDouble {
  id: string | undefined;
  title: string | undefined;
  transcript: TranscriptState;
  runCommand(command: string, args: string): Promise<void>;
  dispose(): void;
  /** Every command the app asked the controller to run. */
  calls: Dispatch[];
  /** Swap the backend target, modelling a controller reconnect in place. */
  setRoute(next: RouteHandler): void;
}

/**
 * A controller double whose only legal surface is the handful of members the
 * app needs. Any other property access (e.g. `prompt`, `setAgent`, `reconnect`)
 * throws, so an accidental non-goal dispatch fails the test instead of
 * silently succeeding.
 */
function fakeController(options: { session?: boolean } = {}): ControllerDouble {
  const transcript = new Transcript();
  const calls: Dispatch[] = [];
  let route: RouteHandler =
    options.session === false
      ? async () => { throw new Error("No active session"); }
      : async () => {};
  const raw = {
    id: options.session === false ? undefined : "session-1",
    title: undefined as string | undefined,
    transcript,
    async runCommand(command: string, args: string): Promise<void> {
      calls.push({ command, args });
      // Mirror SessionController's phase contract: only an idle session turns
      // busy, and a rejected command returns it to idle.
      const wasIdle = transcript.phase === "idle";
      if (wasIdle) transcript.setPhase("busy");
      try {
        await route(command, args);
      } catch (error) {
        if (wasIdle) transcript.setPhase("idle");
        throw error;
      }
    },
    dispose(): void {},
    calls,
    setRoute(next: RouteHandler): void {
      route = next;
    },
  };
  return new Proxy(raw, {
    get(object, property, receiver) {
      if (typeof property === "symbol") return Reflect.get(object, property, receiver);
      if (!(property in object)) throw new Error(`unexpected controller access: ${String(property)}`);
      return Reflect.get(object, property, receiver) as never;
    },
  });
}

interface ApplyInternals {
  activeAgent: string;
  activeOverlay: Component | undefined;
  runSlashCommand(input: string): Promise<void>;
  openGoal(): void;
  syncDispatcher(): void;
  ensureDispatchDaemon(): void;
  tailDispatchLog(): void;
  refreshTaskMeta(...args: unknown[]): Promise<void>;
  ensureVoiceController(...args: unknown[]): unknown;
  runShellCommand(...args: unknown[]): Promise<void>;
  scheduleDraftSave(): void;
  fail(message: string): void;
  warn(message: string): void;
  success(message: string): void;
}

interface Boundaries {
  dispatcher: number;
  metadata: number;
  voice: number;
  shell: number;
  draft: number;
}

interface Harness {
  controller: ControllerDouble;
  internals: ApplyInternals;
  failures: string[];
  warnings: string[];
  boundaries: Boundaries;
  close(): void;
}

/** Build a MidasApp without running it, with every non-goal boundary stubbed. */
function makeHarness(options: { agent?: string; session?: boolean; busy?: boolean } = {}): Harness {
  const controller = fakeController({ session: options.session });
  if (options.busy) controller.transcript.setPhase("busy");
  const app = new MidasApp({
    controller: controller as never,
    cwd: process.cwd(),
    settings: { tuiMode: "regular", voicePreload: false },
    agent: options.agent ?? "main",
    model: { providerID: "test", modelID: "test-model", name: "Test Model", providerName: "Test" },
    terminal: new TestTerminal(),
  });
  const internals = app as unknown as ApplyInternals;
  const boundaries: Boundaries = { dispatcher: 0, metadata: 0, voice: 0, shell: 0, draft: 0 };
  const failures: string[] = [];
  const warnings: string[] = [];
  // Stub the boundaries a goal action must never reach. Any touch is recorded
  // so a test can assert zero, rather than relying on a real daemon/helper.
  internals.syncDispatcher = () => { boundaries.dispatcher += 1; };
  internals.ensureDispatchDaemon = () => { boundaries.dispatcher += 1; };
  internals.tailDispatchLog = () => { boundaries.dispatcher += 1; };
  internals.refreshTaskMeta = async (..._args: unknown[]) => { boundaries.metadata += 1; };
  internals.ensureVoiceController = () => { boundaries.voice += 1; return undefined; };
  internals.runShellCommand = async () => { boundaries.shell += 1; };
  internals.scheduleDraftSave = () => { boundaries.draft += 1; };
  internals.fail = (message: string) => { failures.push(message); };
  internals.warn = (message: string) => { warnings.push(message); };
  internals.success = () => {};
  return {
    controller,
    internals,
    failures,
    warnings,
    boundaries,
    close(): void {
      try { app.quit(); } catch { /* teardown is best-effort in tests */ }
    },
  };
}

/** Let a fire-and-forget command rejection reach `runController`'s catch. */
async function flush(): Promise<void> {
  await new Promise<void>((resolve) => setImmediate(resolve));
  await new Promise<void>((resolve) => setImmediate(resolve));
}

/** The currently open editor-dock overlay, if any. */
function overlayOf(harness: Harness): Component {
  const overlay = harness.internals.activeOverlay;
  assert.ok(overlay, "expected an overlay to be open");
  return overlay;
}

/** Move the selection down `times` rows and press Enter. */
function select(component: Component, times: number): void {
  for (let i = 0; i < times; i += 1) component.handleInput?.("\x1b[B");
  component.handleInput?.("\r");
}

function typeText(component: Component, text: string): void {
  for (const char of text) component.handleInput?.(char);
}

/** Row order of the goal picker, kept in sync with `openGoal`. */
const PICKER = { resume: 0, pause: 1, edit: 2, status: 3, new: 4, clear: 5 } as const;

const TIMEOUT = { timeout: 10_000 };

// --- routing function -----------------------------------------------------

test("goalRoute opens the picker for a bare /goal and forwards arguments exactly", () => {
  assert.equal(goalRoute(""), undefined);
  assert.deepEqual(goalRoute("status"), { command: "goal", args: "status" });
  assert.deepEqual(goalRoute("clear"), { command: "goal", args: "clear" });
  assert.deepEqual(goalRoute("edit ship the parser fix"), { command: "goal", args: "edit ship the parser fix" });
  assert.deepEqual(goalRoute("ship the  parser   fix"), { command: "goal", args: "ship the  parser   fix" });
  assert.deepEqual(goalRoute("pause"), { command: "pause_goal", args: "" });
  assert.deepEqual(goalRoute("resume"), { command: "resume_goal", args: "" });
  // Only the exact keyword uses the dedicated control.
  assert.deepEqual(goalRoute("pause now"), { command: "goal", args: "pause now" });
  assert.deepEqual(goalRoute("resume the old goal"), { command: "goal", args: "resume the old goal" });
});

test("typed /goal commands reach the controller without opening the picker", TIMEOUT, async (t) => {
  const h = makeHarness();
  t.after(() => h.close());

  await h.internals.runSlashCommand("/goal status");
  await h.internals.runSlashCommand("/goal clear");
  await h.internals.runSlashCommand("/goal edit ship the parser fix");
  await h.internals.runSlashCommand("/goal ship  the   parser");
  await h.internals.runSlashCommand("/goal pause");
  await h.internals.runSlashCommand("/goal resume");

  assert.deepEqual(h.controller.calls, [
    { command: "goal", args: "status" },
    { command: "goal", args: "clear" },
    { command: "goal", args: "edit ship the parser fix" },
    { command: "goal", args: "ship  the   parser" },
    { command: "pause_goal", args: "" },
    { command: "resume_goal", args: "" },
  ]);
  assert.equal(h.internals.activeOverlay, undefined, "typed commands never open the picker");
});

test("a bare /goal (including trailing whitespace) opens the picker and dispatches nothing", TIMEOUT, async (t) => {
  const h = makeHarness();
  t.after(() => h.close());

  for (const input of ["/goal", "/goal   "]) {
    await h.internals.runSlashCommand(input);
    assert.ok(h.internals.activeOverlay, `${JSON.stringify(input)} opens the picker`);
    h.internals.activeOverlay?.handleInput?.("\x1b");
    assert.equal(h.internals.activeOverlay, undefined);
  }
  assert.deepEqual(h.controller.calls, []);
});

// --- picker actions -------------------------------------------------------

test("every picker action routes to its matching controller command", TIMEOUT, () => {
  const cases: Array<{ name: string; downs: number; text?: string; expected: Dispatch }> = [
    { name: "resume", downs: PICKER.resume, expected: { command: "resume_goal", args: "" } },
    { name: "pause", downs: PICKER.pause, expected: { command: "pause_goal", args: "" } },
    { name: "edit", downs: PICKER.edit, text: "ship the parser fix", expected: { command: "goal", args: "edit ship the parser fix" } },
    { name: "status", downs: PICKER.status, expected: { command: "goal", args: "status" } },
    { name: "new", downs: PICKER.new, text: "harden the goal bridge", expected: { command: "goal", args: "harden the goal bridge" } },
    { name: "clear", downs: PICKER.clear, expected: { command: "goal", args: "clear" } },
  ];
  for (const entry of cases) {
    const h = makeHarness();
    try {
      h.internals.openGoal();
      const picker = overlayOf(h);
      select(picker, entry.downs);
      if (entry.text !== undefined) {
        const dialog = overlayOf(h);
        typeText(dialog, entry.text);
        dialog.handleInput?.("\r");
      }
      assert.deepEqual(h.controller.calls, [entry.expected], `${entry.name} dispatches exactly once`);
      assert.equal(h.internals.activeOverlay, undefined, `${entry.name} closes its overlay`);
    } finally {
      h.close();
    }
  }
});

test("cancelling the picker or an objective dialog dispatches nothing", TIMEOUT, (t) => {
  const h = makeHarness();
  t.after(() => h.close());

  // Escape leaves the picker without running a command.
  h.internals.openGoal();
  overlayOf(h).handleInput?.("\x1b");
  assert.equal(h.internals.activeOverlay, undefined);
  assert.deepEqual(h.controller.calls, []);

  // Escape from the objective dialog also cancels.
  h.internals.openGoal();
  const picker = overlayOf(h);
  select(picker, PICKER.new);
  overlayOf(h).handleInput?.("\x1b");
  assert.equal(h.internals.activeOverlay, undefined);
  assert.deepEqual(h.controller.calls, []);
});

test("an empty objective cancels instead of dispatching a blank goal", TIMEOUT, (t) => {
  const h = makeHarness();
  t.after(() => h.close());

  for (const action of [PICKER.new, PICKER.edit]) {
    h.internals.openGoal();
    select(overlayOf(h), action);
    const dialog = overlayOf(h);
    typeText(dialog, "   ");
    dialog.handleInput?.("\r");
    assert.equal(h.internals.activeOverlay, undefined, "the dialog closes on an empty objective");
  }
  assert.deepEqual(h.controller.calls, [], "no blank or dangling edit is dispatched");
});

// --- agent mode and dispatcher safety -------------------------------------

test("goal actions never change the selected agent or touch non-goal boundaries", TIMEOUT, async (t) => {
  for (const agent of ["main", "orchestrator", "plan"]) {
    const h = makeHarness({ agent });
    try {
      assert.equal(h.internals.activeAgent, agent, `${agent}: constructed as selected`);

      await h.internals.runSlashCommand("/goal status");
      await h.internals.runSlashCommand("/goal clear");
      await h.internals.runSlashCommand("/goal edit tighten the checks");
      await h.internals.runSlashCommand("/goal tighten the checks");
      await h.internals.runSlashCommand("/goal pause");
      await h.internals.runSlashCommand("/goal resume");
      await h.internals.runSlashCommand("/goal");
      overlayOf(h).handleInput?.("\x1b");

      assert.equal(h.internals.activeAgent, agent, `${agent}: the selection is unchanged`);
      assert.deepEqual(
        h.boundaries,
        { dispatcher: 0, metadata: 0, voice: 0, shell: 0, draft: 0 },
        `${agent}: no dispatcher, metadata, voice, shell or draft boundary was touched`,
      );
    } finally {
      h.close();
    }
  }
});

// --- backend failures and busy sessions -----------------------------------

test("a missing session surfaces the failure and leaves the session idle", TIMEOUT, async (t) => {
  const h = makeHarness({ session: false });
  t.after(() => h.close());

  await h.internals.runSlashCommand("/goal status");
  await flush();

  assert.deepEqual(h.failures, ["No active session"]);
  assert.equal(h.controller.transcript.phase, "idle");
  assert.equal(h.internals.activeAgent, "main");
  assert.equal(h.boundaries.dispatcher, 0);
});

test("a backend error surfaces without stranding the session busy", TIMEOUT, async (t) => {
  const h = makeHarness();
  t.after(() => h.close());
  h.controller.setRoute(async () => { throw new Error("dispatch failed"); });

  await h.internals.runSlashCommand("/goal status");
  await flush();

  assert.deepEqual(h.failures, ["dispatch failed"]);
  assert.equal(h.controller.transcript.phase, "idle");
  assert.equal(h.boundaries.dispatcher, 0);
});

test("a goal command on an already-busy session keeps it busy and still dispatches", TIMEOUT, async (t) => {
  const h = makeHarness({ busy: true });
  t.after(() => h.close());

  await h.internals.runSlashCommand("/goal status");
  assert.deepEqual(h.controller.calls, [{ command: "goal", args: "status" }]);
  assert.equal(h.controller.transcript.phase, "busy", "an in-flight turn stays busy");

  h.controller.setRoute(async () => { throw new Error("dispatch failed") });
  await h.internals.runSlashCommand("/goal resume");
  await flush();
  assert.equal(h.controller.transcript.phase, "busy", "a rejected goal must not clear a live turn");
  assert.deepEqual(h.failures, ["dispatch failed"]);
  assert.equal(h.boundaries.dispatcher, 0);
});

// --- reconnect delegation -------------------------------------------------

test("goal routing follows the controller across a reconnect without restarting anything", TIMEOUT, async (t) => {
  const h = makeHarness();
  t.after(() => h.close());
  const first: Dispatch[] = [];
  const second: Dispatch[] = [];
  h.controller.setRoute(async (command, args) => { first.push({ command, args }); });

  await h.internals.runSlashCommand("/goal status");
  await h.internals.runSlashCommand("/goal pause");

  // A controller reconnect swaps the backend in place. The app must route the
  // next command through the same controller method, which now reaches the new
  // target; it must never restart or re-point anything itself.
  h.controller.setRoute(async (command, args) => { second.push({ command, args }); });
  await h.internals.runSlashCommand("/goal resume");

  assert.deepEqual(first, [
    { command: "goal", args: "status" },
    { command: "pause_goal", args: "" },
  ]);
  assert.deepEqual(second, [{ command: "resume_goal", args: "" }], "the reconnected target receives the command");
  assert.equal(h.boundaries.dispatcher, 0);
});
