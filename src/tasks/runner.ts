import { spawn } from "node:child_process";
import { existsSync } from "node:fs";
import { once } from "node:events";
import type { Session } from "@opencode-ai/sdk";
import { TaskBoard, gitAsync, type Task, type Attempt } from "./board.ts";
import { startServer } from "../opencode/server.ts";
import { midasOpencodeConfig } from "../config/pi.ts";
import { BOARD_WORKER_AGENT, MERGE_AGENT } from "../lib/agents.ts";

/**
 * Where a worker run's output (check commands, the agent's final text) is sent.
 * Embedded callers must inject a non-writing sink: the TUI owns the terminal,
 * so anything a task subprocess prints to stdout corrupts the screen. The
 * standalone CLI opts into `stdoutOutput` explicitly.
 */
export type OutputSink = (chunk: string) => void;
const discardOutput: OutputSink = () => {};
/** Streams output to the process stdout; only for the standalone `midas task` CLI. */
export const stdoutOutput: OutputSink = (chunk) => { process.stdout.write(chunk); };

/** Bound on how much failed-check output is folded into the task detail. */
const FAILURE_TAIL = 2000;

/** How many consecutive transient failures are retried before a task is parked. */
export const TRANSIENT_RETRY_BUDGET = 2;

/** Whether a run failure should be retried automatically or parked for a human. */
export type FailureKind = "transient" | "deterministic";

/**
 * Failures that describe the work itself. Checked before the transient patterns
 * so a failed check whose output merely mentions the network is never retried.
 */
const DETERMINISTIC_FAILURES: RegExp[] = [
  /Check failed/i,
  /did not report completion/i,
  /Worker needs interactive input/i,
  /changed HEAD or branch/i,
  /Checks modified/i,
  /Commit hooks changed validated content/i,
  /Worktree changed during commit/i,
];

/**
 * Infrastructure failures: the worker/backend could not be provisioned, the
 * transport dropped, or the worker timed out. These say nothing about whether
 * the task itself can succeed, so the dispatcher re-picks them automatically.
 */
const TRANSIENT_FAILURES: RegExp[] = [
  /did not create a (worker|merge) session/i,
  /session (create|creation)|create session|creating session/i,
  /fetch failed|failed to fetch/i,
  /event stream closed/i,
  /exceeded (60|20) minutes/i,
  /socket hang up|ECONNRESET|ECONNREFUSED|ECONNABORTED|ETIMEDOUT|ENOTFOUND|EAI_AGAIN|EPIPE|UND_ERR|other side closed/i,
  /network (error|failure)|networkerror/i,
  /unable to access|Could not resolve host|Connection timed out|operation timed out|remote end hung up|RPC failed|early EOF|Failed to connect|transport error|TLS|SSL/i,
];

/**
 * Pure classifier for a task run failure. Only transport/backend/worker errors
 * are transient; anything unrecognised stays deterministic so the loop can never
 * retry forever. Pause/cancel are handled by the caller before classification.
 */
export function classifyFailure(error: unknown): FailureKind {
  const message = error instanceof Error ? error.message : String(error);
  if (DETERMINISTIC_FAILURES.some((pattern) => pattern.test(message))) return "deterministic";
  if (TRANSIENT_FAILURES.some((pattern) => pattern.test(message))) return "transient";
  return "deterministic";
}

async function checks(task: Task, cwd: string, output: OutputSink): Promise<void> {
  for (const command of task.checks) {
    output(`Check: ${command}\n`);
    await new Promise<void>((resolve, reject) => {
      const child = spawn("/bin/bash", ["-c", command], { cwd, stdio: ["ignore", "pipe", "pipe"] });
      let captured = "";
      const onData = (chunk: Buffer): void => {
        const text = chunk.toString();
        captured = (captured + text).slice(-FAILURE_TAIL);
        output(text);
      };
      child.stdout?.on("data", onData);
      child.stderr?.on("data", onData);
      child.on("error", reject);
      child.on("exit", (code, signal) => {
        if (code === 0) return resolve();
        const tail = captured.trimEnd();
        reject(new Error(`Check failed (${code ?? signal}): ${command}${tail ? `\n${tail}` : ""}`));
      });
    });
  }
}

