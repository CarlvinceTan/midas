import assert from "node:assert/strict";
import { test } from "node:test";
import { visibleWidth, type Terminal } from "@earendil-works/pi-tui";
import { initTheme as initPiTheme } from "@earendil-works/pi-coding-agent";
import { stripAnsi } from "../lib/ansi.ts";
import { Transcript } from "../state/transcript.ts";
import { initTheme } from "../theme/theme.ts";
import { MidasApp, submitAction, ensureDispatcherOnSubmit, WorkingIndicator, voiceToggle, voiceFrameTitle, exitVoiceOnEscape, isCtrlCPress, pickAgentModelRef, pickAgentThinking, resolveSlashName } from "./app.ts";

initPiTheme(undefined, false);
initTheme(undefined);

/** Strip CSI colours and the zero-width OSC content markers. */
function plain(text: string): string {
  return stripAnsi(text).replace(/\x1b\][^\x07]*\x07/g, "");
}

function indicator(label: string, tone: "thinking" | "running", pad: number) {
  return new WorkingIndicator(
    () => ({ id: "a", label, tone }),
    () => "⠋",
    () => pad,
    () => undefined,
    () => {},
  );
}

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

test("a long running label is truncated inside the right padding", () => {
  const width = 40;
  const pad = 2;
  const lines = indicator(`Running \`${"x".repeat(200)}\``, "running", pad).render(width);
  assert.equal(lines.length, 1);
  const line = lines[0]!;
  assert.ok(visibleWidth(line) <= width - pad, `width ${visibleWidth(line)} exceeds ${width - pad}`);
  assert.ok(plain(line).startsWith("  "), "keeps the left indent");
  assert.ok(plain(line).endsWith("…"), "ends with an ellipsis");
});

test("a short status line is left untouched", () => {
  const lines = indicator("Thinking", "thinking", 1).render(80);
  assert.equal(plain(lines[0]!), " ⠋ Thinking");
});

test("a running OpenCode task uses the two-line subagent status", () => {
  const tool = {
    kind: "tool" as const,
    id: "task-1",
    callID: "task-1",
    tool: "task",
    status: "running" as const,
    input: { subagent_type: "explore", description: "Inspect Lightning feature gaps" },
    start: 0,
  };
  const status = new WorkingIndicator(
    () => ({ id: tool.id, label: "Running Subagents (1 task)", tone: "running", tool }),
    () => "⠋",
    () => 2,
    () => undefined,
    () => {},
  );
  assert.deepEqual(status.render(80).map(plain), [
    "  ⠋ Running Subagents (1 task):",
    "    ⠋ Explore: Inspect Lightning feature gaps",
  ]);
});

test("the model catalog is resolved before the terminal paints its first frame", async () => {
  const terminal = new TestTerminal();
  const agents = deferred<Array<{ name: string; mode: string }>>();
  const models = deferred<Array<{ providerID: string; modelID: string; name: string; providerName: string }>>();
  const transcript = new Transcript();
  const controller = {
    id: undefined,
    title: undefined,
    transcript,
    listAgents: () => agents.promise,
    listModels: () => models.promise,
    listCommands: async () => [],
    mcpServerNames: async () => [],
    defaultModel: async () => undefined,
    setAgent(): void {},
    setModel(): void {},
    setVariant(): void {},
    dispose(): void {},
  } as never;
  const app = new MidasApp({
    controller,
    cwd: process.cwd(),
    settings: { tuiMode: "regular", voicePreload: false, agentLastUsed: { main: "openai/gpt-5.6-sol" } },
    model: { providerID: "openai", modelID: "gpt-5.6-sol", name: "placeholder", providerName: "openai" },
    cachedModels: [],
    terminal,
  });

  const running = app.run();
  try {
    await new Promise<void>((resolve) => setImmediate(resolve));
    assert.equal(terminal.started, false, "terminal must wait for startup catalogs");

    models.resolve([
      { providerID: "openai", modelID: "gpt-5.6-sol", name: "GPT-5.6 Sol", providerName: "OpenAI" },
    ]);
    await new Promise<void>((resolve) => setImmediate(resolve));
    assert.equal(terminal.started, false, "terminal must wait for the agent catalog too");

    agents.resolve([{ name: "main", mode: "primary" }]);
    await new Promise<void>((resolve) => setImmediate(resolve));
    assert.equal(terminal.started, true);
    const footer = (app as unknown as { footer: { render(width: number): string[] } }).footer;
    assert.match(plain(footer.render(120)[0]!), /GPT 5\.6 Sol/);
    assert.doesNotMatch(plain(footer.render(120)[0]!), /Placeholder/);
  } finally {
    agents.resolve([{ name: "main", mode: "primary" }]);
    models.resolve([]);
    app.quit();
    await running;
  }
});

