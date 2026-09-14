/**
 * Bounded fixture for driving the REAL external OpenCode goal engine
 * (`@prevalentware/opencode-goal-plugin@0.1.48`) under `tsx --test`.
 *
 * This file deliberately does not import the plugin at module load. Callers must
 * establish a private sandbox (state path, HOME/XDG/MIDAS/PI roots) and then use
 * `loadGoalEngine()`, so environment-dependent state resolution happens after the
 * sandbox exists and before the plugin is imported.
 *
 * The fixture only fakes the OpenCode SDK/event surface (client + V2 context). All
 * lifecycle, persistence, limit, and continuation behaviour is the published
 * plugin's own code. No network, model, or board work is performed.
 *
 * The plugin has no type declarations; the module is imported through a runtime
 * specifier and every boundary is typed explicitly here.
 */
import { mkdirSync, mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

export const PLUGIN_PACKAGE = "@prevalentware/opencode-goal-plugin";
/** Exact version referenced by the global OpenCode config during the audit. */
export const PINNED_PLUGIN_VERSION = "0.1.48";

const repoRoot = fileURLToPath(new URL("../../", import.meta.url));

export interface PluginIdentity {
  name: string;
  version: string;
  packageJsonPath: string;
  exports: Record<string, unknown>;
}

/**
 * Identify the artifact that will actually be imported. Reads the installed
 * package manifest identity (name/version/exports) without touching goal state.
 */
export function installedPluginIdentity(): PluginIdentity {
  const packageJsonPath = join(
    repoRoot,
    "node_modules",
    "@prevalentware",
    "opencode-goal-plugin",
    "package.json",
  );
  const raw = JSON.parse(readFileSync(packageJsonPath, "utf8")) as {
    name?: unknown;
    version?: unknown;
    exports?: unknown;
  };
  return {
    name: typeof raw.name === "string" ? raw.name : "",
    version: typeof raw.version === "string" ? raw.version : "",
    packageJsonPath,
    exports: isRecord(raw.exports) ? raw.exports : {},
  };
}

export interface Sandbox {
  dir: string;
  stateFile: string;
}

export function createSandbox(prefix = "midas-goal-engine-"): Sandbox {
  const dir = mkdtempSync(join(tmpdir(), prefix));
  return { dir, stateFile: join(dir, "goals.json") };
}

/**
 * Establish private state/root environment. Called before `loadGoalEngine()` so
 * the plugin can never read or write the real user state or global config.
 */
export function installSandboxRoots(sandbox: Sandbox): void {
  process.env.OPENCODE_GOAL_STATE_PATH = sandbox.stateFile;
  process.env.HOME = join(sandbox.dir, "home");
  process.env.XDG_CONFIG_HOME = join(sandbox.dir, "config");
  process.env.XDG_DATA_HOME = join(sandbox.dir, "data");
  process.env.XDG_CACHE_HOME = join(sandbox.dir, "cache");
  process.env.XDG_STATE_HOME = join(sandbox.dir, "state");
  process.env.MIDAS_CONFIG_DIR = join(sandbox.dir, "midas");
  process.env.PI_CONFIG_DIR = join(sandbox.dir, "pi");
  process.env.MIDAS_NO_UPDATE = "1";
}

export interface GoalEngineModule {
  id: string;
  server: (input: { client: unknown }, options?: Record<string, unknown>) => Promise<V1Hooks>;
  setup: (context: unknown) => Promise<() => Promise<void>>;
}

export async function loadGoalEngine(): Promise<GoalEngineModule> {
  // Runtime specifier keeps TypeScript from trying to resolve a typed module that
  // does not ship declarations; the plugin's `exports["./server"]` is honoured.
  const specifier: string = `${PLUGIN_PACKAGE}/server`;
  const loaded = (await import(specifier)) as { default?: GoalEngineModule };
  if (!loaded.default || typeof loaded.default.server !== "function" || typeof loaded.default.setup !== "function") {
    throw new Error(`unexpected ${PLUGIN_PACKAGE}/server export shape`);
  }
  return loaded.default;
}

export type AnyRecord = Record<string, any>;

export interface V1Prompt {
  path: { id: string };
  body: {
    agent?: string;
    parts: Array<{ type: string; text: string }>;
  };
}

export interface V1Hooks {
  tool: Record<string, { execute: (args: AnyRecord, context: AnyRecord) => Promise<any> }>;
  event: (input: { event: AnyRecord }) => Promise<any>;
  config: (config: AnyRecord) => Promise<any> | any;
  dispose?: () => Promise<any> | any;
  "command.execute.before": (input: AnyRecord, output: AnyRecord) => Promise<any>;
  "chat.message": (input: AnyRecord, output: AnyRecord) => Promise<any>;
  "tool.execute.before": (input: AnyRecord) => Promise<any>;
  "tool.execute.after": (input: AnyRecord, output: AnyRecord) => Promise<any>;
  "experimental.chat.messages.transform": (input: AnyRecord, output: AnyRecord) => Promise<any>;
  "experimental.chat.system.transform": (input: AnyRecord, output: AnyRecord) => Promise<any>;
  "experimental.session.compacting": (input: AnyRecord, output: AnyRecord) => Promise<any>;
  "experimental.compaction.autocontinue": (input: AnyRecord, output: AnyRecord) => Promise<any>;
}

export interface V1Harness {
  readonly engine: GoalEngineModule;
  readonly sessionID: string;
  readonly stateFile: string;
  readonly prompts: V1Prompt[];
  readonly hooks: V1Hooks;
  callTool(name: string, args?: AnyRecord, context?: { sessionID?: string; agent?: string }): Promise<string>;
  emit(event: AnyRecord): Promise<void>;
  getGoal(): Promise<AnyRecord | null>;
  readPersistedGoal(): AnyRecord | null;
  patchPersistedGoal(patch: AnyRecord): void;
  promptTexts(): string[];
  dispose(): Promise<void>;
}

export interface CreateV1Options {
  engine: GoalEngineModule;
  sandbox: Sandbox;
  pluginOptions?: AnyRecord;
  sessionID?: string;
}

export async function createV1Harness(options: CreateV1Options): Promise<V1Harness> {
  const { engine, sandbox } = options;
  const sessionID = options.sessionID ?? "ses_goal_v1";
  process.env.OPENCODE_GOAL_STATE_PATH = sandbox.stateFile;

  const prompts: V1Prompt[] = [];
  const client = {
    session: {
      async promptAsync(arg: V1Prompt) {
        prompts.push(arg);
        return { data: {} };
      },
      async messages() {
        return { data: [] as unknown[] };
      },
    },
    app: {
      async log() {
        return true;
      },
    },
  };

  const hooks = await engine.server({ client }, options.pluginOptions ?? {});
  const harness: V1Harness = {
    engine,
    sessionID,
    stateFile: sandbox.stateFile,
    prompts,
    hooks,
    async callTool(name, args = {}, context = {}) {
      const tool = hooks.tool[name];
      if (!tool) throw new Error(`V1 tool ${name} is not registered`);
      const result = await tool.execute(args, {
        sessionID: context.sessionID ?? sessionID,
        agent: context.agent ?? "build",
      });
      return typeof result === "string" ? result : JSON.stringify(result);
    },
    async emit(event) {
      await hooks.event({ event });
    },
    async getGoal() {
      const raw = await hooks.tool.get_goal!.execute({}, { sessionID });
      return (JSON.parse(String(raw)) as { goal: AnyRecord | null }).goal;
    },
    readPersistedGoal() {
      return persistedGoal(sandbox.stateFile, sessionID);
    },
    patchPersistedGoal(patch) {
      patchPersistedState(sandbox.stateFile, sessionID, patch);
    },
    promptTexts() {
      return prompts.map((prompt) => prompt.body.parts.map((part) => part.text).join("\n"));
    },
    async dispose() {
      await hooks.dispose?.();
    },
  };
  return harness;
}

export type V2Tool = { name: string; execute: (args: AnyRecord, context: AnyRecord) => Promise<{ content: unknown }> };

export interface V2Prompt {
  sessionID: string;
  text: string;
  agents?: Array<{ name: string }>;
}

export interface V2Harness {
  readonly sessionID: string;
  readonly stateFile: string;
  readonly prompts: V2Prompt[];
  readonly tools: Map<string, V2Tool>;
  readonly commands: Map<string, AnyRecord>;
  readonly sessionHooks: Map<string, (input: AnyRecord) => unknown>;
  callTool(name: string, args?: AnyRecord, context?: { sessionID?: string; agent?: string }): Promise<string>;
  push(...events: AnyRecord[]): void;
  settle(ms?: number): Promise<void>;
  readPersistedGoal(): AnyRecord | null;
  patchPersistedGoal(patch: AnyRecord): void;
  dispose(): Promise<void>;
}

export interface CreateV2Options {
  engine: GoalEngineModule;
  sandbox: Sandbox;
  pluginOptions?: AnyRecord;
  sessionID?: string;
}

interface EventStream {
  subscribe(input?: { signal?: AbortSignal }): AsyncIterable<unknown>;
  push(event: AnyRecord): void;
  close(): void;
}

function createEventStream(): EventStream {
  const queue: unknown[] = [];
  const waiters: Array<(result: IteratorResult<unknown>) => void> = [];
  let closed = false;

  const flush = (): void => {
    while (waiters.length > 0 && (queue.length > 0 || closed)) {
      const waiter = waiters.shift()!;
      if (queue.length > 0) waiter({ value: queue.shift(), done: false });
      else waiter({ value: undefined, done: true });
    }
  };

  const iterator: AsyncIterator<unknown> = {
    next() {
      if (queue.length > 0) return Promise.resolve({ value: queue.shift(), done: false });
      if (closed) return Promise.resolve({ value: undefined, done: true });
      return new Promise((resolve) => waiters.push(resolve));
    },
    return() {
      closed = true;
      flush();
      return Promise.resolve({ value: undefined, done: true });
    },
  };

  return {
    subscribe(input) {
      const signal = input?.signal;
      if (signal) {
        if (signal.aborted) closed = true;
        else signal.addEventListener("abort", () => {
          closed = true;
          flush();
        }, { once: true });
      }
      return {
        [Symbol.asyncIterator]() {
          return iterator;
        },
      };
    },
    push(event) {
      if (closed) return;
      if (waiters.length > 0) {
        const waiter = waiters.shift()!;
        waiter({ value: event, done: false });
      } else {
        queue.push(event);
      }
    },
    close() {
      closed = true;
      flush();
    },
  };
}

export async function createV2Harness(options: CreateV2Options): Promise<V2Harness> {
  const { engine, sandbox } = options;
  const sessionID = options.sessionID ?? "ses_goal_v2";
  process.env.OPENCODE_GOAL_STATE_PATH = sandbox.stateFile;

  const prompts: V2Prompt[] = [];
  const tools = new Map<string, V2Tool>();
  const commands = new Map<string, AnyRecord>();
  const sessionHooks = new Map<string, (input: AnyRecord) => unknown>();
  const stream = createEventStream();
  const registration = { dispose: async () => undefined };

  const context = {
    options: options.pluginOptions ?? {},
    session: {
      async prompt(arg: V2Prompt) {
        prompts.push(arg);
        return { data: {} };
      },
      async hook(name: string, handler: (input: AnyRecord) => unknown) {
        sessionHooks.set(`session.${name}`, handler);
        return registration;
      },
    },
    command: {
      async list() {
        return { data: [...commands.values()].map((command) => ({ name: command.name })) };
      },
      async transform(apply: (draft: { add: (command: AnyRecord) => void }) => unknown) {
        await apply({ add: (command) => commands.set(String(command.name), command) });
        return registration;
      },
    },
    tool: {
      async transform(apply: (draft: { add: (tool: V2Tool) => void }) => unknown) {
        await apply({ add: (tool) => tools.set(tool.name, tool) });
        return registration;
      },
      async hook(_name: string, _handler: (input: AnyRecord) => unknown) {
        return registration;
      },
    },
    event: {
      subscribe(input?: { signal?: AbortSignal }) {
        return stream.subscribe(input);
      },
    },
  };

  const dispose = await engine.setup(context);

  return {
    sessionID,
    stateFile: sandbox.stateFile,
    prompts,
    tools,
    commands,
    sessionHooks,
    async callTool(name, args = {}, toolContext = {}) {
      const tool = tools.get(name);
      if (!tool) throw new Error(`V2 tool ${name} is not registered`);
      const result = await tool.execute(args, {
        sessionID: toolContext.sessionID ?? sessionID,
        agent: toolContext.agent ?? "build",
      });
      return typeof result.content === "string" ? result.content : JSON.stringify(result.content);
    },
    push(...events) {
      for (const event of events) stream.push(event);
    },
    async settle(ms = 80) {
      await sleep(ms);
    },
    readPersistedGoal() {
      return persistedGoal(sandbox.stateFile, sessionID);
    },
    patchPersistedGoal(patch) {
      patchPersistedState(sandbox.stateFile, sessionID, patch);
    },
    async dispose() {
      stream.close();
      await dispose();
    },
  };
}

/** Node test runner sleep; used only to bound timer-driven engine behaviour. */
export function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

export function nowSeconds(): number {
  return Math.floor(Date.now() / 1000);
}

function persistedState(stateFile: string): { version: number; goals: Record<string, AnyRecord> } {
  try {
    const parsed = JSON.parse(readFileSync(stateFile, "utf8")) as Partial<{
      version: number;
      goals: Record<string, AnyRecord>;
    }>;
    if (parsed && isRecord(parsed.goals)) return { version: 1, goals: parsed.goals };
  } catch {
    // Missing or unreadable state is treated as empty; the engine owns creation.
  }
  return { version: 1, goals: {} };
}

function persistedGoal(stateFile: string, sessionID: string): AnyRecord | null {
  return persistedState(stateFile).goals[sessionID] ?? null;
}

function patchPersistedState(stateFile: string, sessionID: string, patch: AnyRecord): void {
  const state = persistedState(stateFile);
  const goal = state.goals[sessionID];
  if (!goal) throw new Error(`cannot patch goal ${sessionID}: not present in state file`);
  Object.assign(goal, patch);
  mkdirSync(dirname(stateFile), { recursive: true });
  writeFileSync(stateFile, `${JSON.stringify(state, null, 2)}\n`);
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}