/** Steer text sent to a running worker when its task contract changes. */
export function revisionNotice(task: Task): string {
  return `Task ${task.id} was updated while you were working. Re-read its contract from the board (\`midas task list\`) and adjust your work to match.\n\nUpdated title: ${task.title}\nUpdated instructions:\n${task.instructions}`;
}

export type Worker = (task: Task, attempt: Attempt, onSession: (id: string) => void, signal?: AbortSignal, onRevision?: (cb: (task: Task) => void) => void, output?: OutputSink) => Promise<void>;
const worker: Worker = async (task, attempt, onSession, signal, onRevision, output = discardOutput) => {
  const server = await startServer({ cwd: attempt.worktree, configContent: JSON.stringify(midasOpencodeConfig(attempt.worktree)) });
  let session: Session | undefined;
  const abort = new AbortController();
  if (signal) {
    if (signal.aborted) abort.abort(signal.reason);
    else signal.addEventListener("abort", () => abort.abort(signal.reason), { once: true });
  }
  const timeout = setTimeout(() => abort.abort(new Error("Worker exceeded 60 minutes")), 60 * 60_000);
  try {
    session = await server.client.session.create({ query: { directory: attempt.worktree }, body: { title: `${task.id}: ${task.title}` } }) as unknown as Session;
    if (!session?.id) throw new Error("Backend did not create a worker session");
    onSession(session.id);
    // An edited contract is steered into the live worker so it can re-review and
    // adapt instead of finishing against a stale brief.
    onRevision?.((updated) => {
      if (!session || abort.signal.aborted) return;
      void server.client.session.prompt({
        path: { id: session.id }, query: { directory: attempt.worktree }, signal: abort.signal,
        body: { agent: BOARD_WORKER_AGENT, parts: [{ type: "text", text: revisionNotice(updated) }] },
      }).catch(() => undefined);
    });
    const events = await server.client.event.subscribe({ signal: abort.signal });
    void (async () => {
      for await (const event of events.stream) {
        if (event.type === "permission.updated" || (event as { type: string }).type === "question.asked") abort.abort(new Error("Worker needs interactive input; inspect its session before retrying"));
      }
      if (!abort.signal.aborted) abort.abort(new Error("Worker event stream closed"));
    })().catch((error) => { if (!abort.signal.aborted) abort.abort(error); });
    const result = await server.client.session.prompt({ path: { id: session.id }, query: { directory: attempt.worktree }, signal: abort.signal,
      body: { agent: BOARD_WORKER_AGENT, parts: [{ type: "text", text: `Execute only this task in ${attempt.worktree}. Do not commit, change branches, merge, or edit the board. The controller owns Git and validation. Finish your final response with MIDAS_TASK_DONE only if the task is fully implemented; otherwise explain the blocker.\n\n${task.title}\n${task.instructions}\n\nRequired checks:\n${task.checks.join("\n")}` }] },
    }) as unknown as { info?: { error?: unknown }; parts?: Array<{ type: string; text?: string }> };
    if (result.info?.error) throw new Error(`Worker failed: ${JSON.stringify(result.info.error)}`);
    const text = (result.parts ?? []).filter((p) => p.type === "text").map((p) => p.text ?? "").join("\n");
    output(text + "\n");
    if (!text.trim().endsWith("MIDAS_TASK_DONE")) {
      const explanation = text.trim().slice(-FAILURE_TAIL);
      throw new Error(`Worker did not report completion${explanation ? `\n${explanation}` : ""}`);
    }
  } finally {
    clearTimeout(timeout);
    abort.abort();
    if (session) await server.client.session.abort({ path: { id: session.id }, query: { directory: attempt.worktree }, signal: AbortSignal.timeout(5000) }).catch(() => {});
    if (server.proc.exitCode === null && server.proc.signalCode === null) {
      const exited = once(server.proc, "exit");
      server.close();
      const kill = setTimeout(() => server.proc.kill("SIGKILL"), 5000);
      try { await exited; } finally { clearTimeout(kill); }
    }
  }
};