test("a cached model catalog makes startup wait only for agents", async () => {
  const terminal = new TestTerminal();
  const agents = deferred<Array<{ name: string; mode: string }>>();
  const models = deferred<Array<{ providerID: string; modelID: string; name: string; providerName: string }>>();
  const catalog = [
    { providerID: "openai", modelID: "gpt-5.6-sol", name: "GPT-5.6 Sol", providerName: "OpenAI" },
  ];
  const controller = {
    id: undefined,
    title: undefined,
    transcript: new Transcript(),
    listAgents: () => agents.promise,
    listModels: () => models.promise,
    listCommands: async () => [],
    mcpServerNames: async () => [],
    defaultModel: async () => undefined,
    setAgent(): void {},
    setModel(): void {},
    setVariant(): void {},
    dispose(): void {},
  } as never;
  const app = new MidasApp({
    controller,
    cwd: process.cwd(),
    settings: { tuiMode: "regular", voicePreload: false, agentLastUsed: { main: "openai/gpt-5.6-sol" } },
    model: { providerID: "openai", modelID: "gpt-5.6-sol", name: "placeholder", providerName: "openai" },
    cachedModels: catalog,
    terminal,
  });

  const running = app.run();
  try {
    await new Promise<void>((resolve) => setImmediate(resolve));
    assert.equal(terminal.started, false);
    agents.resolve([{ name: "main", mode: "primary" }]);
    await new Promise<void>((resolve) => setImmediate(resolve));
    assert.equal(terminal.started, true, "a slow live model refresh must not block a warm start");
    const footer = (app as unknown as { footer: { render(width: number): string[] } }).footer;
    assert.match(plain(footer.render(120)[0]!), /GPT 5\.6 Sol/);
  } finally {
    agents.resolve([{ name: "main", mode: "primary" }]);
    models.resolve(catalog);
    app.quit();
    await running;
  }
});

test("multitask queues a busy prompt like every other mode", () => {
  assert.equal(submitAction({ multitask: true, runActive: true, isCommand: false, editing: false }), "queue");
});

test("multitask sends a prompt when no run is active", () => {
  assert.equal(submitAction({ multitask: true, runActive: false, isCommand: false, editing: false }), "send");
});

test("busy input queues when multitask is off", () => {
  assert.equal(submitAction({ multitask: false, runActive: true, isCommand: false, editing: false }), "queue");
});

test("an edited follow-up goes back to its slot in every mode", () => {
  assert.equal(submitAction({ multitask: true, runActive: true, isCommand: false, editing: true }), "requeue");
  assert.equal(submitAction({ multitask: false, runActive: true, isCommand: false, editing: true }), "requeue");
});

test("commands keep their existing routing in every mode", () => {
  for (const multitask of [true, false]) {
    for (const runActive of [true, false]) {
      assert.equal(
        submitAction({ multitask, runActive, isCommand: true, editing: false }),
        "command",
        `multitask=${multitask} runActive=${runActive}`,
      );
    }
  }
});

test("a multitask submission ensures the dispatcher is running", () => {
  let calls = 0;
  ensureDispatcherOnSubmit(true, () => { calls += 1; });
  assert.equal(calls, 1);
});

