import assert from "node:assert/strict";
import { test } from "node:test";
import type { MessageView, PartView } from "../state/transcript.ts";
import {
  buildRunSegments,
  computeRuns,
  countFailures,
  finalTextIndex,
  formatRunDuration,
  formatSeconds,
  isLiveItem,
  isPendingSteerRun,
  liveRunId,
  runDurationMs,
  summarizeChain,
} from "./run-model.ts";
import { setMcpServerNames } from "./components/tool-call.ts";

function message(id: string, role: "user" | "assistant", parts: PartView[], extra: Partial<MessageView> = {}): MessageView {
  return {
    id,
    role,
    cost: 0,
    tokens: { input: 0, output: 0, reasoning: 0, cacheRead: 0, cacheWrite: 0 },
    parts,
    ...extra,
  };
}

const text = (id: string, value: string): PartView => ({ kind: "text", id, text: value });
const tool = (id: string, name: string, status: "running" | "completed" | "pending" | "error" = "completed", start = 0): PartView => ({
  kind: "tool",
  id,
  callID: id,
  tool: name,
  status,
  input: {},
  start,
});

test("computeRuns starts a run at each user message and keeps orphans together", () => {
  const messages = [
    message("notice0", "assistant", [text("n0", "hi")], { notice: true }),
    message("u1", "user", [text("u1t", "do it")]),
    message("a1", "assistant", [tool("t1", "read")]),
    message("u2", "user", [text("u2t", "again")]),
    message("a2", "assistant", [text("a2t", "done")]),
  ];
  const runs = computeRuns(messages);
  assert.equal(runs.length, 3);
  assert.equal(runs[0]!.id, "notice0");
  assert.equal(runs[0]!.prompt, undefined);
  assert.equal(runs[1]!.id, "u1");
  assert.equal(runs[1]!.prompt?.id, "u1");
  assert.equal(runs[2]!.id, "u2");
});

test("buildRunSegments merges consecutive tools across messages and splits on text", () => {
  const run = computeRuns([
    message("u1", "user", [text("u1t", "go")]),
    message("a1", "assistant", [tool("t1", "read"), tool("t2", "bash")]),
    message("a2", "assistant", [tool("t3", "grep")]),
    message("a3", "assistant", [text("txt", "here is the answer")]),
  ])[0]!;
  const segments = buildRunSegments(run);
  assert.equal(segments.length, 2);
  assert.equal(segments[0]!.kind, "activity");
  assert.deepEqual(
    (segments[0] as Extract<typeof segments[0], { kind: "activity" }>).items.map((i) => i.part.id),
    ["t1", "t2", "t3"],
  );
  assert.equal(segments[1]!.kind, "text");
});

test("buildRunSegments keeps reasoning in the chain and bash as its own segment", () => {
  const run = computeRuns([
    message("u1", "user", [text("u1t", "go")]),
    message("a1", "assistant", [
      { kind: "reasoning", id: "r1", text: "hmm", ended: true },
      tool("t1", "read"),
      { kind: "bash", id: "b1", command: "ls", output: "", status: "complete", exclude: false },
      text("txt", "answer"),
    ]),
  ])[0]!;
  const segments = buildRunSegments(run);
  assert.deepEqual(segments.map((s) => s.kind), ["activity", "bash", "text"]);
});

test("finalTextIndex picks the last text segment", () => {
  const run = computeRuns([
    message("u1", "user", [text("u1t", "go")]),
    message("a1", "assistant", [text("t1", "intermediate"), tool("tool1", "read")]),
    message("a2", "assistant", [text("t2", "final")]),
  ])[0]!;
  const segments = buildRunSegments(run);
  // intermediate text, activity, final text
  assert.equal(segments.length, 3);
  assert.equal(finalTextIndex(segments), 2);
});

test("runDurationMs uses stored timestamps and ignores missing ends", () => {
  const run = computeRuns([
    message("u1", "user", [text("u1t", "go")], { created: 1000 }),
    message("a1", "assistant", [tool("t1", "read", "completed", 1100)], { created: 1100, completed: 4200 }),
  ])[0]!;
  assert.equal(runDurationMs(run), 3200);

  const noEnd = computeRuns([message("u2", "user", [text("x", "go")], { created: 1000 })]);
  assert.equal(runDurationMs(noEnd[0]!), undefined);
  assert.equal(formatRunDuration(3200), "3s");
});

test("buildRunSegments splits text at assistant message boundaries but chains span them", () => {
  const run = computeRuns([
    message("u1", "user", [text("u1t", "go")]),
    message("a1", "assistant", [text("t1", "let me look"), tool("tool1", "read")]),
    message("a2", "assistant", [tool("tool2", "read"), text("t2", "here is the answer")]),
  ])[0]!;
  const segments = buildRunSegments(run);
  assert.deepEqual(segments.map((s) => s.kind), ["text", "activity", "text"]);
  const activity = segments[1];
  assert.ok(activity && activity.kind === "activity");
  assert.deepEqual(activity.items.map((i) => i.part.id), ["tool1", "tool2"]);
  assert.equal(finalTextIndex(segments), 2);
});

