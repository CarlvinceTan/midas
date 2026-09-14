import assert from "node:assert/strict";
import { test } from "node:test";
import { initTheme as initPiTheme } from "@earendil-works/pi-coding-agent";
import { visibleWidth } from "@earendil-works/pi-tui";
import { stripAnsi } from "../../lib/ansi.ts";
import { initTheme } from "../../theme/theme.ts";
import type { ToolView } from "../../state/transcript.ts";
import { formatMcpDisplayName, renderSubagentTool, renderTool, setMcpServerNames, toolLiveText } from "./tool-call.ts";

initPiTheme(undefined, false);
initTheme(undefined);

const diff = ["--- a/src/a.ts", "+++ b/src/a.ts", "@@ -1,2 +1,2 @@", " const x = 1;", "-old line", "+new line"].join(
  "\n",
);

function edit(extra: Partial<ToolView> = {}): ToolView {
  return {
    kind: "tool",
    id: "t1",
    callID: "t1",
    tool: "edit",
    status: "completed",
    input: { filePath: "src/a.ts" },
    metadata: { diff },
    output: "",
    start: 0,
    end: 1,
    ...extra,
  };
}

test("edit rows color the +added and -removed counts", () => {
  const [line] = renderTool(edit(), 80, false, "/cwd");
  assert.ok(line);
  assert.equal(stripAnsi(line), "✓ Edited src/a.ts +1 -1");
  // Both numbers carry their own color (green plus, red minus).
  assert.match(line, /\x1b\[[0-9;]*m\+1/);
  assert.match(line, /\x1b\[[0-9;]*m-1/);
});

test("expanded edit rows show colored diff lines", () => {
  const lines = renderTool(edit(), 80, true, "/cwd");
  assert.equal(stripAnsi(lines[0]!), "✓ Edited src/a.ts +1 -1");
  // Git header noise (Index/===/---/+++/@@) is dropped; hunk body remains.
  const body = lines.slice(1).map(stripAnsi);
  assert.deepEqual(body, [" const x = 1;", "-old line", "+new line"]);
  assert.match(lines[2]!, /\x1b\[[0-9;]*m-/);
  assert.match(lines[3]!, /\x1b\[[0-9;]*m\+/);
});

test("edit diff preview skips git headers mixed into the patch", () => {
  const patch = [
    "Index: src/a.ts",
    "===================================================================",
    "--- src/a.ts",
    "+++ src/a.ts",
    "@@ -1350,3 +1350,3 @@",
    " context",
    "-removed",
    "+added",
  ].join("\n");
  const lines = renderTool(edit({ metadata: { diff: patch } }), 80, true, "/cwd");
  assert.deepEqual(lines.slice(1).map(stripAnsi), [" context", "-removed", "+added"]);
});

test("edit stats fall back to filediff counts", () => {
  const tool = edit({ metadata: { filediff: { additions: 10, deletions: 4 } } });
  const [line] = renderTool(tool, 80, false, "/cwd");
  assert.equal(stripAnsi(line!), "✓ Edited src/a.ts +10 -4");
});

test("toolLiveText reports preparing vs running actions", () => {
  const pending = edit({ status: "pending", input: { command: "npm test" }, tool: "bash" });
  assert.equal(toolLiveText(pending, "/cwd"), "Writing command");
  const running = edit({ status: "running", input: { command: "npm test" }, tool: "bash" });
  assert.equal(toolLiveText(running, "/cwd"), "Running `npm test`");
  const reading = edit({ status: "running", input: { filePath: "src/a.ts" }, tool: "read" });
  assert.equal(toolLiveText(reading, "/cwd"), "Reading src/a.ts");
});

test("question tools read as asking, not the generic running label", () => {
  const asking = edit({ status: "running", tool: "question", input: { questions: [] } });
  assert.equal(toolLiveText(asking, "/cwd"), "Asking question");
  const asked = edit({ status: "completed", tool: "question", input: { questions: [] } });
  assert.equal(stripAnsi(renderTool(asked, 80, false, "/cwd")[0]!), "✓ Asked question");
});

test("skill tools name the skill instead of using the generic tool label", () => {
  const using = edit({ status: "running", tool: "skill", input: { name: "control" } });
  assert.equal(toolLiveText(using, "/cwd"), "Using Control skill");
  const used = edit({ tool: "skill", input: { name: "control" }, output: "Loaded skill: control" });
  assert.equal(stripAnsi(renderTool(used, 80, false, "/cwd")[0]!), "✓ Used Control skill");
});

test("OpenCode task tools use the Pi-style subagent presentation", () => {
  const task = edit({
    status: "running",
    tool: "task",
    input: {
      subagent_type: "explore",
      description: "Inspect Lightning feature gaps",
      prompt: "Inspect the repository and report any missing features.",
    },
  });
  assert.equal(toolLiveText(task, "/cwd"), "Running Subagents (1 task)");
  assert.deepEqual(renderSubagentTool(task, 80, false, "⠋").map(stripAnsi), [
    "⠋ Running Subagents (1 task):",
    "⠋ Explore: Inspect Lightning feature gaps",
  ]);

  const completed = { ...task, status: "completed" as const };
  assert.deepEqual(renderSubagentTool(completed, 80, false).map(stripAnsi), [
    "✓ Subagents (1 task):",
    "✓ Explore: Inspect Lightning feature gaps",
  ]);
});

test("expanded subagent tools show the delegated prompt and unwrapped result", () => {
  const task = edit({
    tool: "task",
    input: { subagent_type: "code-reviewer", description: "Review changes", prompt: "Review the current diff." },
    output: "<task id=\"child\"><task_result>Looks good.</task_result></task>",
  });
  assert.deepEqual(renderTool(task, 80, true, "/cwd").map(stripAnsi), [
    "✓ Code Reviewer",
    "",
    "─── Task ───",
    "Review the current diff.",
    "",
    "─── Output ───",
    "Looks good.",
  ]);
});

test("formatMcpDisplayName title-cases server and tool names", () => {
  assert.equal(formatMcpDisplayName("davinci-resolve"), "Davinci Resolve");
  assert.equal(formatMcpDisplayName("openaiDeveloperDocs"), "Openai Developer Docs");
  assert.equal(formatMcpDisplayName("web_search_exa"), "Web Search Exa");
});

test("MCP tools render as '<Server> MCP: <Tool>'", () => {
  setMcpServerNames(["davinci-resolve", "Supabase_Midas"]);
  const mcp = edit({ tool: "davinci-resolve_folder", input: {}, output: "ok" });
  const [line] = renderTool(mcp, 80, false, "/cwd");
  assert.equal(stripAnsi(line!), "✓ Davinci Resolve MCP: Folder");
  // Longest server name wins so the tool part stays intact.
  const supabase = edit({ tool: "Supabase_Midas_execute_sql", input: {}, output: "ok" });
  assert.equal(stripAnsi(renderTool(supabase, 80, false, "/cwd")[0]!), "✓ Supabase Midas MCP: Execute Sql");
  setMcpServerNames([]);
});

test("long paths are middle-shortened and keep the edit stats", () => {
  const longPath = "/Users/carlvincetan/code/midas/src/ui/components/sessions-view.ts";
  const patch = [
    "--- a",
    "+++ b",
    "@@ -1 +1 @@",
    ...Array.from({ length: 8 }, (_, i) => `+added ${i}`),
    ...Array.from({ length: 7 }, (_, i) => `-removed ${i}`),
  ].join("\n");
  const tool = edit({ input: { filePath: longPath }, metadata: { diff: patch } });
  const [line] = renderTool(tool, 60, false, "/tmp");
  assert.ok(line);
  assert.ok(visibleWidth(line) <= 60, `line too wide: ${visibleWidth(line)}`);
  const plain = stripAnsi(line);
  assert.match(plain, /sessions-view\.ts/);
  assert.match(plain, /\+8 -7/);
  assert.match(plain, /\.\.\./);
});