export interface RunOptions {
  /** How often to poll for contract edits to steer into the worker. Default 2s. */
  revisionPollMs?: number;
  /**
   * Where worker/check output goes. Omitted in the TUI so subprocess output can
   * never scribble over the renderer; the CLI passes `stdoutOutput`.
   */
  output?: OutputSink;
}

export async function runTask(board: TaskBoard, id: string, execute: Worker = worker, signal?: AbortSignal, options: RunOptions = {}): Promise<void> {
  const output = options.output ?? discardOutput;
  board.get(id);
  const unlock = board.lock(`task-${id}`);
  const previousAttempts = board.get(id).attempts.length;
  let lastRevision = board.get(id).revision ?? 1;
  let notifyRevision: ((task: Task) => void) | undefined;
  const watcher = setInterval(() => {
    if (!notifyRevision) return;
    try {
      const latest = board.get(id);
      const revision = latest.revision ?? 1;
      if (revision !== lastRevision) { lastRevision = revision; notifyRevision(latest); }
    } catch { /* task removed; ignore */ }
  }, options.revisionPollMs ?? 2000);
  watcher.unref?.();
  try {
    const attempt = await board.prepare(id);
    const task = board.get(id);
    board.update(id, (t) => { t.detail = "Worker running"; });
    await execute(task, attempt, (session) => board.update(id, (t) => { t.attempts.at(-1)!.session = session; }), signal, (cb) => { notifyRevision = cb; }, output);
    await assertHead(attempt);
    board.update(id, (t) => { t.detail = "Validating"; });
    const tree = await snapshot(attempt.worktree);
    await checks(task, attempt.worktree, output);
    await assertHead(attempt);
    if (await snapshot(attempt.worktree) !== tree) throw new Error("Checks modified task files; refusing to checkpoint unvalidated changes");
    if (await gitAsync(attempt.worktree, "diff", "--cached", "--name-only")) {
      await gitAsync(attempt.worktree, "commit", "-m", `${id}: ${task.title}`);
    }
    if (await gitAsync(attempt.worktree, "status", "--porcelain")) throw new Error("Worktree changed during commit; inspect before retrying");
    const result = await gitAsync(attempt.worktree, "rev-parse", "HEAD");
    if (await gitAsync(attempt.worktree, "rev-parse", "HEAD^{tree}") !== tree) throw new Error("Commit hooks changed validated content");
    const retries = board.get(id).consecutiveFailures ?? 0;
    board.update(id, (t) => {
      t.status = "completed";
      t.detail = retries ? `Checks passed after ${retries} ${retries === 1 ? "retry" : "retries"}` : "Checks passed";
      t.consecutiveFailures = undefined;
      Object.assign(t.attempts.at(-1)!, { result, checkedTree: tree, checkedAt: new Date().toISOString() });
    });
  } catch (error) {
    const current = board.get(id);
    // A halt/cancel request is not a failure: record the intended state.
    if (current.requestedAction === "pause") {
      board.update(id, (t) => { t.status = "blocked"; t.requestedAction = undefined; t.detail = "Halted"; });
      return;
    }
    if (current.requestedAction === "cancel") {
      board.update(id, (t) => { t.status = "cancelled"; t.requestedAction = undefined; t.progress = undefined; t.detail = "Cancelled"; });
      return;
    }
    // A failure before the attempt was recorded already leaves the task `new`, so
    // the dispatcher re-picks it; only post-provision failures can loop here.
    if (current.attempts.length > previousAttempts) {
      const reason = String(error);
      if (classifyFailure(error) === "transient") {
        const failures = (current.consecutiveFailures ?? 0) + 1;
        if (failures <= TRANSIENT_RETRY_BUDGET) {
          // Re-queue and rethrow: the dispatcher logs the error and re-dispatches
          // the task on its next tick, without a human in the loop.
          board.update(id, (t) => {
            t.status = "new";
            t.mergeBlocked = undefined;
            t.consecutiveFailures = failures;
            t.detail = `Transient failure (retry ${failures}/${TRANSIENT_RETRY_BUDGET}): ${reason}`;
          });
        } else {
          board.update(id, (t) => {
            t.status = "blocked";
            t.consecutiveFailures = failures;
            t.detail = `Transient failure after ${TRANSIENT_RETRY_BUDGET} retries; retry budget exhausted: ${reason}`;
          });
        }
      } else {
        board.update(id, (t) => { t.status = "blocked"; t.consecutiveFailures = undefined; t.detail = reason; });
      }
    }
    throw error;
  } finally { clearInterval(watcher); unlock(); }
}

