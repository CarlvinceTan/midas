import { execFile, execFileSync } from "node:child_process";
import { mkdirSync, readFileSync, writeFileSync, renameSync, rmSync } from "node:fs";
import { join, resolve } from "node:path";
import { randomUUID } from "node:crypto";
import { hostname } from "node:os";

export interface Contract {
  title: string;
  group?: string;
  instructions: string;
  checks: string[];
  dependencies?: string[];
  /** Repo-relative paths/globs this task may change (parallel-safety hint). */
  scope?: string[];
}
export interface Attempt {
  id: string;
  worktree: string;
  branch: string;
  base: string;
  session?: string;
  result?: string;
  checkedTree?: string;
  checkedAt?: string;
  cleaned?: boolean;
}
export interface Task extends Contract {
  id: string;
  status: "new" | "running" | "completed" | "blocked" | "clarify" | "cancelled";
  merge: "not-merged" | "integrating" | "merged" | "failed";
  target: string;
  attempts: Attempt[];
  /** Bumped on every contract edit so observers (e.g. running workers) notice. */
  revision: number;
  /** Set on a running task to ask the dispatcher to pause/cancel its worker. */
  requestedAction?: "pause" | "cancel";
  detail?: string;
  /** Model-generated stage phrase (the lightweight status agent). */
  progress?: string;
  /** Contract revision the model title was generated from; keeps titles stable. */
  titledRevision?: number;
  mergedCommit?: string;
  /** Machine-readable reason the last merge attempt was deferred (e.g. wrong branch, overlapping edits). */
  mergeBlocked?: string;
  /** Consecutive transient run failures; reset on success or when a human resumes. */
  consecutiveFailures?: number;
}
export interface Board { version: 1; tasks: Task[] }

/** Marker for a scope that names every path (missing/empty scope). */
const ANY_SCOPE = "*";

/**
 * A trailing `/` declares a subtree rooted at that directory, so it is
 * equivalent to `<dir>/*` where `*` matches across separators. Every other
 * scope is already a single-`*` glob or an exact path.
 */
function scopeToPattern(scope: string): string {
  return scope.endsWith("/") ? `${scope}*` : scope;
}

/**
 * True when the two supported globs can match at least one shared path. Each
 * glob is an alternating sequence of literal text and `*` (which matches any
 * run of characters, including `/`). The two globs are run as NFAs on a common
 * input and the product is searched for a reachable accepting pair, so the
 * answer is exact for this syntax: a `*` consumes whatever the other side needs,
 * equal literals advance together, and unequal literals dead-end. This is
 * sound (it never reports disjointness for a path both scopes match) while
 * still proving disjoint literal prefixes disjoint instead of over-serialising.
 */
function patternsIntersect(p: string, q: string): boolean {
  const width = q.length + 1;
  const seen = new Set<number>([0]);
  const queue: Array<[number, number]> = [[0, 0]];
  while (queue.length) {
    const [i, j] = queue.pop()!;
    if (i === p.length && j === q.length) return true;
    const pi = i < p.length ? p[i] : undefined;
    const qj = j < q.length ? q[j] : undefined;
    const next: Array<[number, number]> = [];
    if (pi === "*") next.push([i + 1, j]); // Consume no input, move past the glob.
    if (qj === "*") next.push([i, j + 1]);
    if (pi !== undefined && qj !== undefined) {
      if (pi === "*" && qj !== "*") next.push([i, j + 1]); // The glob swallows the other side's char.
      if (qj === "*" && pi !== "*") next.push([i + 1, j]);
      if (pi !== "*" && qj !== "*" && pi === qj) next.push([i + 1, j + 1]);
    }
    for (const [ni, nj] of next) {
      const key = ni * width + nj;
      if (!seen.has(key)) { seen.add(key); queue.push([ni, nj]); }
    }
  }
  return false;
}

/**
 * True when two declared scopes could touch the same files. Exact paths match;
 * a trailing `/` covers descendants; `*` globs intersect whether or not either
 * pattern literally contains the other; a missing/empty scope is treated as
 * `["*"]` (overlaps everything) so correctness wins until a task declares the
 * files it may change. Symmetric in its arguments.
 */
