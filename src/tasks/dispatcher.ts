import { existsSync } from "node:fs";
import { join } from "node:path";
import { TaskBoard, scopesOverlap, type Task } from "./board.ts";
import { runTask, mergeTask, cleanupTask, type Worker, type OutputSink } from "./runner.ts";

export interface DispatcherOptions {
  /** Maximum tasks running at once. Omitted means unlimited. */
  concurrency?: number;
  /** Poll interval. Default 5s. */
  intervalMs?: number;
  /** Worker override (tests only). */
  worker?: Worker;
  /** Remove worktrees once merged. Default true. */
  cleanup?: boolean;
  /** Take the cross-process dispatcher lease. Default true. */
  lease?: boolean;
  /**
   * Where worker/check output goes. Omitted in the TUI so subprocess output can
   * never scribble over the renderer; the CLI passes `stdoutOutput`.
   */
  output?: OutputSink;
  onEvent?: (message: string) => void;
  /** Merge runner override (tests only). Defaults to `mergeTask`. */
  merge?: (board: TaskBoard, id: string, output?: OutputSink) => Promise<void>;
  /** Clock override (tests only). Defaults to `Date.now`. */
  now?: () => number;
  /** First backoff after a fresh pending reason. Default 5s. */
  backoffBaseMs?: number;
  /** Ceiling for repeated identical pending reasons. Default 60s. */
  backoffCapMs?: number;
}

/** A completed task whose merge is waiting on a reason, with its retry state. */
interface PendingMerge {
  /** Last reason surfaced to the user; an unchanged reason is never re-emitted. */
  reason: string;
  /** Consecutive mergeTask attempts that deferred for `reason`. */
  deferrals: number;
  /** `now()` before which the next mergeTask attempt is skipped. */
  nextAttemptAt: number;
}

/**
 * Autonomous board runner: picks ready tasks, runs them in isolated worktrees,
 * integrates completed work, and cleans up — one pass per tick. A task runs only
 * when it is `new` and every dependency is merged; failed/blocked tasks are left
 * for a human so the loop cannot retry forever.
 */
export class TaskDispatcher {
  private active = new Map<string, Promise<void>>();
  private controllers = new Map<string, AbortController>();
  private pending = new Map<string, PendingMerge>();
  private timer?: ReturnType<typeof setInterval>;
  private release?: () => void;
  private ticking?: Promise<void>;
  private stopped = false;

  constructor(
    private board: TaskBoard,
    private options: DispatcherOptions = {},
  ) {}

  get running(): number {
    return this.active.size;
  }

  async start(): Promise<void> {
    this.stopped = false;
    const interval = this.options.intervalMs ?? 5000;
    if (this.options.lease !== false) this.release = this.board.lease("dispatch", interval * 3);
    await this.tick();
    this.timer = setInterval(() => void this.tick(), interval);
    this.timer.unref?.();
  }

  stop(): void {
    this.stopped = true;
    if (this.timer) clearInterval(this.timer);
    this.timer = undefined;
    this.release?.();
    this.release = undefined;
  }

  /** Await in-flight runs (used by tests and `--once`). */
  async drain(): Promise<void> {
    // A tick may still be integrating; wait for it before sampling the board.
    await this.ticking;
    await Promise.allSettled([...this.active.values()]);
  }

  async tick(): Promise<void> {
    if (this.stopped) return;
    if (this.ticking) return this.ticking;
    this.ticking = (async () => {
      try {
        await this.reconcile();
        await this.integrate();
        this.requestControls();
        this.dispatchReady();
      } finally {
        this.ticking = undefined;
      }
    })();
    return this.ticking;
  }

  /**
   * A `running` task with no live lock was left behind by a crashed process.
   * Honour a pending halt/cancel, otherwise auto-requeue so a crash does not
   * strand work. The existing worktree is reused by the next attempt.
   */
  private async reconcile(): Promise<void> {
    for (const task of this.board.read().tasks) {
      if (task.status !== "running") continue;
      if (existsSync(join(this.board.directory, `task-${task.id}.lock`))) continue;
      const requested = task.requestedAction;
      if (!requested) {
        // A crashed run is requeued; drop its half-finished worktree so the next
        // attempt starts clean instead of leaking it.
        try { await cleanupTask(this.board, task.id, true); } catch { /* best effort */ }
      }
      this.board.update(task.id, (t) => {
        t.requestedAction = undefined;
        if (requested === "cancel") { t.status = "cancelled"; t.progress = undefined; t.detail = "Cancelled"; }
        else if (requested === "pause") { t.status = "blocked"; t.detail = "Halted"; }
        else { t.status = "new"; t.merge = "not-merged"; t.detail = "Requeued after an interrupted run"; }
      });
      this.options.onEvent?.(`${task.id}: interrupted; ${requested === "cancel" ? "cancelled" : requested === "pause" ? "blocked" : "requeued"}`);
    }
  }