async function snapshot(cwd: string): Promise<string> {
  await gitAsync(cwd, "add", "--all");
  return gitAsync(cwd, "write-tree");
}

async function assertHead(attempt: Attempt): Promise<void> {
  if (await gitAsync(attempt.worktree, "rev-parse", "HEAD") !== attempt.base || await gitAsync(attempt.worktree, "branch", "--show-current") !== attempt.branch) {
    throw new Error("Worker changed HEAD or branch; refusing automatic checkpoint");
  }
}

/** Resolves an in-progress merge conflict in `cwd` before the pipeline commits. */
export type ConflictResolver = (task: Task, cwd: string, output: OutputSink) => Promise<void>;

/**
 * Default resolver: run the `merge` subagent in the conflicted checkout. It only
 * resolves and stages files; this pipeline verifies and commits, so a failed or
 * partial resolution still aborts safely.
 */
const mergeAgentResolve: ConflictResolver = async (task, cwd, output) => {
  const server = await startServer({ cwd, configContent: JSON.stringify(midasOpencodeConfig(cwd)) });
  const abort = new AbortController();
  const timeout = setTimeout(() => abort.abort(new Error("Merge agent exceeded 20 minutes")), 20 * 60_000);
  let session: Session | undefined;
  try {
    session = await server.client.session.create({ query: { directory: cwd }, body: { title: `Merge ${task.id}` } }) as unknown as Session;
    if (!session?.id) throw new Error("Backend did not create a merge session");
    const conflicts = await gitAsync(cwd, "diff", "--name-only", "--diff-filter=U");
    const result = await server.client.session.prompt({
      path: { id: session.id }, query: { directory: cwd }, signal: abort.signal,
      body: {
        agent: MERGE_AGENT,
        parts: [{ type: "text", text: `An automated merge for task ${task.id} ("${task.title}") hit conflicts in this checkout. Conflicted files:\n${conflicts}\n\nResolve every conflict and stage each resolved file. Do not commit.` }],
      },
    }) as unknown as { info?: { error?: unknown }; parts?: Array<{ type: string; text?: string }> };
    if (result.info?.error) throw new Error(`Merge agent failed: ${JSON.stringify(result.info.error)}`);
    output((result.parts ?? []).filter((part) => part.type === "text").map((part) => part.text ?? "").join("\n") + "\n");
    if (await gitAsync(cwd, "diff", "--name-only", "--diff-filter=U")) throw new Error("Merge agent left unresolved conflicts");
  } finally {
    clearTimeout(timeout);
    abort.abort();
    if (session) await server.client.session.abort({ path: { id: session.id }, query: { directory: cwd }, signal: AbortSignal.timeout(5000) }).catch(() => {});
    if (server.proc.exitCode === null && server.proc.signalCode === null) {
      const exited = once(server.proc, "exit");
      server.close();
      const kill = setTimeout(() => server.proc.kill("SIGKILL"), 5000);
      try { await exited; } finally { clearTimeout(kill); }
    }
  }
};