export function scopesOverlap(a: string[], b: string[]): boolean {
  const norm = (scope: string[]): string[] => scope.length ? scope : [ANY_SCOPE];
  for (const x of norm(a)) {
    for (const y of norm(b)) {
      if (x === ANY_SCOPE || y === ANY_SCOPE || x === y) return true;
      if (patternsIntersect(scopeToPattern(x), scopeToPattern(y))) return true;
    }
  }
  return false;
}

function validScope(value: unknown): value is string[] {
  return Array.isArray(value) && value.every((v) => typeof v === "string" && v.trim() && !v.startsWith("/") && !v.includes(".."));
}

/**
 * True when `pid` names a live process. `EPERM` means the process exists but is
 * not ours, which still counts as alive; any other signal error is not.
 */
function processAlive(pid: number): boolean {
  if (!Number.isInteger(pid) || pid <= 0) return false;
  try { process.kill(pid, 0); return true; }
  catch (error) { return (error as NodeJS.ErrnoException).code === "EPERM"; }
}

/**
 * Blocking git. Reserved for one-time setup (resolving the board directory) and
 * tests; runtime worktree/merge/validation operations must use `gitAsync` so a
 * slow `git` subprocess can never stall the TUI event loop.
 */
export function git(cwd: string, ...args: string[]): string {
  return execFileSync("git", ["-C", cwd, ...args], { encoding: "utf8", stdio: ["ignore", "pipe", "pipe"] }).trim();
}

/** Non-blocking git; see `git`. */
export function gitAsync(cwd: string, ...args: string[]): Promise<string> {
  return new Promise((resolve, reject) => {
    execFile("git", ["-C", cwd, ...args], { encoding: "utf8", maxBuffer: 8 * 1024 * 1024 }, (error, stdout) => {
      if (error) reject(error);
      else resolve(stdout.trim());
    });
  });
}

/** Local-filesystem board shared by linked worktrees. Corrupt data is never reset. */
export class TaskBoard {
  readonly directory: string;
  constructor(readonly cwd: string) {
    this.directory = join(git(cwd, "rev-parse", "--path-format=absolute", "--git-common-dir"), "midas");
  }
  read(): Board {
    let raw: string;
    try { raw = readFileSync(join(this.directory, "board.json"), "utf8"); }
    catch (error) { if ((error as NodeJS.ErrnoException).code === "ENOENT") return { version: 1, tasks: [] }; throw error; }
    const board = JSON.parse(raw) as Board;
    if (board.version !== 1 || !Array.isArray(board.tasks)) throw new Error("Unsupported or corrupt Midas task board");
    return board;
  }
  lock(name: string): () => void {
    if (!/^[a-zA-Z0-9-]+$/.test(name)) throw new Error("Invalid lock name");
    mkdirSync(this.directory, { recursive: true });
    const path = join(this.directory, `${name}.lock`);
    const locked = (): never => {
      throw new Error(`Operation locked: ${path}. If interrupted, stop its worker and check processes before manually removing this lock.`);
    };
    try { mkdirSync(path); }
    catch (error) {
      if ((error as NodeJS.ErrnoException).code !== "EEXIST") throw error;
      // A dead owner on this host can never release the lock, so take it over.
      // A lock held by a live process, or one we cannot prove dead (foreign
      // host, missing/malformed owner), stays fail-closed.
      let owner: { pid?: unknown; host?: unknown } | undefined;
      try { owner = JSON.parse(readFileSync(join(path, "owner.json"), "utf8")) as typeof owner; }
      catch { owner = undefined; }
      if (!owner || owner.host !== hostname() || typeof owner.pid !== "number" || processAlive(owner.pid)) locked();
      rmSync(path, { recursive: true, force: true });
      mkdirSync(path);
    }
    writeFileSync(join(path, "owner.json"), JSON.stringify({ pid: process.pid, host: hostname(), started: new Date().toISOString() }));
    return () => rmSync(path, { recursive: true });
  }
  /**
   * True only while a dispatcher holds a live lease. Authoring new board tasks
   * is gated on this so a plain agent cannot queue work no orchestrator will
   * pick up. A missing, unreadable, malformed, or stale heartbeat is inactive.
   */
  hasActiveDispatcher(ttlMs = 15_000): boolean {
    try {
      const owner = JSON.parse(readFileSync(join(this.directory, "dispatch.lock", "owner.json"), "utf8")) as { heartbeat?: unknown };
      const heartbeat = owner?.heartbeat;
      return typeof heartbeat === "number" && Number.isFinite(heartbeat) && Date.now() - heartbeat < ttlMs;
    } catch {
      return false;
    }
  }

