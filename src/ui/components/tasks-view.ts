import { matchesKey, truncateToWidth, visibleWidth, type Component } from "@earendil-works/pi-tui";
import { theme, type ThemeColor } from "../../theme/theme.ts";
import type { Task } from "../../tasks/board.ts";

const frames = ["⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"];
/**
 * Pure glyph rendering, derived from task status plus merge progress:
 * `○` ready (empty blue), spinner while a worker runs, `●` completed but not yet
 * merged (filled blue), `⇣` merging (orange), `✓` merged (green), `?` needing
 * clarification (yellow), `!` blocked/interrupted (red), `✗` cancelled (red).
 */
export function taskIcon(task: Task, frame: number): string {
  if (task.merge === "merged") return "✓";
  if (task.status === "running") return frames[frame % frames.length]!;
  if (task.status === "completed") return task.merge === "integrating" ? "⇣" : "●";
  if (task.status === "new") return "○";
  if (task.status === "clarify") return "?";
  if (task.status === "cancelled") return "✗";
  return "!"; // blocked, and any future failure state
}
/** Theme color for the status glyph; `undefined` leaves the default foreground. */
export function taskIconColor(task: Task): ThemeColor | undefined {
  if (task.merge === "merged") return "success";
  switch (task.status) {
    case "running": return "accent";
    case "completed": return task.merge === "integrating" ? "warning" : "accent";
    case "new": return "accent";
    case "clarify": return "warning";
    default: return "error"; // blocked, cancelled
  }
}
const safe = (text: string): string => text.replace(/[\x00-\x1f\x7f-\x9f]/g, " ");
/**
 * Short, scannable form of a deferred-merge reason. The board stores the full
 * machine-readable cause (which can list every overlapping path), but the row
 * only has room for the gist; the actions menu's details view keeps the rest.
 */
export function shortMergeBlock(reason: string): string {
  const text = safe(reason).trim();
  const target = /^target (\S+) is not checked out$/i.exec(text);
  if (target) return `target ${target[1]} not checked out`;
  if (/local changes|would be overwritten/i.test(text)) return "local changes overlap";
  return text;
}
/** Number of task rows in the scroll window. */
const windowRows = 7;

/** Board control hooks the actions menu invokes for the selected task. */
export interface TaskControls {
  pause(id: string): void;
  resume(id: string): void;
  cancel(id: string): void;
  remove(id: string): void;
}

interface MenuAction {
  label: string;
  run: () => void;
}