/** Paths the merge would bring into the checkout: everything that differs between HEAD and the task result. */
async function mergePaths(cwd: string, base: string, mergedCommit: string): Promise<Set<string>> {
  const output = await gitAsync(cwd, "diff", "--name-only", base, mergedCommit);
  return new Set(output.split("\n").map((line) => line.trim()).filter(Boolean));
}

/**
 * Locally dirty paths from `git status --porcelain`: staged, unstaged, renamed
 * (`R old -> new`, both sides) and untracked (`??`) entries. Used to check the
 * merge against the user's in-progress work before Git is asked to touch it.
 */
async function dirtyPaths(cwd: string): Promise<Set<string>> {
  const output = await gitAsync(cwd, "status", "--porcelain");
  const paths = new Set<string>();
  for (const line of output.split("\n")) {
    if (!line.trim()) continue;
    const entry = line.slice(3);
    const arrow = entry.indexOf(" -> ");
    if (arrow >= 0) {
      paths.add(entry.slice(0, arrow));
      paths.add(entry.slice(arrow + 4));
    } else {
      paths.add(entry);
    }
  }
  return paths;
}

/** True while Git is mid-merge (a staged `--no-commit` merge or an unresolved conflict). */
async function mergeInProgress(cwd: string): Promise<boolean> {
  try { return Boolean(await gitAsync(cwd, "rev-parse", "-q", "--verify", "MERGE_HEAD")); }
  catch { return false; }
}