  /**
   * Dispatcher lease. Unlike the fail-closed operation locks, a stale lease is
   * taken over once its heartbeat expires, so a crashed dispatcher recovers.
   * Task-level locks stay fail-closed; this only elects a leader.
   */
  lease(name: string, ttlMs: number): () => void {
    if (!/^[a-zA-Z0-9-]+$/.test(name)) throw new Error("Invalid lease name");
    mkdirSync(this.directory, { recursive: true });
    const path = join(this.directory, `${name}.lock`);
    try {
      mkdirSync(path);
    } catch (error) {
      if ((error as NodeJS.ErrnoException).code !== "EEXIST") throw error;
      let heartbeat = 0;
      try { heartbeat = Number(JSON.parse(readFileSync(join(path, "owner.json"), "utf8")).heartbeat) || 0; } catch { /* treat as stale */ }
      if (Date.now() - heartbeat < ttlMs) throw new Error(`Another dispatcher is already running (${path})`);
      rmSync(path, { recursive: true, force: true });
      mkdirSync(path);
    }
    const write = (): void => writeFileSync(join(path, "owner.json"), JSON.stringify({ pid: process.pid, host: hostname(), heartbeat: Date.now() }));
    write();
    const timer = setInterval(write, Math.max(500, Math.floor(ttlMs / 3)));
    timer.unref?.();
    return () => { clearInterval(timer); rmSync(path, { recursive: true, force: true }); };
  }

  /**
   * Serializes worktree provisioning and integration inside this process, then
   * takes the cross-process integration file lock. The file lock stays
   * fail-closed (the dispatcher lease elects one leader); the in-process queue
   * lets concurrent task runs wait for each other instead of failing.
   */
  private integrationQueue: Promise<void> = Promise.resolve();
  async withIntegration<T>(fn: () => Promise<T>): Promise<T> {
    const previous = this.integrationQueue;
    let release!: () => void;
    this.integrationQueue = new Promise<void>((resolve) => { release = resolve; });
    await previous;
    try {
      const unlock = this.lock("integration");
      try {
        return await fn();
      } finally {
        unlock();
      }
    } finally {
      release();
    }
  }