test("a normal submission never touches the dispatcher", () => {
  let calls = 0;
  ensureDispatcherOnSubmit(false, () => { calls += 1; });
  assert.equal(calls, 0);
});

test("repeated multitask submissions keep exactly one dispatcher leader", () => {
  // The real sync is idempotent; this models it so the submission path cannot
  // start a duplicate loop when a leader already holds the lease.
  let leading = false;
  let starts = 0;
  const ensure = (): void => {
    if (leading) return;
    leading = true;
    starts += 1;
  };
  ensureDispatcherOnSubmit(true, ensure);
  ensureDispatcherOnSubmit(true, ensure);
  ensureDispatcherOnSubmit(true, ensure);
  assert.equal(starts, 1);
});

test("voice toggles on and off explicitly and flips with no argument", () => {
  assert.equal(voiceToggle("on", false), true);
  assert.equal(voiceToggle("off", true), false);
  assert.equal(voiceToggle("", false), true);
  assert.equal(voiceToggle("", true), false);
  assert.equal(voiceToggle("bogus", false), undefined);
});

test("the voice title shows Loading then Listening while active", () => {
  assert.equal(voiceFrameTitle({ voice: true, ready: false }), "Voice: Loading");
  assert.equal(voiceFrameTitle({ voice: true, ready: true }), "Voice: Listening");
  assert.equal(voiceFrameTitle({ voice: false, ready: false }), undefined);
  assert.equal(voiceFrameTitle({ voice: false, ready: true }), undefined);
});

test("agent model precedence: session, override, config, last used", () => {
  assert.equal(pickAgentModelRef({ session: "s", override: "o", configured: "c", lastUsed: "l" }), "s");
  assert.equal(pickAgentModelRef({ override: "o", configured: "c", lastUsed: "l" }), "o");
  assert.equal(pickAgentModelRef({ configured: "c", lastUsed: "l" }), "c");
  assert.equal(pickAgentModelRef({ lastUsed: "l" }), "l");
  assert.equal(pickAgentModelRef({}), undefined);
});

test("agent reasoning precedence: own level, model level, fallback", () => {
  assert.equal(pickAgentThinking({ override: "high", model: "low", fallback: "medium" }), "high");
  assert.equal(pickAgentThinking({ model: "low", fallback: "medium" }), "low");
  assert.equal(pickAgentThinking({ fallback: "medium" }), "medium");
});

test("slash names resolve exactly, then by the top fuzzy match", () => {
  const names = ["model", "agents", "multitask", "thinking", "tasks", "exit"];
  assert.equal(resolveSlashName(names, "model"), "model");
  assert.equal(resolveSlashName(names, "mdl"), "model");
  assert.equal(resolveSlashName(names, "mta"), "multitask");
  assert.equal(resolveSlashName(names, "taks"), "tasks");
  // No fuzzy match: keep what was typed so the command reports itself unknown.
  assert.equal(resolveSlashName(names, "zzz"), "zzz");
});

test("escape exits voice only while listening and not in autocomplete", () => {
  assert.equal(exitVoiceOnEscape({ voiceActive: true, escape: true, autocomplete: false }), true);
  assert.equal(exitVoiceOnEscape({ voiceActive: true, escape: true, autocomplete: true }), false);
  assert.equal(exitVoiceOnEscape({ voiceActive: false, escape: true, autocomplete: false }), false);
  assert.equal(exitVoiceOnEscape({ voiceActive: true, escape: false, autocomplete: false }), false);
});

test("ctrl+c only acts on presses, not Kitty repeats or releases", () => {
  assert.equal(isCtrlCPress("\x03"), true);
  assert.equal(isCtrlCPress("\x1b[99;5:1u"), true);
  assert.equal(isCtrlCPress("\x1b[99;5:2u"), false);
  assert.equal(isCtrlCPress("\x1b[99;5:3u"), false);
});
