import { appendFileSync, readFileSync, statSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { TaskBoard } from "./board.ts";
import { runTask, mergeTask, cleanupTask, removeTask, stdoutOutput } from "./runner.ts";
import { TaskDispatcher } from "./dispatcher.ts";

const DISPATCH_LOG_LIMIT = 2_000;

/** Append one dispatcher event to the per-repo log, capped to the last N lines. */
export function appendDispatchLog(directory: string, message: string): void {
  const path = join(directory, "dispatch.log");
  appendFileSync(path, `${new Date().toISOString()} ${message}\n`);
  try {
    if (statSync(path).size > DISPATCH_LOG_LIMIT * 120) {
      const lines = readFileSync(path, "utf8").split("\n");
      writeFileSync(path, lines.slice(-DISPATCH_LOG_LIMIT).join("\n"));
    }
  } catch { /* best effort */ }
}

/** True once every task is merged or cancelled (nothing left for the daemon). */
export function boardDrained(board: TaskBoard): boolean {
  return board.read().tasks.every((task) => task.merge === "merged" || task.status === "cancelled");
}

export async function taskCli(args: string[]): Promise<void> {
  const cwdIndex = args.indexOf("--cwd");
  let cwd = process.cwd();
  if (cwdIndex >= 0) {
    if (!args[cwdIndex + 1]) throw new Error("--cwd requires a directory");
    cwd = args[cwdIndex + 1]!;
    args.splice(cwdIndex, 2);
  }
  const [command, value, extra] = args;
  if (!command || command === "--help") {
    process.stdout.write(
      "midas task [--cwd DIR] add CONTRACT.json | update ID CONTRACT.json | remove ID | list | run ID | merge ID | cleanup [ID]\n" +
        "midas task [--cwd DIR] block ID | clarify ID [DETAIL]   halt a task / flag it as needing the user\n" +
        "midas task [--cwd DIR] dispatch [--once] [--concurrency N]   run the board autonomously\n",
    );
    return;
  }
  const board = new TaskBoard(cwd);
  if (command === "dispatch") {
    const concurrencyIndex = args.indexOf("--concurrency");
    let concurrency: number | undefined;
    if (concurrencyIndex >= 0) {
      concurrency = Number(args[concurrencyIndex + 1]);
      if (!Number.isInteger(concurrency) || concurrency < 1) throw new Error("--concurrency requires a positive integer");
    }
    const log = (message: string): void => { process.stdout.write(`${message}\n`); appendDispatchLog(board.directory, message); };
    const dispatcher = new TaskDispatcher(board, { concurrency, output: stdoutOutput, onEvent: log });
    try {
      await dispatcher.start();
    } catch (error) {
      // Another session already leads; a duplicate daemon exits quietly.
      if (/already running/.test(String(error))) return;
      throw error;
    }
    process.stdout.write("Dispatcher running. Press Ctrl+C to stop.\n");
    if (args.includes("--once")) {
      await dispatcher.drain();
      dispatcher.stop();
      return;
    }
    // Detached daemons use `--until-drained` to clean themselves up after the
    // board finishes; a foreground `dispatch` keeps running until interrupted.
    let idle: ReturnType<typeof setInterval> | undefined;
    if (args.includes("--until-drained")) {
      let idleTicks = 0;
      idle = setInterval(() => {
        if (dispatcher.running === 0 && boardDrained(board)) idleTicks += 1;
        else idleTicks = 0;
        if (idleTicks >= 60) { clearInterval(idle); dispatcher.stop(); }
      }, 5000);
    }
    await new Promise<void>((resolve) => {
      const stop = (): void => { if (idle) clearInterval(idle); dispatcher.stop(); resolve(); };
      process.once("SIGINT", stop);
      process.once("SIGTERM", stop);
    });
    await dispatcher.drain();
    return;
  }
  const arity = command === "list" || command === "cleanup" ? 1 : command === "update" ? 3 : 2;
  if (!["add", "update", "remove", "list", "run", "merge", "cleanup", "block", "clarify"].includes(command)
    || (command === "cleanup"
      ? args.length < 1 || args.length > 2
      : command === "clarify"
        ? args.length < 2 || args.length > 3
        : args.length !== arity)) {
    throw new Error("Invalid task command; use midas task --help");
  }
  if (command === "add") {
    // Only an active orchestrator may author tasks; otherwise a plain agent
    // could queue work that no dispatcher will ever run.
    if (!board.hasActiveDispatcher()) throw new Error("No active orchestrator: enable /multitask before adding tasks.");
    process.stdout.write(JSON.stringify(board.add(JSON.parse(readFileSync(value!, "utf8"))), null, 2) + "\n");
  }
  if (command === "update") {
    if (!board.hasActiveDispatcher()) throw new Error("No active orchestrator: enable /multitask before updating tasks.");
    process.stdout.write(JSON.stringify(board.edit(value!, JSON.parse(readFileSync(extra!, "utf8"))), null, 2) + "\n");
  }
  if (command === "remove") {
    if (!board.hasActiveDispatcher()) throw new Error("No active orchestrator: enable /multitask before removing tasks.");
    const removed = await removeTask(board, value!);
    process.stdout.write(`Removed ${removed.id}\n`);
  }
  if (command === "block") {
    if (!board.hasActiveDispatcher()) throw new Error("No active orchestrator: enable /multitask before halting tasks.");
    process.stdout.write(JSON.stringify(board.pause(value!), null, 2) + "\n");
  }
  if (command === "clarify") {
    if (!board.hasActiveDispatcher()) throw new Error("No active orchestrator: enable /multitask before clarifying tasks.");
    process.stdout.write(JSON.stringify(board.clarify(value!, extra), null, 2) + "\n");
  }
  if (command === "list") process.stdout.write(JSON.stringify(board.read(), null, 2) + "\n");
  if (command === "run") await runTask(board, value!, undefined, undefined, { output: stdoutOutput });
  if (command === "merge") await mergeTask(board, value!, stdoutOutput);
  if (command === "cleanup") {
    // Explicit board maintenance: force-clean one task, or every settled task.
    const ids = value
      ? [value]
      : board.read().tasks
          .filter((task) => task.status !== "running")
          .map((task) => task.id);
    for (const id of ids) await cleanupTask(board, id, true);
  }
}