  mutate<T>(fn: (board: Board) => T): T {
    const unlock = this.lock("board");
    let temp: string | undefined;
    try {
      const board = this.read();
      const result = fn(board);
      temp = join(this.directory, `${randomUUID()}.tmp`);
      writeFileSync(temp, JSON.stringify(board, null, 2) + "\n", { mode: 0o600 });
      renameSync(temp, join(this.directory, "board.json"));
      return result;
    } finally { if (temp) rmSync(temp, { force: true }); unlock(); }
  }
  get(id: string): Task {
    const task = this.read().tasks.find((task) => task.id === id);
    if (!task) throw new Error(`Unknown task: ${id}`);
    return task;
  }
  /**
   * Branch checked out in this worktree. Tasks branch from and merge back into
   * it, so the board tracks the user's current branch instead of a staging ref.
   */
  currentBranch(): string {
    try {
      return git(this.cwd, "symbolic-ref", "--short", "HEAD");
    } catch {
      throw new Error("Multitask requires a checked-out branch (detached HEAD is not supported)");
    }
  }
  update(id: string, fn: (task: Task) => void): void {
    this.mutate((board) => {
      const task = board.tasks.find((task) => task.id === id);
      if (!task) throw new Error(`Unknown task: ${id}`);
      fn(task);
    });
  }
  add(input: unknown): Task {
    const c = input as Contract;
    if (!c || typeof c.title !== "string" || !c.title.trim() || typeof c.instructions !== "string" || !c.instructions.trim()
      || !Array.isArray(c.checks) || !c.checks.length || c.checks.some((v) => typeof v !== "string" || !v.trim())
      || (c.group !== undefined && typeof c.group !== "string")
      || (c.scope !== undefined && !validScope(c.scope))
      || (c.dependencies !== undefined && (!Array.isArray(c.dependencies) || c.dependencies.some((v) => typeof v !== "string")))) {
      throw new Error("Contract requires title, instructions, nonempty checks[], and optional group/dependencies[]/scope[]");
    }
    const target = this.currentBranch();
    return this.mutate((board) => {
      for (const id of c.dependencies ?? []) if (!board.tasks.some((t) => t.id === id)) throw new Error(`Unknown dependency: ${id}`);
      // Ids are monotonic: removing a task must never let a later add reuse its id.
      const next = board.tasks.reduce((max, task) => Math.max(max, Number(task.id.slice(1)) || 0), 0) + 1;
      const task: Task = { title: c.title, instructions: c.instructions, checks: [...c.checks], group: c.group,
        dependencies: [...(c.dependencies ?? [])], scope: c.scope ? [...c.scope] : undefined, id: `T${next}`,
        status: "new", merge: "not-merged", target, attempts: [], revision: 1 };
      board.tasks.push(task);
      return task;
    });
  }
  /**
   * Edit an existing task's contract in place and bump its `revision`, so a
   * running worker can re-read the board and adapt. Refuses merged/cancelled
   * tasks (add a follow-up instead); blocked or completed-unmerged tasks are
   * re-queued so the edited contract actually runs.
   */
  edit(id: string, patch: Partial<Pick<Contract, "title" | "group" | "instructions" | "checks" | "dependencies" | "scope">>): Task {
    if (!patch || typeof patch !== "object") throw new Error("Edit requires a patch object");
    if (patch.title !== undefined && (typeof patch.title !== "string" || !patch.title.trim())) throw new Error("title must be a nonempty string");
    if (patch.instructions !== undefined && (typeof patch.instructions !== "string" || !patch.instructions.trim())) throw new Error("instructions must be a nonempty string");
    if (patch.checks !== undefined && (!Array.isArray(patch.checks) || !patch.checks.length || patch.checks.some((v) => typeof v !== "string" || !v.trim()))) throw new Error("checks must be a nonempty string[]");
    if (patch.group !== undefined && typeof patch.group !== "string") throw new Error("group must be a string");
    if (patch.scope !== undefined && !validScope(patch.scope)) throw new Error("scope must be an array of repo-relative paths");
    if (patch.dependencies !== undefined && (!Array.isArray(patch.dependencies) || patch.dependencies.some((v) => typeof v !== "string"))) throw new Error("dependencies must be a string[]");
    return this.mutate((board) => {
      const task = board.tasks.find((task) => task.id === id);
      if (!task) throw new Error(`Unknown task: ${id}`);
      if (task.merge === "merged") throw new Error(`Task ${id} is merged and cannot be edited; add a follow-up task instead`);
      if (task.status === "cancelled") throw new Error(`Task ${id} is cancelled and cannot be edited; add a new task instead`);
      for (const dep of patch.dependencies ?? []) {
        if (dep === id) throw new Error("A task cannot depend on itself");
        if (!board.tasks.some((t) => t.id === dep)) throw new Error(`Unknown dependency: ${dep}`);
      }
      if (patch.title !== undefined) task.title = patch.title;
      if (patch.group !== undefined) task.group = patch.group;
      if (patch.instructions !== undefined) task.instructions = patch.instructions;
      if (patch.checks !== undefined) task.checks = [...patch.checks];
      if (patch.scope !== undefined) task.scope = [...patch.scope];
      if (patch.dependencies !== undefined) task.dependencies = [...patch.dependencies];
      task.revision = (task.revision ?? 0) + 1;
      task.detail = "updated";
      // A finished-but-unmerged, blocked, or clarify task must re-run the edited contract.
      if (task.status === "blocked" || task.status === "clarify" || task.status === "completed") {
        task.status = "new";
        task.merge = "not-merged";
      }
      return task;
    });
  }
  /**
   * Halt a task: a running worker is asked to stop, an idle task blocks
   * immediately. Blocked is the halt state; there is no separate `paused`.
   */
  pause(id: string): Task {
    return this.mutate((board) => {
      const task = board.tasks.find((task) => task.id === id);
      if (!task) throw new Error(`Unknown task: ${id}`);
      if (task.status === "cancelled" || task.status === "completed" || task.merge === "merged") throw new Error(`Task ${id} cannot be halted`);
      if (task.status === "running") { task.requestedAction = "pause"; task.detail = "Halt requested"; }
      else { task.status = "blocked"; task.detail = "Halted"; }
      return task;
    });
  }
  /**
   * Flag a task as needing a user decision (`?`). The dispatcher never runs a
   * clarify task; the orchestrator asks the user, then edits/resumes it.
   */
  clarify(id: string, detail = "Needs clarification"): Task {
    return this.mutate((board) => {
      const task = board.tasks.find((task) => task.id === id);
      if (!task) throw new Error(`Unknown task: ${id}`);
      if (task.merge === "merged" || task.status === "cancelled") throw new Error(`Task ${id} cannot be clarified`);
      if (task.status === "running") throw new Error(`Task ${id} is running; halt it before marking it for clarification`);
      task.status = "clarify";
      task.detail = detail;
      return task;
    });
  }
  /** Re-queue a blocked or clarify task so the (possibly edited) contract runs. */
  resume(id: string): Task {
    return this.mutate((board) => {
      const task = board.tasks.find((task) => task.id === id);
      if (!task) throw new Error(`Unknown task: ${id}`);
      if (task.status !== "blocked" && task.status !== "clarify") throw new Error(`Task ${id} is not blocked or awaiting clarification`);
      task.requestedAction = undefined;
      task.status = "new";
      task.merge = "not-merged";
      task.consecutiveFailures = undefined;
      task.detail = "Queued to resume";
      return task;
    });
  }
  /** Ask a running task to cancel; an idle pending task cancels immediately. */
  cancel(id: string): Task {
    return this.mutate((board) => {
      const task = board.tasks.find((task) => task.id === id);
      if (!task) throw new Error(`Unknown task: ${id}`);
      if (task.merge === "merged") throw new Error(`Task ${id} is already merged`);
      if (task.status === "running") { task.requestedAction = "cancel"; task.detail = "Cancel requested"; }
      else { task.status = "cancelled"; task.detail = "Cancelled"; task.progress = undefined; }
      return task;
    });
  }
  /**
   * Delete a task from the board. Refuses while it is running or while another
   * task still depends on it, so the dependency graph never dangles. Callers
   * should clean the task's worktrees first (see `removeTask`).
   */
  remove(id: string): Task {
    return this.mutate((board) => {
      const index = board.tasks.findIndex((task) => task.id === id);
      if (index < 0) throw new Error(`Unknown task: ${id}`);
      const task = board.tasks[index]!;
      if (task.status === "running") throw new Error(`Task ${id} is running; cancel it first`);
      const dependent = board.tasks.find((other) => other.id !== id && (other.dependencies ?? []).includes(id));
      if (dependent) throw new Error(`Task ${dependent.id} depends on ${id}; remove or edit it first`);
      board.tasks.splice(index, 1);
      return task;
    });
  }
  async prepare(id: string): Promise<Attempt> {
    const task = this.get(id);
    if (task.status !== "new" && task.status !== "blocked") throw new Error(`Task is ${task.status}; cannot run`);
    for (const dep of task.dependencies ?? []) {
      const prerequisite = this.get(dep);
      if (prerequisite.merge !== "merged" || prerequisite.target !== task.target) throw new Error(`Waiting on ${dep} to merge`);
    }
    return this.withIntegration(async () => {
      let base: string;
      try { base = await gitAsync(this.cwd, "rev-parse", "--verify", `refs/heads/${task.target}`); }
      catch {
        await gitAsync(this.cwd, "branch", task.target, "HEAD");
        base = await gitAsync(this.cwd, "rev-parse", `refs/heads/${task.target}`);
      }
      for (const dep of task.dependencies ?? []) await gitAsync(this.cwd, "merge-base", "--is-ancestor", this.get(dep).mergedCommit!, base);
      const attemptId = `${id}-${randomUUID().slice(0, 8)}`;
      const attempt: Attempt = { id: attemptId, base, branch: `midas/task-${attemptId}`, worktree: resolve(this.directory, "worktrees", attemptId) };
      this.update(id, (t) => { t.status = "running"; t.detail = "Provisioning worktree"; t.attempts.push(attempt); });
      mkdirSync(join(this.directory, "worktrees"), { recursive: true });
      await gitAsync(this.cwd, "worktree", "add", "-b", attempt.branch, attempt.worktree, base);
      return attempt;
    });
  }
}