test("isLiveItem flags only in-flight tools and thinking", () => {
  assert.equal(isLiveItem({ kind: "tool", part: tool("t", "bash", "running") as never }), true);
  assert.equal(isLiveItem({ kind: "tool", part: tool("t", "bash", "pending") as never }), true);
  assert.equal(isLiveItem({ kind: "tool", part: tool("t", "bash", "completed") as never }), false);
  assert.equal(isLiveItem({ kind: "reasoning", part: { kind: "reasoning", id: "r", text: "", ended: false } }), true);
  assert.equal(isLiveItem({ kind: "reasoning", part: { kind: "reasoning", id: "r", text: "", ended: true } }), false);
});

test("formatSeconds never shows milliseconds", () => {
  assert.equal(formatSeconds(0), "0s");
  assert.equal(formatSeconds(413), "0s");
  assert.equal(formatSeconds(1999), "1s");
  assert.equal(formatSeconds(59_400), "59s");
  assert.equal(formatSeconds(65_000), "1m 5s");
});

test("countFailures counts only errored tool calls", () => {
  const run = computeRuns([
    message("u1", "user", [text("u1t", "go")]),
    message("a1", "assistant", [tool("t1", "bash", "error"), tool("t2", "read"), tool("t3", "grep", "error")]),
  ])[0]!;
  const segments = buildRunSegments(run);
  const activity = segments.find((s) => s.kind === "activity");
  assert.ok(activity && activity.kind === "activity");
  assert.equal(countFailures(activity.items), 2);
  assert.equal(countFailures([]), 0);
});

test("steered user messages start their own run only once the agent responds", () => {
  const base = [
    message("u1", "user", [text("p", "first")]),
    message("a1", "assistant", [text("t1", "working")]),
    message("u2", "user", [text("s", "steer")], { steer: true }),
  ];
  // Until the agent answers the steer, it stays pending so the earlier turn is
  // still treated as the live one.
  const pending = computeRuns(base);
  assert.equal(pending.length, 2);
  assert.equal(isPendingSteerRun(pending[1]!), true);

  // Once assistant output follows, the steer is a normal run.
  const answered = [...base, message("a2", "assistant", [text("t2", "done")])];
  const runs = computeRuns(answered);
  assert.equal(runs.length, 2);
  assert.equal(isPendingSteerRun(runs[1]!), false);
});

test("liveRunId keeps the steered turn live until the steer is picked up", () => {
  const pending = computeRuns([
    message("u1", "user", [text("p", "first")]),
    message("a1", "assistant", [tool("t1", "read", "running")]),
    message("u2", "user", [text("s", "steer")], { steer: true }),
  ]);
  assert.equal(liveRunId(pending), "u1");

  const answered = computeRuns([
    message("u1", "user", [text("p", "first")]),
    message("a1", "assistant", [tool("t1", "read")]),
    message("u2", "user", [text("s", "steer")], { steer: true }),
    message("a2", "assistant", [text("t2", "on it")]),
  ]);
  assert.equal(liveRunId(answered), "u2");
});

test("summarizeChain produces ChatGPT-style category phrases", () => {
  const skill = tool("t4", "skill");
  if (skill.kind === "tool") skill.input = { name: "control" };
  const run = computeRuns([
    message("u1", "user", [text("u1t", "go")]),
    message("a1", "assistant", [tool("t1", "bash"), tool("t2", "read"), tool("t3", "read"), skill]),
  ])[0]!;
  const segments = buildRunSegments(run);
  const activity = segments.find((s) => s.kind === "activity");
  assert.ok(activity && activity.kind === "activity");
  assert.equal(summarizeChain(activity.items), "Ran 1 command, read 2 files, used Control skill");
});

test("activity summaries identify skills and MCP servers by name", () => {
  const skill = tool("skill", "skill");
  const mcp = tool("mcp", "davinci-resolve_folder");
  if (skill.kind === "tool") skill.input = { name: "control" };
  setMcpServerNames(["davinci-resolve"]);
  try {
    assert.equal(
      summarizeChain([
        { kind: "tool", part: skill as Extract<PartView, { kind: "tool" }> },
        { kind: "tool", part: tool("bash", "bash") as Extract<PartView, { kind: "tool" }> },
      ]),
      "Used Control skill, ran 1 command",
    );
    assert.equal(
      summarizeChain([{ kind: "tool", part: mcp as Extract<PartView, { kind: "tool" }> }]),
      "Used Davinci Resolve MCP",
    );
  } finally {
    setMcpServerNames([]);
  }
});