export async function mergeTask(board: TaskBoard, id: string, output: OutputSink = discardOutput, resolveConflict: ConflictResolver = mergeAgentResolve): Promise<void> {
  board.get(id);
  const releaseTask = board.lock(`task-${id}`);
  let integrating = false;
  try {
    const task = board.get(id);
    const attempt = task.attempts.at(-1);
    if (task.status !== "completed" || !attempt?.result) throw new Error("Task has no validated result");
    if (task.merge === "merged") return;
    const mergedCommit = attempt.result;
    await board.withIntegration(async () => {
      const defer = (reason: string): void => {
        board.update(id, (t) => { t.mergeBlocked = reason; t.detail = `Waiting to merge: ${reason}`; });
      };
      // Direct merge: the task's branch lands on the branch the user has checked
      // out right now. Defer (do not fail) when that branch isn't checked out,
      // recording why so the deferral is visible and the dispatcher can retry.
      const current = await gitAsync(board.cwd, "symbolic-ref", "--short", "HEAD");
      if (current !== task.target) return defer(`target ${task.target} is not checked out`);
      const base = await gitAsync(board.cwd, "rev-parse", "HEAD");
      // Git itself only refuses a merge that would overwrite local changes, so
      // only defer on a real path collision: a dirty file the merge would touch.
      // Unrelated work-in-progress must not block the merge.
      const incoming = await mergePaths(board.cwd, base, mergedCommit);
      const dirty = await dirtyPaths(board.cwd);
      const overlap = [...incoming].filter((path) => dirty.has(path));
      if (overlap.length) return defer(`local changes would be overwritten: ${overlap.join(", ")}`);

      board.update(id, (t) => { t.merge = "integrating"; t.mergeBlocked = undefined; t.detail = `Merging into ${task.target}`; });
      integrating = true;
      // Stage the merge without committing so the post-merge checks run before
      // HEAD moves. On failure `merge --abort` restores the pre-merge state and
      // keeps unrelated local changes byte-identical (never `reset --hard`).
      let staged = false;
      try {
        await gitAsync(board.cwd, "merge", "--no-edit", "--no-ff", "--no-commit", mergedCommit);
        staged = await mergeInProgress(board.cwd);
      } catch (error) {
        const conflicted = (await gitAsync(board.cwd, "diff", "--name-only", "--diff-filter=U")).trim();
        if (!conflicted) {
          // Git's own refusal is the backstop: defer, do not mark the merge failed.
          await gitAsync(board.cwd, "merge", "--abort").catch(() => undefined);
          board.update(id, (t) => { t.merge = "not-merged"; });
          integrating = false;
          return defer(`git merge refused: ${error instanceof Error ? error.message : String(error)}`);
        }
        const conflictedPaths = conflicted.split("\n").map((path) => path.trim()).filter(Boolean);
        try {
          await resolveConflict(task, board.cwd, output);
          // Stage only the conflicted paths; unrelated WIP must never be committed.
          await gitAsync(board.cwd, "add", "--", ...conflictedPaths);
          if (await gitAsync(board.cwd, "diff", "--name-only", "--diff-filter=U")) throw new Error("unresolved conflicts remain");
          // `git diff --check` rejects leftover `<<<<<<<` / `>>>>>>>` markers.
          try { await gitAsync(board.cwd, "diff", "--cached", "--check"); }
          catch { throw new Error("merge left conflict markers"); }
        } catch (resolveError) {
          await gitAsync(board.cwd, "merge", "--abort").catch(() => undefined);
          throw new Error(`Merge conflict unresolved: ${resolveError instanceof Error ? resolveError.message : String(resolveError)}`);
        }
        staged = true;
      }
      try {
        const before = await gitAsync(board.cwd, "status", "--porcelain");
        await checks(task, board.cwd, output);
        if (await gitAsync(board.cwd, "status", "--porcelain") !== before) throw new Error("Integration checks changed files");
        if (staged) await gitAsync(board.cwd, "commit", "--no-edit");
        const result = await gitAsync(board.cwd, "rev-parse", "HEAD");
        board.update(id, (t) => { t.merge = "merged"; t.mergedCommit = result; t.mergeBlocked = undefined; t.progress = undefined; t.detail = `Merged into ${task.target}`; });
      } catch (error) {
        // Checks failed or dirtied the tree: roll the staged merge back without
        // disturbing unrelated local changes.
        if (staged) await gitAsync(board.cwd, "merge", "--abort").catch(() => undefined);
        throw error;
      }
    });
  } catch (error) {
    if (integrating && board.get(id).merge !== "merged") board.update(id, (t) => { t.merge = "failed"; t.detail = `${t.detail}\n${String(error)}`; });
    throw error;
  } finally { releaseTask(); }
}

export async function cleanupTask(board: TaskBoard, id: string, force = false): Promise<void> {
  board.get(id);
  const unlock = board.lock(`task-${id}`);
  try {
    const task = board.get(id);
    if (task.status === "running") throw new Error("Cannot clean a running task's worktree");
    if (!force && task.merge !== "merged") throw new Error("Only merged task worktrees can be cleaned automatically");
    for (const attempt of task.attempts) {
      if (attempt.cleaned) continue;
      // A merged attempt must still point at its validated commit; a forced clean
      // (cancelled/blocked/removing a task) removes whatever is in the worktree.
      if (!force && attempt.result && await gitAsync(attempt.worktree, "rev-parse", "HEAD") !== attempt.result) {
        throw new Error("Worktree HEAD changed after validation");
      }
      if (existsSync(attempt.worktree)) await gitAsync(board.cwd, "worktree", "remove", "--force", attempt.worktree);
      board.update(id, (t) => { const stored = t.attempts.find((candidate) => candidate.id === attempt.id); if (stored) stored.cleaned = true; });
    }
  } finally { unlock(); }
}

/** Clean a task's worktrees, then delete it from the board. */
export async function removeTask(board: TaskBoard, id: string): Promise<Task> {
  const task = board.get(id);
  if (task.status === "running") throw new Error(`Task ${id} is running; cancel it first`);
  await cleanupTask(board, id, true);
  return board.remove(id);
}
