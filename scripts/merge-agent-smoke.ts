/**
 * Opt-in smoke harness for the REAL conflict-resolution path.
 *
 * The existing suite (`src/tasks/merge-safety.test.ts`) and the disjoint Lightrig
 * merges only ever exercise `mergeTask` with an injected `ConflictResolver`.
 * This harness drives the actual production entry points instead:
 *
 *   - a genuinely independent `git init` repo under a `mkdtemp` directory,
 *   - `TaskBoard.add` → `runTask` with a deterministic worker callback (no model),
 *   - a real, independently-committed target change so the merge truly conflicts,
 *   - `mergeTask` called WITHOUT the resolver argument, so its default
 *     `mergeAgentResolve` (the real `merge` subagent) must resolve the conflict.
 *
 * Nothing here touches the caller's worktree. Every git operation runs in the
 * disposable fixture or in a throwaway probe worktree; the harness records the
 * invoking repository's HEAD/status before and after to prove it was not mutated.
 *
 * The real model call is opt-in: running this file prints an "unavailable"
 * failure unless `MIDAS_RUN_MERGE_AGENT_SMOKE=1` is set. When enabled, the
 * top-level process spawns a detached child in its own process group and enforces
 * an independent wall-clock deadline; on expiry the whole group (including the
 * `opencode serve` backend) is killed. Exactly one conflict-resolution attempt is
 * made and there are no retries or continuing agents.
 *
 * Unit tests use the deterministic seam (`runDeterministicSmoke`) so they never
 * make a model call; only the CLI child ever uses `runMergeAgentSmoke`.
 */
import { spawn } from "node:child_process";
import { createRequire } from "node:module";
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, symlinkSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import { TaskBoard, git, gitAsync, type Attempt, type Task } from "../src/tasks/board.ts";
import { mergeTask, runTask } from "../src/tasks/runner.ts";
import type { ConflictResolver, OutputSink } from "../src/tasks/runner.ts";

/** Set to `1` to allow the real merge-agent model call. */
export const SMOKE_ENV = "MIDAS_RUN_MERGE_AGENT_SMOKE";
/** Override the independent wall-clock deadline (milliseconds). */
export const DEADLINE_ENV = "MIDAS_MERGE_AGENT_SMOKE_DEADLINE_MS";
/** Internal: fixture root handed from the deadline parent to the model child. */
export const FIXTURE_ROOT_ENV = "MIDAS_MERGE_AGENT_SMOKE_FIXTURE_ROOT";
/** Internal: where the model child writes its JSON evidence. */
export const EVIDENCE_PATH_ENV = "MIDAS_MERGE_AGENT_SMOKE_EVIDENCE";
/** Internal: marks the model child invocation. */
export const CHILD_FLAG = "--run-real-merge-smoke";
/** Default child-process deadline; the merge agent's own timeout is 20 minutes. */
export const DEFAULT_DEADLINE_MS = 300_000;

export const SETTINGS_FILE = "settings.env";
export const TARGET_MARKER = "target.marker";

const HEADER = "# Independent settings. A correct merge keeps every value.\n";
const CONFLICT_MARKER_RE = /^(?:<{7}|={7}|>{7})/m;

function settingsLine(profile: string, retries: string, timeout: string): string {
  return `profile=${profile} retries=${retries} timeout=${timeout}\n`;
}

/** Baseline: both branches will edit the single settings line, forcing a conflict. */
export const SETTINGS_BASE = HEADER + settingsLine("default", "3", "30");
const WORKER_EDIT = { from: "retries=3", to: "retries=5" };
const TARGET_EDIT = { from: "profile=default", to: "profile=prod" };
/** What a semantically correct resolution must contain. */
export const SETTINGS_COMBINED = HEADER + settingsLine("prod", "5", "30");
export const RESOLUTION_TOKENS = ["profile=prod", "retries=5", "timeout=30"];

