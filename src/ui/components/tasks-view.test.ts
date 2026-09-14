import assert from "node:assert/strict";
import { test } from "node:test";
import { initTheme as initPiTheme } from "@earendil-works/pi-coding-agent";
import { initTheme, theme } from "../../theme/theme.ts";
import { stripAnsi } from "../../lib/ansi.ts";
import type { Task } from "../../tasks/board.ts";
import { TasksView, taskIcon, taskIconColor } from "./tasks-view.ts";

initPiTheme(undefined, false);
initTheme(undefined);

function task(overrides: Partial<Task> = {}): Task {
  return {
    id: "T1",
    title: "Implement feature",
    instructions: "Add result.txt",
    checks: [],
    status: "new",
    merge: "not-merged",
    target: "main",
    revision: 1,
    attempts: [],
    ...overrides,
  };
}

function renderRow(overrides: Partial<Task>, width = 160): string {
  const view = new TasksView(() => {});
  view.tasks = [task(overrides)];
  return view.render(width).join("\n");
}

/** Last SGR code before `index` that sets a colour/attribute, ignoring resets. */
function activeColor(text: string, index: number): string {
  const codes = [...text.slice(0, index).matchAll(/\x1b\[[0-9;]*m/g)].map((match) => match[0]);
  return codes.reverse().find((code) => code !== "\x1b[0m" && code !== "\x1b[39m" && code !== "\x1b[22m" && code !== "\x1b[49m") ?? "";
}

/** A board of `count` tasks with one worktree branch each. */
function manyTasks(count = 12): Task[] {
  return Array.from({ length: count }, (_, i) => task({
    id: `T${i + 1}`,
    title: `Task ${i + 1}`,
    attempts: [{ id: `a${i}`, worktree: `/tmp/worktree-${i}`, branch: `midas/t${i + 1}`, base: "main" }],
  }));
}

/** Move the pointer to `index` through the public input path. */
function selectRow(view: TasksView, index: number): void {
  for (let i = 0; i < index; i += 1) view.handleInput("\x1b[B");
}

interface ContentRow {
  arrow: boolean;
  id: string;
}

/** Content rows (task rows, not the counter) in render order. */
function contentRows(lines: string[]): ContentRow[] {
  return lines.map(stripAnsi).flatMap((line) => {
    const match = /^([→ ]) \S (T\d+)\b/.exec(line);
    return match ? [{ arrow: match[1] === "→", id: match[2]! }] : [];
  });
}

function renderSelected(tasks: Task[], selected: number): string[] {
  const view = new TasksView(() => {});
  view.tasks = tasks;
  selectRow(view, selected);
  return view.render(160);
}

/** Blank rows at the bottom of a render, which the panel must never add. */
function trailingBlanks(lines: string[]): number {
  let count = 0;
  for (let i = lines.length - 1; i >= 0 && stripAnsi(lines[i]!).trim() === ""; i -= 1) count += 1;
  return count;
}

test("taskIcon/taskIconColor derive from status plus merge progress", () => {
  assert.equal(taskIcon(task({ status: "new" }), 0), "○");
  assert.equal(taskIconColor(task({ status: "new" })), "accent");

  const running = task({ status: "running" });
  assert.notEqual(taskIcon(running, 0), taskIcon(running, 1));
  assert.equal(taskIconColor(running), "accent");

  assert.equal(taskIcon(task({ status: "completed", merge: "not-merged" }), 0), "●");
  assert.equal(taskIconColor(task({ status: "completed", merge: "not-merged" })), "accent");

  assert.equal(taskIcon(task({ status: "completed", merge: "integrating" }), 0), "⇣");
  assert.equal(taskIconColor(task({ status: "completed", merge: "integrating" })), "warning");

  assert.equal(taskIcon(task({ status: "completed", merge: "merged" }), 0), "✓");
  assert.equal(taskIconColor(task({ status: "completed", merge: "merged" })), "success");

  assert.equal(taskIcon(task({ status: "clarify" }), 0), "?");
  assert.equal(taskIconColor(task({ status: "clarify" })), "warning");

  assert.equal(taskIcon(task({ status: "blocked" }), 0), "!");
  assert.equal(taskIconColor(task({ status: "blocked" })), "error");

  assert.equal(taskIcon(task({ status: "cancelled" }), 0), "✗");
  assert.equal(taskIconColor(task({ status: "cancelled" })), "error");
});

test("each state paints its glyph in the right colour", () => {
  assert.ok(renderRow({ status: "new" }).includes(theme().fg("accent", "○")));
  assert.ok(renderRow({ status: "completed", merge: "not-merged" }).includes(theme().fg("accent", "●")));
  assert.ok(renderRow({ status: "completed", merge: "integrating" }).includes(theme().fg("warning", "⇣")));
  assert.ok(renderRow({ status: "completed", merge: "merged" }).includes(theme().fg("success", "✓")));
  assert.ok(renderRow({ status: "clarify" }).includes(theme().fg("warning", "?")));
  assert.ok(renderRow({ status: "blocked" }).includes(theme().fg("error", "!")));
  assert.ok(renderRow({ status: "cancelled" }).includes(theme().fg("error", "✗")));
});

test("the state is right-aligned to the row edge", () => {
  const view = new TasksView(() => {});
  view.tasks = [task({ status: "completed", merge: "merged", target: "main", title: "Short" })];
  const line = view.render(60).find((candidate) => candidate.includes("T1"))!;
  assert.equal(stripAnsi(line).length, 60, `row does not fill the width: ${stripAnsi(line)}`);
  assert.ok(stripAnsi(line).endsWith("merged → main"), `state is not right-aligned: ${stripAnsi(line)}`);
});

test("rows render flat (no group headers) in id order", () => {
  const tasks = [task({ id: "T2", title: "Two" }), task({ id: "T1", title: "One" }), task({ id: "T10", title: "Ten" })];
  const rows = contentRows(renderSelected(tasks, 0));
  assert.deepEqual(rows.map((row) => row.id), ["T1", "T2", "T10"], "rows are not flat and id-sorted");
});

test("clarify/blocked fall back to a terse label, with the reason in details", () => {
  assert.ok(stripAnsi(renderRow({ status: "clarify", detail: "which API?" })).includes("needs clarification"));
  assert.ok(stripAnsi(renderRow({ status: "blocked", detail: "missing token" })).includes("blocked"));

  const view = new TasksView(() => {});
  view.tasks = [task({ status: "blocked", detail: "missing token" })];
  view.handleInput("\r"); // menu (read-only -> Show details is first)
  view.handleInput("\r");
  assert.ok(stripAnsi(view.render(160).join("\n")).includes("missing token"), "reason missing from details");
});

test("the model status phrase is shown right-aligned when present", () => {
  const view = new TasksView(() => {});
  view.tasks = [task({ status: "running", progress: "writing parser tests" })];
  const line = view.render(60).find((candidate) => candidate.includes("T1"))!;
  assert.ok(stripAnsi(line).trimEnd().endsWith("writing parser tests"), `status not right-aligned: ${stripAnsi(line)}`);
  assert.ok(line.includes(theme().fg("muted", "writing parser tests")));
});

test("merge-failed rows paint only the leading bang red", () => {
  const line = renderRow({ merge: "failed" });
  assert.ok(stripAnsi(line).includes("! merge failed"), `status text changed: ${line}`);
  assert.equal(activeColor(line, line.indexOf("!")), theme().getFgAnsi("error"));
  assert.equal(activeColor(line, line.indexOf("merge failed")), theme().getFgAnsi("muted"));
});

test("completed tasks with a blocked merge show a terse merge-pending label", () => {
  const line = renderRow({ status: "completed", merge: "not-merged", mergeBlocked: "target main is not checked out" });
  assert.ok(stripAnsi(line).includes("merge pending"), stripAnsi(line));
  assert.ok(!stripAnsi(line).includes("not checked out"), `reason should not be in the row: ${stripAnsi(line)}`);
});

test("keeps the full status and ellipsises the title in its own colour", () => {
  const title = "Implement a very long feature that will not fit in a narrow panel";
  const view = new TasksView(() => {});
  view.tasks = [task({ title, status: "completed", merge: "merged", target: "midas/integration" })];
  const titleColor = theme().getFgAnsi("text");
  const narrow = view.render(48).find((line) => line.includes("T1"))!;
  assert.ok(stripAnsi(narrow).endsWith("merged → midas/integration"), `status was clipped: ${stripAnsi(narrow)}`);
  assert.ok(narrow.includes("…"), `title was not ellipsised: ${narrow}`);
  const ellipsis = narrow.indexOf("…");
  assert.equal(activeColor(narrow, ellipsis), titleColor, `ellipsis is not in the title colour: ${narrow}`);
});

test("id column aligns single and double digit ids", () => {
  const view = new TasksView(() => {});
  view.tasks = [task({ id: "T2", title: "Two" }), task({ id: "T13", title: "Thirteen" })];
  const lines = view.render(160).map(stripAnsi);
  const column = (line: string): number => line.indexOf("Two") >= 0 ? line.indexOf("Two") : line.indexOf("Thirteen");
  assert.equal(column(lines.find((line) => line.includes("T2"))!), column(lines.find((line) => line.includes("T13"))!));
});

test("panel fits its content with no blank rows while the selection moves", () => {
  const tasks = manyTasks();
  for (const selected of [0, Math.floor(tasks.length / 2), tasks.length - 1]) {
    const lines = renderSelected(tasks, selected);
    const marker = lines.find((line) => line.startsWith("→"));
    assert.ok(marker, `no selected row for index ${selected}`);
    assert.equal(trailingBlanks(lines), 0, `blank rows below the list at ${selected}`);
  }
});

test("details toggle renders without trailing blank rows", () => {
  const tasks = manyTasks();
  const view = new TasksView(() => {});
  view.tasks = tasks;
  view.handleInput("\r"); // menu
  view.handleInput("\r"); // Show details
  assert.ok(view.render(160).some((line) => line.includes("Worktree:")));
  assert.equal(trailingBlanks(view.render(160)), 0);
});

test("Enter opens an actions menu scoped to the task status", () => {
  const view = new TasksView(() => {}, true, { pause: () => {}, resume: () => {}, cancel: () => {}, remove: () => {} });
  view.tasks = [task({ id: "T1", status: "running" }), task({ id: "T2", status: "blocked" })];
  view.handleInput("\r");
  let text = stripAnsi(view.render(160).join("\n"));
  assert.match(text, /Actions/);
  assert.match(text, /Halt/);
  assert.match(text, /Cancel/);
  assert.doesNotMatch(text, /Resume/);
  assert.doesNotMatch(text, /Remove/, "a running task cannot be removed");
  view.handleInput("\x1b");
  selectRow(view, 1);
  view.handleInput("\r");
  text = stripAnsi(view.render(160).join("\n"));
  assert.match(text, /Resume/);
  assert.match(text, /Remove/);
  assert.doesNotMatch(text, /Halt/);
});

test("the default main agent sees a read-only board with no control actions", () => {
  const view = new TasksView(() => {}, false, { pause: () => {}, resume: () => {}, cancel: () => {}, remove: () => {} });
  view.tasks = [task({ id: "T1", status: "running" })];
  view.handleInput("\r");
  const text = stripAnsi(view.render(160).join("\n"));
  assert.match(text, /Actions/);
  assert.match(text, /Show details/);
  assert.doesNotMatch(text, /Halt|Resume|Cancel|Remove/);
});

test("Halt is wired to the pause control", () => {
  const halted: string[] = [];
  const view = new TasksView(() => {}, true, { pause: (id) => halted.push(id), resume: () => {}, cancel: () => {}, remove: () => {} });
  view.tasks = [task({ id: "T1", status: "running" })];
  view.handleInput("\r");
  view.handleInput("\r");
  assert.deepEqual(halted, ["T1"]);
});

test("selection stays on the bottom row while the window scrolls up", () => {
  const tasks = manyTasks();
  const first = contentRows(renderSelected(tasks, 0));
  assert.equal(first[0]?.id, "T1");
  assert.ok(first[0]?.arrow);

  const last = contentRows(renderSelected(tasks, tasks.length - 1));
  assert.equal(last.length, 7, `window was not full at the end: ${last.length}`);
  assert.ok(last.at(-1)?.arrow, "arrow is not on the last content row");
  assert.equal(last.filter((row) => row.arrow).length, 1);
});

test("empty and error renders keep a stable height", () => {
  const plain = new TasksView(() => {});
  const multitask = new TasksView(() => {}, true);
  assert.equal(plain.render(160).length, multitask.render(160).length);
});