  private async integrate(): Promise<void> {
    const runMerge = this.options.merge ?? mergeTask;
    for (const task of this.board.read().tasks) {
      try {
        const latest = this.board.get(task.id);
        if (latest.status === "completed" && latest.merge === "not-merged") {
          const now = this.now();
          const pending = this.pending.get(task.id);
          if (pending && now < pending.nextAttemptAt) {
            // An unchanged pending reason is backing off; do not retry every tick.
          } else {
            await runMerge(this.board, task.id, this.options.output);
            const after = this.board.get(task.id);
            if (after.merge === "merged") {
              this.pending.delete(task.id);
              this.options.onEvent?.(`${task.id}: merged into ${after.target}`);
            } else if (after.mergeBlocked) {
              this.recordPending(task.id, after.mergeBlocked, now);
            } else {
              // The reason cleared without merging; drop the tracked state.
              this.pending.delete(task.id);
            }
          }
        } else {
          this.pending.delete(task.id);
        }
        if (this.options.cleanup !== false) {
          const after = this.board.get(task.id);
          if (after.merge === "merged" && !after.attempts.at(-1)?.cleaned) {
            await cleanupTask(this.board, task.id);
            this.options.onEvent?.(`${task.id}: worktree cleaned`);
          }
        }
      } catch (error) {
        const message = error instanceof Error ? error.message : String(error);
        this.options.onEvent?.(`${task.id}: autonomous step failed — ${message}`);
      }
    }
  }

  private now(): number {
    return this.options.now?.() ?? Date.now();
  }

  /** Exponential backoff for repeated identical pending reasons, capped. */
  private backoffMs(deferrals: number): number {
    const base = this.options.backoffBaseMs ?? 5000;
    const cap = this.options.backoffCapMs ?? 60000;
    return Math.min(base * 2 ** Math.max(0, deferrals - 1), cap);
  }

  /**
   * Surface a task's pending merge reason exactly once, then back off repeated
   * identical deferrals. A new reason re-emits and resets the backoff.
   */
  private recordPending(id: string, reason: string, now: number): void {
    const previous = this.pending.get(id);
    const deferrals = previous && previous.reason === reason ? previous.deferrals + 1 : 1;
    this.pending.set(id, { reason, deferrals, nextAttemptAt: now + this.backoffMs(deferrals) });
    if (deferrals === 1) this.options.onEvent?.(`${id}: merge pending — ${reason}`);
  }

  private dispatchReady(): void {
    if (this.stopped) return;
    const limit = this.options.concurrency === undefined
      ? Number.POSITIVE_INFINITY
      : Math.max(1, this.options.concurrency);
    const snapshot = this.board.read();
    const merged = (id: string): boolean => snapshot.tasks.find((t) => t.id === id)?.merge === "merged";
    const running = snapshot.tasks.filter((t) => this.active.has(t.id));
    // Scope-aware parallelism: any number of ready tasks run at once, except
    // that a task is deferred while an active (or already-chosen) task's scope
    // overlaps it. Overlapping work would only merge-conflict, so serialising it
    // protects throughput; disjoint work runs freely.
    const chosenScopes: Array<{ id: string; scope: string[] }> = running.map((t) => ({ id: t.id, scope: t.scope ?? [] }));
    for (const task of snapshot.tasks) {
      if (this.active.size >= limit) break;
      if (task.status !== "new" || this.active.has(task.id)) continue;
      if (!this.ready(task, merged)) continue;
      const blocker = chosenScopes.find((other) => scopesOverlap(task.scope ?? [], other.scope));
      if (blocker) continue;
      chosenScopes.push({ id: task.id, scope: task.scope ?? [] });
      this.options.onEvent?.(`${task.id}: started`);
      const controller = new AbortController();
      this.controllers.set(task.id, controller);
      const promise = runTask(this.board, task.id, this.options.worker, controller.signal, { output: this.options.output })
        .then(() => this.options.onEvent?.(`${task.id}: completed`))
        .catch((error) => this.options.onEvent?.(`${task.id}: ${error instanceof Error ? error.message : String(error)}`))
        .finally(() => {
          this.active.delete(task.id);
          this.controllers.delete(task.id);
          void this.tick();
        });
      this.active.set(task.id, promise);
    }
  }

  /** Abort a running worker when its task was asked to pause or cancel. */
  private requestControls(): void {
    for (const [id, controller] of this.controllers) {
      const task = this.board.get(id);
      if (task.requestedAction && !controller.signal.aborted) {
        controller.abort(new Error(`Task ${id} ${task.requestedAction} requested`));
        this.options.onEvent?.(`${id}: ${task.requestedAction} requested`);
      }
    }
  }

  private ready(task: Task, merged: (id: string) => boolean): boolean {
    return (task.dependencies ?? []).every(merged);
  }
}