export const SMOKE_CONTRACT = {
  title: "Retain independent settings during a real merge",
  instructions:
    "The worker deterministically rewrites the retries setting; the target branch independently rewrites the profile setting. The real merge resolver must combine the two independent values rather than pick a side.",
  checks: [
    "test -s settings.env",
    "grep -q 'retries=5' settings.env",
    "grep -q 'timeout=30' settings.env",
    // Only asserted once the merge is being validated onto the advanced target.
    "if [ -f target.marker ]; then grep -q 'profile=prod' settings.env; fi",
    "! grep -qE '^(<<<<<<<|=======|>>>>>>>)' settings.env",
    // Proves the isolated integration worktree can resolve the repo's deps.
    "node -e \"require.resolve('typescript')\"",
  ],
};

const require = createRequire(import.meta.url);
/** The real dependency store, so fixture checks mirror a real checkout. */
function dependencyStore(): string | undefined {
  try {
    return dirname(dirname(require.resolve("typescript/package.json")));
  } catch {
    return undefined;
  }
}

export interface SmokeFixture {
  root: string;
  board: TaskBoard;
}

/**
 * Create an independently initialized repository. Git identity is supplied by
 * environment variables only; no Git configuration is written anywhere.
 */
export function createFixture(rootArg?: string): SmokeFixture {
  const root = rootArg ? resolve(rootArg) : mkdtempSync(join(tmpdir(), "midas-merge-agent-smoke-"));
  mkdirSync(root, { recursive: true });
  process.env.GIT_AUTHOR_NAME = "Midas merge-agent smoke";
  process.env.GIT_AUTHOR_EMAIL = "smoke@example.invalid";
  process.env.GIT_COMMITTER_NAME = "Midas merge-agent smoke";
  process.env.GIT_COMMITTER_EMAIL = "smoke@example.invalid";
  git(root, "init", "-b", "main");
  writeFileSync(join(root, ".gitignore"), "node_modules/\n");
  writeFileSync(join(root, SETTINGS_FILE), SETTINGS_BASE);
  const store = dependencyStore();
  if (store && !existsSync(join(root, "node_modules"))) {
    try { symlinkSync(store, join(root, "node_modules"), "dir"); } catch { /* a missing store fails checks loudly */ }
  }
  git(root, "add", ".");
  git(root, "commit", "-m", "fixture: independent settings");
  return { root, board: new TaskBoard(root) };
}

export function cleanupFixture(fixture: SmokeFixture): void {
  rmSync(fixture.root, { recursive: true, force: true });
}

function replaceOnce(text: string, from: string, to: string): string {
  if (!text.includes(from)) throw new Error(`fixture marker not found: ${from}`);
  return text.replace(from, to);
}

/** Deterministic worker: edits the task-side setting and no model is called. */
export function deterministicWorker(_task: Task, attempt: Attempt): Promise<void> {
  const path = join(attempt.worktree, SETTINGS_FILE);
  writeFileSync(path, replaceOnce(readFileSync(path, "utf8"), WORKER_EDIT.from, WORKER_EDIT.to));
  return Promise.resolve();
}

/** Independently advance the target branch after the task branch was created. */
export function advanceTarget(root: string): string {
  const path = join(root, SETTINGS_FILE);
  writeFileSync(path, replaceOnce(readFileSync(path, "utf8"), TARGET_EDIT.from, TARGET_EDIT.to));
  writeFileSync(join(root, TARGET_MARKER), "target advanced after the task branch was created\n");
  git(root, "add", "--", SETTINGS_FILE, TARGET_MARKER);
  git(root, "commit", "-m", "fixture: independent target setting");
  return git(root, "rev-parse", "HEAD");
}

export interface ConflictProbe {
  mergeFailed: boolean;
  conflicted: string[];
  markerSnippet: string;
}

/**
 * Deterministically prove the prepared merge conflicts, in a throwaway detached
 * worktree. This never invokes a model and is aborted/removed afterwards, so the
 * fixture root's HEAD and status are untouched by the probe.
 */