/** Read-only board browser: never changes the foreground session or directory. */
export class TasksView implements Component {
  tasks: Task[] = [];
  error?: string;
  private selected = 0;
  private details = false;
  private menu?: { taskId: string; actions: MenuAction[]; index: number };
  /**
   * `multitask` is true only in the orchestrator's mode. It selects the
   * empty-state copy and enables board control. In the default `main` agent the
   * panel is a read-only browser of the same board.
   */
  constructor(
    private onCancel: () => void,
    private multitask = false,
    private controls: TaskControls = { pause: () => {}, resume: () => {}, cancel: () => {}, remove: () => {} },
  ) {}
  invalidate(): void {}
  private sorted(): Task[] {
    return [...this.tasks].sort((a, b) => a.id.localeCompare(b.id, undefined, { numeric: true }));
  }
  handleInput(data: string): void {
    if (this.menu) {
      if (matchesKey(data, "escape") || matchesKey(data, "ctrl+c")) { this.menu = undefined; return; }
      if (matchesKey(data, "up")) { this.menu.index = Math.max(0, this.menu.index - 1); return; }
      if (matchesKey(data, "down")) { this.menu.index = Math.min(this.menu.actions.length - 1, this.menu.index + 1); return; }
      if (matchesKey(data, "enter")) {
        const action = this.menu.actions[this.menu.index];
        this.menu = undefined;
        action?.run();
        return;
      }
      return;
    }
    if (matchesKey(data, "escape") || matchesKey(data, "ctrl+c")) return this.onCancel();
    if (matchesKey(data, "up")) this.selected = Math.max(0, this.selected - 1);
    if (matchesKey(data, "down")) this.selected = Math.min(this.tasks.length - 1, this.selected + 1);
    if (matchesKey(data, "enter")) this.openMenu();
  }
  /** Actions valid for the selected task; Enter invokes, Esc closes the menu. */
  private openMenu(): void {
    const sorted = this.sorted();
    const task = sorted[Math.max(0, Math.min(this.selected, sorted.length - 1))];
    if (!task) return;
    const actions: MenuAction[] = [];
    // Board control belongs to the multitask workflow; outside it (the default
    // `main` agent) the panel is a read-only browser of the same board.
    if (this.multitask) {
      if (task.status === "running" || task.status === "new") actions.push({ label: "Halt", run: () => this.controls.pause(task.id) });
      if (task.status === "blocked" || task.status === "clarify") actions.push({ label: "Resume", run: () => this.controls.resume(task.id) });
      if (task.merge !== "merged" && task.status !== "completed" && task.status !== "cancelled") actions.push({ label: "Cancel", run: () => this.controls.cancel(task.id) });
      if (task.status !== "running") actions.push({ label: "Remove", run: () => this.controls.remove(task.id) });
    }
    actions.push({ label: "Show details", run: () => { this.details = !this.details; } });
    actions.push({ label: "Close", run: () => {} });
    this.menu = { taskId: task.id, actions, index: 0 };
  }
  render(width: number): string[] {
    const t = theme();
    const sorted = this.sorted();
    this.selected = Math.max(0, Math.min(this.selected, sorted.length - 1));
    const lines: string[] = [];
    if (this.error) lines.push(t.fg("error", safe(this.error)));
    else if (!sorted.length) {
      lines.push(
        this.multitask
          ? "No tasks yet. Tasks are created automatically as requests are sent."
          : "No tasks yet.",
      );
    } else {
      // One id column for the whole board, computed from the full task set so it
      // does not shift as the scroll window or the selection changes.
      const idWidth = Math.max(3, ...this.tasks.map((task) => safe(task.id).length));
      const start = this.scrollStart(sorted.length, this.selected);
      const statusStyle = (text: string): string => t.fg("muted", text);
      const titleStyle = (text: string): string => t.fg("text", text);
      sorted.slice(start, start + windowRows).forEach((task, i) => {
        const icon = taskIcon(task, Math.floor(Date.now() / 100));
        const color = taskIconColor(task);
        const prefix = `${start + i === this.selected ? "→" : " "} ${color ? t.fg(color, icon) : icon} ${safe(task.id).padEnd(idWidth)}  `;
        const statusText = this.statusText(task, statusStyle);
        const title = titleStyle(safe(task.title));
        const prefixWidth = visibleWidth(prefix);
        const statusWidth = visibleWidth(statusText);
        const gap = "  ";
        const room = width - prefixWidth - visibleWidth(gap) - statusWidth;
        if (room >= 0) {
          // Right-align the state: title fills the left, state sits at the edge.
          const fitted = truncateToWidth(title, room, titleStyle("…"));
          const filler = " ".repeat(Math.max(0, width - prefixWidth - visibleWidth(fitted) - visibleWidth(gap) - statusWidth));
          lines.push(prefix + fitted + filler + gap + statusText);
        } else {
          // Not even the state fits: give up the title, then clip the state.
          const statusRoom = width - prefixWidth - visibleWidth(gap);
          const clipped = statusRoom > 0 ? truncateToWidth(statusText, statusRoom, statusStyle("…")) : "";
          lines.push(prefix + gap + clipped);
        }
      });
      if (sorted.length > windowRows) lines.push(t.fg("dim", `${this.selected + 1}/${sorted.length}`));
    }
    if (this.menu) {
      lines.push(t.fg("accent", "Actions"));
      for (const [index, action] of this.menu.actions.entries()) {
        const cursor = index === this.menu.index ? "→" : " ";
        lines.push(`${cursor} ${index === this.menu.index ? t.fg("accent", action.label) : action.label}`);
      }
    }
    const selected = sorted[this.selected];
    const details: string[] = [];
    if (selected && this.details) {
      const attempt = selected.attempts.at(-1);
      details.push("", safe(selected.instructions), safe(selected.detail ?? ""));
      if (selected.progress) details.push(`Status: ${safe(selected.progress)}`);
      if (selected.mergeBlocked) details.push(`Merge note: ${safe(selected.mergeBlocked)}`);
      details.push(
        `Worktree: ${safe(attempt?.worktree ?? "not allocated")}${attempt?.cleaned ? " (removed)" : ""}`,
        `Session: ${safe(attempt?.session ?? "none")} · attempts: ${selected.attempts.length}`,
        `Checks: ${safe(selected.checks.join("; "))}`,
        `Result: ${attempt?.result ?? "none"} · merge: ${selected.mergedCommit ?? "none"}`);
    }
    lines.push(...details);
    return lines.map((line) => truncateToWidth(line, Math.max(1, width), "…"));
  }

  /**
   * Right-hand text. The lightweight status agent owns it: its stage phrase is
   * shown for every state. Until it has run, fall back to a terse deterministic
   * label; the detailed reason always lives in the details view.
   */
  private statusText(task: Task, statusStyle: (text: string) => string): string {
    const t = theme();
    if (task.progress) return t.fg("muted", safe(task.progress));
    if (task.merge === "merged") return t.fg("success", `merged → ${task.target}`);
    if (task.merge === "failed") return `${t.fg("error", "!")}${statusStyle(" merge failed")}`;
    if (task.merge === "integrating") return t.fg("warning", "merging…");
    if (task.status === "completed") return statusStyle(task.mergeBlocked ? "merge pending" : "not merged");
    if (task.status === "clarify") return t.fg("warning", "needs clarification");
    if (task.status === "blocked") return t.fg("error", "blocked");
    if (task.status === "cancelled") return statusStyle("cancelled");
    const waiting = (task.dependencies ?? []).filter((id) => this.tasks.find((v) => v.id === id)?.merge !== "merged");
    if (waiting.length) return statusStyle(`waiting on ${waiting.join(", ")}`);
    if (task.status === "new") return statusStyle("ready");
    return statusStyle(task.attempts.at(-1)?.branch ?? "provisioning");
  }

  /**
   * First task index of the scroll window. Centred on the selection, then
   * clamped so the window never runs past the last task.
   */
  private scrollStart(total: number, selected: number): number {
    const maxStart = Math.max(0, total - windowRows);
    return Math.min(Math.max(0, selected - 3), maxStart);
  }
}
