import assert from "node:assert/strict";
import { after, test } from "node:test";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

/**
 * Frontend command-bridge tests. These exercise `SessionController.runCommand`
 * against a fake opencode client; they never reach the goal engine, the network
 * or a real server. The goal plugin is external, so nothing here proves budget
 * or continuation behaviour.
 */

// Isolate every path the module graph could consult before it is imported, so
// no test can read or write a real config, cache or goal state file.
const sandbox = mkdtempSync(join(tmpdir(), "midas-goal-command-"));
const ISOLATED_ENV = [
  "HOME",
  "MIDAS_CONFIG_DIR",
  "PI_CONFIG_DIR",
  "XDG_CONFIG_HOME",
  "XDG_DATA_HOME",
  "XDG_CACHE_HOME",
  "XDG_STATE_HOME",
  "OPENCODE_GOAL_STATE_PATH",
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

const { SessionController } = await import("./session.ts");

after(() => {
  for (const [key, value] of savedEnv) {
    if (value === undefined) delete process.env[key];
    else process.env[key] = value;
  }
  rmSync(sandbox, { recursive: true, force: true });
});

// --- fixtures -------------------------------------------------------------

interface CommandRequest {
  path: { id: string };
  query: { directory: string };
  body: { command: string; arguments: string; messageID?: string; model?: string; agent?: string };
}

/**
 * The exact id Midas supplies in the command body: opencode's ascending message
 * id shape (`msg_` + 12 hex timestamp chars + 14 base62 random chars).
 */
const MESSAGE_ID_PATTERN = /^msg_[0-9a-f]{12}[0-9A-Za-z]{14}$/;

/** Assert a request carries a supported exact id, then return the body without it. */
function bodyWithoutId(request: CommandRequest): { command: string; arguments: string; model?: string; agent?: string } {
  assert.match(String(request.body.messageID), MESSAGE_ID_PATTERN, "the command supplies a supported exact message id");
  const { messageID: _id, ...rest } = request.body;
  return rest;
}

function sessionRecord(id: string, title = "title") {
  // The fields SessionController reads on resume: id, title and revert.
  return { id, projectID: "p", directory: "/x", title, version: "1", time: { created: 0, updated: 0 } };
}

/**
 * Wrap a fixture so any property the test did not provide throws instead of
 * silently resolving. That keeps an accidental network/model/board dispatch
 * from passing as a no-op.
 */
function strict<T extends object>(target: T, label: string): T {
  return new Proxy(target, {
    get(object, property, receiver) {
      if (typeof property === "symbol") return Reflect.get(object, property, receiver);
      if (!(property in object)) {
        throw new Error(`unexpected ${label} dispatch: ${String(property)}`);
      }
      const value = Reflect.get(object, property, receiver) as unknown;
      if (value !== null && typeof value === "object" && !Array.isArray(value)) {
        return strict(value as object, `${label}.${String(property)}`);
      }
      return value as never;
    },
  });
}

function deferred() {
  let resolve!: () => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<void>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

/**
 * A fake client whose only legal opencode route is `session.command`. Resume
 * needs `session.get`, `session.messages` and `event.subscribe`; everything
 * else throws. `behavior` lets a test hold the command pending or fail it.
 */
function commandFixture(options: { sessionId?: string; behavior?: () => Promise<unknown> } = {}) {
  const id = options.sessionId ?? "s1";
  const requests: CommandRequest[] = [];
  const client = strict(
    {
      session: {
        get: async () => sessionRecord(id),
        messages: async () => [],
        command: async (request: CommandRequest) => {
          requests.push(request);
          if (options.behavior) return options.behavior();
          return {};
        },
      },
      event: { subscribe: async () => ({ stream: (async function* () {})() }) },
    },
    "client",
  );
  return { client, requests };
}

const settle = async (): Promise<void> => {
  for (let i = 0; i < 8; i++) await new Promise((resolve) => setTimeout(resolve, 0));
};

const statusEvent = (status: "busy" | "idle") => ({
  type: "session.status",
  properties: { sessionID: "s1", status: { type: status } },
});

/** Command fixture plus a controllable event stream, for lifecycle races. */
function raceFixture() {
  const requests: CommandRequest[] = [];
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
  const gate = deferred();
  const client = strict(
    {
      session: {
        get: async () => sessionRecord("s1"),
        messages: async () => [],
        command: async (request: CommandRequest) => {
          requests.push(request);
          await gate.promise;
          return {};
        },
      },
      event: { subscribe: async () => ({ stream }) },
    },
    "client",
  );
  return {
    client,
    requests,
    gate,
    send(event: unknown) {
      queue.push(event);
      waiters.shift()?.();
    },
  };
}

const TIMEOUT = { timeout: 5000 };

// --- request shape --------------------------------------------------------

test("runCommand sends goal, pause_goal and resume_goal with agent, model, args and identity", TIMEOUT, async () => {
  const { client, requests } = commandFixture();
  const controller = new SessionController({ client: client as never, cwd: "/work" });
  await controller.resume("s1");
  controller.setModel({ providerID: "anthropic", modelID: "claude-sonnet" });
  controller.setAgent("main");

  const cases = [
    { name: "goal", args: "status" },
    { name: "goal", args: "clear" },
    { name: "goal", args: "edit ship the parser fix" },
    { name: "pause_goal", args: "" },
    { name: "resume_goal", args: "" },
  ];
  for (const entry of cases) {
    // Each command is submitted as its own fresh idle command, exactly as the
    // UI does when the previous one has settled.
    controller.transcript.setPhase("idle");
    await controller.runCommand(entry.name, entry.args);
  }

  assert.deepEqual(
    requests.map((request) => ({ path: request.path, query: request.query, body: bodyWithoutId(request) })),
    cases.map((entry) => ({
      path: { id: "s1" },
      query: { directory: "/work" },
      body: {
        command: entry.name,
        arguments: entry.args,
        model: "anthropic/claude-sonnet",
        agent: "main",
      },
    })),
  );
  const ids = requests.map((request) => request.body.messageID);
  assert.equal(new Set(ids).size, ids.length, "every submission gets its own exact identity");
});

test("runCommand preserves the selected main, orchestrator and plan agent", TIMEOUT, async () => {
  for (const agent of ["main", "orchestrator", "plan"]) {
    const { client, requests } = commandFixture();
    const controller = new SessionController({ client: client as never, cwd: "/x" });
    await controller.resume("s1");
    controller.setAgent(agent);
    await controller.runCommand("goal", "status");
    assert.equal(requests[0]?.body?.agent, agent, `${agent}: selected agent is sent`);
    assert.deepEqual(bodyWithoutId(requests[0]!), { command: "goal", arguments: "status", agent });
  }
});

test("runCommand omits model when none is selected but still sends the agent", TIMEOUT, async () => {
  const { client, requests } = commandFixture();
  const controller = new SessionController({ client: client as never, cwd: "/x" });
  await controller.resume("s1");
  controller.setAgent("plan");
  await controller.runCommand("goal", "status");
  assert.deepEqual(bodyWithoutId(requests[0]!), { command: "goal", arguments: "status", agent: "plan" });
});

test("runCommand without an active session throws before dispatching", TIMEOUT, async () => {
  const { client, requests } = commandFixture();
  const controller = new SessionController({ client: client as never, cwd: "/x" });
  await assert.rejects(controller.runCommand("goal", "status"), /No active session/);
  assert.deepEqual(requests, []);
  assert.equal(controller.transcript.phase, "idle");
});

// --- failure recovery -----------------------------------------------------

test("runCommand marks an idle session busy before awaiting the command", TIMEOUT, async () => {
  const gate = deferred();
  const { client } = commandFixture({ behavior: () => gate.promise });
  const controller = new SessionController({ client: client as never, cwd: "/x" });
  await controller.resume("s1");

  const running = controller.runCommand("goal", "status");
  assert.equal(controller.transcript.phase, "busy");
  gate.resolve();
  await running;
  // A successful command stays busy until opencode reports its lifecycle.
  assert.equal(controller.transcript.phase, "busy");
});

test("a rejected command recovers an idle session to idle", TIMEOUT, async () => {
  const gate = deferred();
  const { client } = commandFixture({ behavior: () => gate.promise });
  const controller = new SessionController({ client: client as never, cwd: "/x" });
  await controller.resume("s1");

  const running = controller.runCommand("goal", "status");
  gate.reject(new Error("dispatch failed"));
  await assert.rejects(running, /dispatch failed/);
  assert.equal(controller.transcript.phase, "idle", "the failed command must not strand busy");
});

test("a rejected command preserves an already-busy session", TIMEOUT, async () => {
  const gate = deferred();
  const { client } = commandFixture({ behavior: () => gate.promise });
  const controller = new SessionController({ client: client as never, cwd: "/x" });
  await controller.resume("s1");
  controller.transcript.setPhase("busy");

  const running = controller.runCommand("goal", "status");
  gate.reject(new Error("dispatch failed"));
  await assert.rejects(running, /dispatch failed/);
  assert.equal(controller.transcript.phase, "busy", "an in-flight turn stays busy");
});

test("a rejected command preserves a retrying session and its reconnect flag", TIMEOUT, async () => {
  const gate = deferred();
  const { client } = commandFixture({ behavior: () => gate.promise });
  const controller = new SessionController({ client: client as never, cwd: "/x" });
  await controller.resume("s1");
  controller.transcript.setPhase("retry");
  controller.transcript.setReconnecting(true);

  const running = controller.runCommand("goal", "resume");
  gate.reject(new Error("dispatch failed"));
  await assert.rejects(running, /dispatch failed/);
  assert.equal(controller.transcript.phase, "retry");
  assert.equal(controller.transcript.reconnecting, true, "retry state is not cleared");
});

// --- lifecycle race -------------------------------------------------------

test("a newer busy lifecycle event is not clobbered by a rejected command", TIMEOUT, async () => {
  const { client, gate, send } = raceFixture();
  const controller = new SessionController({ client: client as never, cwd: "/x" });
  await controller.resume("s1");

  const running = controller.runCommand("goal", "status");
  assert.equal(controller.transcript.phase, "busy");
  send(statusEvent("busy"));
  await settle();
  assert.equal(controller.transcript.phase, "busy");

  gate.reject(new Error("dispatch failed"));
  await assert.rejects(running, /dispatch failed/);
  assert.equal(controller.transcript.phase, "busy", "the newer event owns the phase");
});

test("a newer idle lifecycle event wins over a rejected command", TIMEOUT, async () => {
  const { client, gate, send } = raceFixture();
  const controller = new SessionController({ client: client as never, cwd: "/x" });
  await controller.resume("s1");

  const running = controller.runCommand("goal", "status");
  send(statusEvent("idle"));
  await settle();
  assert.equal(controller.transcript.phase, "idle");

  gate.reject(new Error("dispatch failed"));
  await assert.rejects(running, /dispatch failed/);
  assert.equal(controller.transcript.phase, "idle");
});

// --- reconnect ------------------------------------------------------------

test("reconnect routes commands to the new client with the same session identity", TIMEOUT, async () => {
  const first = commandFixture({ sessionId: "s3" });
  const controller = new SessionController({ client: first.client as never, cwd: "/x" });
  await controller.resume("s3");

  const second = commandFixture({ sessionId: "s3" });
  await controller.reconnect({ client: second.client as never });
  controller.setAgent("plan");
  await controller.runCommand("resume_goal", "");

  assert.equal(first.requests.length, 0, "the old client receives nothing");
  assert.deepEqual(second.requests.map((request) => ({ path: request.path, query: request.query, body: bodyWithoutId(request) })), [
    {
      path: { id: "s3" },
      query: { directory: "/x" },
      body: { command: "resume_goal", arguments: "", agent: "plan" },
    },
  ]);
  assert.equal(controller.id, "s3");
});

// --- fixture guard --------------------------------------------------------

test("fixtures reject unexpected network, model and board dispatch", TIMEOUT, () => {
  const { client } = commandFixture();
  const loose = client as unknown as { session: Record<string, unknown>; config: unknown };
  assert.throws(
    () => loose.session.promptAsync,
    /unexpected client\.session dispatch: promptAsync/,
    "a network prompt must not be silently swallowed",
  );
  assert.throws(
    () => loose.config,
    /unexpected client dispatch: config/,
    "a model/catalog dispatch must not be silently swallowed",
  );
});