export async function probeConflict(root: string, base: string, resultCommit: string): Promise<ConflictProbe> {
  const holder = mkdtempSync(join(tmpdir(), "midas-merge-probe-"));
  const worktree = join(holder, "worktree");
  try {
    await gitAsync(root, "worktree", "add", "--detach", worktree, base);
    let mergeFailed = false;
    try { await gitAsync(worktree, "merge", "--no-edit", "--no-ff", resultCommit); }
    catch { mergeFailed = true; }
    const conflicted = (await gitAsync(worktree, "diff", "--name-only", "--diff-filter=U"))
      .split("\n").map((path) => path.trim()).filter(Boolean);
    let markerSnippet = "";
    for (const path of conflicted) {
      const content = readFileSync(join(worktree, path), "utf8");
      if (CONFLICT_MARKER_RE.test(content)) { markerSnippet = content; break; }
    }
    return { mergeFailed, conflicted, markerSnippet };
  } finally {
    await gitAsync(root, "worktree", "remove", "--force", worktree).catch(() => undefined);
    rmSync(holder, { recursive: true, force: true });
  }
}

export interface GitState { head: string; status: string }

function captureGitState(cwd: string): GitState {
  try { return { head: git(cwd, "rev-parse", "HEAD"), status: git(cwd, "status", "--porcelain") }; }
  catch (error) { return { head: "", status: `unavailable: ${String(error)}` }; }
}

export type SmokeMode = "real-merge-agent" | "deterministic-resolver";

export interface SmokeEvidence {
  ok: boolean;
  mode: SmokeMode;
  fixtureRoot: string;
  taskId?: string;
  checks: string[];
  worker?: { deterministic: true; resultCommit: string; status: string };
  conflict?: ConflictProbe & { rootUnchangedByProbe: boolean };
  resolver: { mode: SmokeMode; defaultResolver: boolean; modelOutput: string; mergeOutputPreview: string };
  resolution?: { content: string; tokensPresent: Record<string, boolean>; markersAbsent: boolean };
  ancestry?: { base: string; result: string; mergedCommit: string; parents: string[]; ok: boolean };
  root?: { headBeforeMerge: string; headAfterMerge: string; statusAfterMerge: string; clean: boolean; advanced: boolean };
  source: { before: GitState; after: GitState; unchanged: boolean };
  mergeStatus?: string;
  mergeBlocked?: string;
  mergeMs?: number;
  elapsedMs: number;
  error?: string;
}

export interface SmokeRunOptions {
  output?: OutputSink;
  /** Pre-created fixture root (used by the deadline parent). */
  fixtureRoot?: string;
  /** Keep the fixture for inspection (deterministic tests only). */
  keepFixture?: boolean;
}

interface WorkflowInput extends SmokeRunOptions {
  mode: SmokeMode;
  /** Omitted on the real path, so `mergeTask`'s default resolver runs. */
  resolveConflict?: ConflictResolver;
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

/**
 * The merge agent writes its prose before the integration checks run, so the
 * model text is whatever precedes the first check line.
 */
function extractModelText(mergeOutput: string): string {
  const marker = mergeOutput.indexOf("Check: ");
  return (marker >= 0 ? mergeOutput.slice(0, marker) : mergeOutput).trim();
}

async function runWorkflow(input: WorkflowInput): Promise<SmokeEvidence> {
  const startedAt = Date.now();
  const sourceBefore = captureGitState(process.cwd());
  const fixture = createFixture(input.fixtureRoot);
  const output: OutputSink = input.output ?? (() => {});
  const mergeChunks: string[] = [];
  let taskId: string | undefined;
  const evidence: SmokeEvidence = {
    ok: false,
    mode: input.mode,
    fixtureRoot: fixture.root,
    checks: [...SMOKE_CONTRACT.checks],
    resolver: { mode: input.mode, defaultResolver: input.resolveConflict === undefined, modelOutput: "", mergeOutputPreview: "" },
    source: { before: sourceBefore, after: sourceBefore, unchanged: true },
    elapsedMs: 0,
  };
  try {
    const task = fixture.board.add(SMOKE_CONTRACT);
    taskId = task.id;
    await runTask(fixture.board, task.id, deterministicWorker, undefined, { output });
    const stored = fixture.board.get(task.id);
    const attempt = stored.attempts.at(-1)!;
    if (!attempt.result) throw new Error("deterministic worker produced no validated result commit");
    const resultCommit = attempt.result;
    evidence.worker = { deterministic: true, resultCommit, status: stored.status };

    const rootBeforeMerge = captureGitState(fixture.root);
    evidence.root = { headBeforeMerge: rootBeforeMerge.head, headAfterMerge: "", statusAfterMerge: "", clean: false, advanced: false };

    const base = advanceTarget(fixture.root);
    evidence.ancestry = { base, result: resultCommit, mergedCommit: "", parents: [], ok: false };

    const beforeProbe = captureGitState(fixture.root);
    const probe = await probeConflict(fixture.root, base, resultCommit);
    const afterProbe = captureGitState(fixture.root);
    evidence.conflict = {
      ...probe,
      rootUnchangedByProbe: beforeProbe.head === afterProbe.head && beforeProbe.status === afterProbe.status,
    };
    if (!probe.mergeFailed || probe.conflicted.length === 0 || !probe.markerSnippet) {
      evidence.error = "fixture precondition failed: the prepared merge did not conflict";
      return evidence;
    }

    // The production call: only three arguments, so the default real merge-agent
    // resolver is used. `input.resolveConflict` is set exclusively by unit tests.
    const mergeSink: OutputSink = (chunk) => { mergeChunks.push(chunk); output(chunk); };
    const mergeStarted = Date.now();
    await mergeTask(fixture.board, task.id, mergeSink, input.resolveConflict);
    evidence.mergeMs = Date.now() - mergeStarted;

    const mergedTask = fixture.board.get(task.id);
    evidence.mergeStatus = mergedTask.merge;
    evidence.mergeBlocked = mergedTask.mergeBlocked;
    const mergedCommit = mergedTask.mergedCommit;
    if (!mergedCommit) throw new Error("mergeTask completed without a merged commit");

    const content = readFileSync(join(fixture.root, SETTINGS_FILE), "utf8");
    const tokensPresent: Record<string, boolean> = {};
    for (const token of RESOLUTION_TOKENS) tokensPresent[token] = content.includes(token);
    const markersAbsent = !CONFLICT_MARKER_RE.test(content);
    evidence.resolution = { content, tokensPresent, markersAbsent };

    const parents = git(fixture.root, "rev-list", "--parents", "-n", "1", mergedCommit).split(/\s+/).slice(1);
    const ancestryOk = parents.length === 2 && parents[0] === base && parents.includes(resultCommit);
    evidence.ancestry = { base, result: resultCommit, mergedCommit, parents, ok: ancestryOk };

    const rootAfter = captureGitState(fixture.root);
    evidence.root = {
      headBeforeMerge: rootBeforeMerge.head,
      headAfterMerge: rootAfter.head,
      statusAfterMerge: rootAfter.status,
      clean: rootAfter.status === "",
      advanced: rootAfter.head === mergedCommit,
    };

    const sourceAfter = captureGitState(process.cwd());
    evidence.source = {
      before: sourceBefore,
      after: sourceAfter,
      unchanged: sourceBefore.head === sourceAfter.head && sourceBefore.status === sourceAfter.status,
    };

    const tokensOk = Object.values(tokensPresent).every(Boolean);
    evidence.ok = mergedTask.merge === "merged" && markersAbsent && tokensOk && ancestryOk
      && rootAfter.status === "" && rootAfter.head === mergedCommit
      && evidence.conflict.rootUnchangedByProbe && evidence.source.unchanged;
    if (!evidence.ok) {
      evidence.error = [
        mergedTask.merge !== "merged" ? `merge status is ${mergedTask.merge}` : "",
        !markersAbsent ? "conflict markers remain" : "",
        !tokensOk ? `missing combined settings: ${RESOLUTION_TOKENS.filter((token) => !tokensPresent[token]).join(", ")}` : "",
        !ancestryOk ? "merged commit is not a merge of the validated inputs" : "",
        rootAfter.status !== "" ? `fixture root is dirty: ${rootAfter.status}` : "",
        rootAfter.head !== mergedCommit ? "fixture root HEAD is not the validated merge" : "",
        !evidence.conflict.rootUnchangedByProbe ? "conflict probe changed the fixture root" : "",
        !evidence.source.unchanged ? "invoking worktree changed during the smoke" : "",
      ].filter(Boolean).join("; ");
    }
    return evidence;
  } catch (error) {
    evidence.error = errorMessage(error);
    try {
      if (taskId) {
        const current = fixture.board.get(taskId);
        evidence.mergeStatus = current.merge;
        evidence.mergeBlocked = current.mergeBlocked;
      }
    } catch { /* board read best effort */ }
    try {
      const content = readFileSync(join(fixture.root, SETTINGS_FILE), "utf8");
      const tokensPresent: Record<string, boolean> = {};
      for (const token of RESOLUTION_TOKENS) tokensPresent[token] = content.includes(token);
      evidence.resolution = { content, tokensPresent, markersAbsent: !CONFLICT_MARKER_RE.test(content) };
    } catch { /* settings file absent best effort */ }
    try {
      const rootAfter = captureGitState(fixture.root);
      evidence.root = {
        headBeforeMerge: evidence.root?.headBeforeMerge ?? "",
        headAfterMerge: rootAfter.head,
        statusAfterMerge: rootAfter.status,
        clean: rootAfter.status === "",
        advanced: false,
      };
    } catch { /* root state best effort */ }
    return evidence;
  } finally {
    const merged = mergeChunks.join("");
    evidence.resolver.mergeOutputPreview = merged.slice(0, 4000);
    evidence.resolver.modelOutput = extractModelText(merged);
    const sourceAfter = captureGitState(process.cwd());
    evidence.source = {
      before: sourceBefore,
      after: sourceAfter,
      unchanged: sourceBefore.head === sourceAfter.head && sourceBefore.status === sourceAfter.status,
    };
    evidence.elapsedMs = Date.now() - startedAt;
    if (!input.keepFixture) cleanupFixture(fixture);
  }
}

/** Real path: `mergeTask` is called without a resolver, so the real agent runs. */
export async function runMergeAgentSmoke(options: SmokeRunOptions = {}): Promise<SmokeEvidence> {
  return runWorkflow({ ...options, mode: "real-merge-agent" });
}

/** Deterministic seam used only by the unit tests; never calls a model. */
export async function runDeterministicSmoke(
  resolveConflict: ConflictResolver,
  options: SmokeRunOptions = {},
): Promise<SmokeEvidence> {
  return runWorkflow({ ...options, mode: "deterministic-resolver", resolveConflict });
}

function parseDeadline(): number {
  const raw = Number(process.env[DEADLINE_ENV] ?? DEFAULT_DEADLINE_MS);
  return Number.isFinite(raw) && raw > 0 ? Math.floor(raw) : DEFAULT_DEADLINE_MS;
}

function writeStdout(chunk: string): void { process.stdout.write(chunk); }

async function runChild(): Promise<void> {
  process.env.MIDAS_NO_UPDATE = "1";
  const evidencePath = process.env[EVIDENCE_PATH_ENV];
  const evidence = await runMergeAgentSmoke({ output: writeStdout, fixtureRoot: process.env[FIXTURE_ROOT_ENV] });
  const json = JSON.stringify(evidence, null, 2);
  process.stdout.write(`\n${json}\n`);
  if (evidencePath) {
    try { writeFileSync(evidencePath, json); } catch { /* parent reports the missing evidence */ }
  }
  process.exitCode = evidence.ok ? 0 : 1;
}

async function runParent(repoRoot: string): Promise<void> {
  const script = fileURLToPath(import.meta.url);
  const workRoot = mkdtempSync(join(tmpdir(), "midas-merge-agent-smoke-"));
  const evidencePath = join(workRoot, "evidence.json");
  const fixtureRoot = join(workRoot, "repo");
  const deadlineMs = parseDeadline();
  const child = spawn(process.execPath, ["--import", "tsx", script, CHILD_FLAG], {
    cwd: repoRoot,
    env: {
      ...process.env,
      [SMOKE_ENV]: "1",
      [EVIDENCE_PATH_ENV]: evidencePath,
      [FIXTURE_ROOT_ENV]: fixtureRoot,
      MIDAS_NO_UPDATE: "1",
    },
    stdio: ["ignore", "inherit", "inherit"],
    detached: true,
  });
  const pid = child.pid;
  const killGroup = (signal: NodeJS.Signals): void => {
    if (pid === undefined) return;
    try { process.kill(-pid, signal); } catch { try { child.kill(signal); } catch { /* already gone */ } }
  };
  const cleanup = (): void => rmSync(workRoot, { recursive: true, force: true });
  let timedOut = false;
  let interrupted = false;
  const timer = setTimeout(() => {
    timedOut = true;
    process.stderr.write(`\nmerge-agent smoke: deadline ${deadlineMs}ms exceeded; killing fixture process group\n`);
    killGroup("SIGKILL");
  }, deadlineMs);
  const onSignal = (): void => { interrupted = true; killGroup("SIGTERM"); cleanup(); process.exit(130); };
  process.once("SIGINT", onSignal);
  process.once("SIGTERM", onSignal);
  const exitCode = await new Promise<number | null>((done) => child.once("exit", (code) => done(code)));
  clearTimeout(timer);
  process.removeListener("SIGINT", onSignal);
  process.removeListener("SIGTERM", onSignal);
  if (interrupted) return;
  if (timedOut) {
    cleanup();
    process.stderr.write("merge-agent smoke: FAIL (deadline)\n");
    process.exitCode = 1;
    return;
  }
  let evidence: { ok?: unknown; error?: unknown } | undefined;
  try { evidence = JSON.parse(readFileSync(evidencePath, "utf8")) as typeof evidence; } catch { evidence = undefined; }
  cleanup();
  if (exitCode === 0 && evidence?.ok === true) {
    process.stdout.write("merge-agent smoke: PASS\n");
    process.exitCode = 0;
    return;
  }
  const detail = evidence?.error ? `: ${String(evidence.error)}` : "";
  process.stderr.write(`merge-agent smoke: FAIL (exit ${exitCode ?? "signal"})${detail}\n`);
  process.exitCode = 1;
}

async function main(): Promise<void> {
  const repoRoot = fileURLToPath(new URL("../", import.meta.url));
  if (process.argv.includes(CHILD_FLAG)) {
    await runChild();
    return;
  }
  if (process.env[SMOKE_ENV] !== "1") {
    process.stderr.write(
      `merge-agent smoke unavailable: set ${SMOKE_ENV}=1 to run the real merge agent. ` +
      "No model call was made; the deterministic suite covers the fixture without one.\n",
    );
    process.exitCode = 2;
    return;
  }
  await runParent(repoRoot);
}

const invokedDirectly = process.argv[1] !== undefined
  && import.meta.url === pathToFileURL(process.argv[1]).href;
if (invokedDirectly) {
  void main().catch((error) => {
    process.stderr.write(`merge-agent smoke error: ${errorMessage(error)}\n`);
    process.exitCode = 1;
  });
}
