#!/usr/bin/env node

import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { join, relative, resolve } from "node:path";
import { Transcript } from "../../src/state/transcript.ts";

const root = resolve(import.meta.dirname, "..", "..");
const outFile = join(root, "internal", "opencode", "testdata", "transcript-reference.json");
const check = process.argv.includes("--check");
const originalNow = Date.now;
Date.now = () => 9000;

function partView(part) {
  return {
    kind: part.kind,
    id: part.id,
    text: part.kind === "text" || part.kind === "reasoning" ? part.text : null,
    ended: part.kind === "reasoning" ? part.ended : null,
    startedAt: part.kind === "reasoning" ? (part.startedAt ?? null) : null,
    endedAt: part.kind === "reasoning" ? (part.endedAt ?? null) : null,
    callID: part.kind === "tool" ? part.callID : null,
    tool: part.kind === "tool" ? part.tool : null,
    status: part.kind === "tool" ? part.status : null,
    input: part.kind === "tool" ? part.input : null,
    title: part.kind === "tool" ? (part.title ?? null) : null,
    output: part.kind === "tool" ? (part.output ?? null) : null,
    error: part.kind === "tool" ? (part.error ?? null) : null,
    metadata: part.kind === "tool" ? (part.metadata ?? null) : null,
    start: part.kind === "tool" ? part.start : null,
    end: part.kind === "tool" ? (part.end ?? null) : null,
  };
}

function project(transcript) {
  return {
    phase: transcript.phase,
    reconnecting: transcript.reconnecting,
    session: transcript.session ? { id: transcript.session.id, title: transcript.session.title ?? null } : null,
    messages: transcript.messages.map((message) => ({
      id: message.id,
      role: message.role,
      agent: message.agent ?? null,
      providerID: message.providerID ?? null,
      modelID: message.modelID ?? null,
      cost: message.cost,
      tokens: message.tokens,
      created: message.created ?? null,
      completed: message.completed ?? null,
      error: message.error ?? null,
      notice: message.notice ?? false,
      hidden: message.hidden ?? false,
      steer: message.steer ?? false,
      imageFilenames: message.imageFilenames ?? [],
      parts: message.parts.map(partView),
    })),
    permissions: transcript.permissions.map((permission) => ({
      id: permission.id,
      sessionID: permission.sessionID ?? null,
      type: permission.type ?? null,
      title: permission.title ?? null,
      pattern: permission.pattern ?? null,
    })),
    questions: transcript.questions.map((question) => ({
      id: question.id,
      sessionID: question.sessionID ?? null,
      questions: question.questions,
    })),
    totals: transcript.totals(),
    tools: transcript.countTools(),
  };
}

try {
  const transcript = new Transcript();
  transcript.reset({ id: "s1", title: "Initial" });
  transcript.upsertMessage({ id: "u1", sessionID: "s1", role: "user", agent: "build", time: { created: 100 } });
  transcript.upsertPart({ id: "p1", sessionID: "s1", messageID: "u1", type: "text", text: "hello" });
  transcript.upsertPart({ id: "p1", sessionID: "s1", messageID: "u1", type: "text", text: "hel" });
  transcript.appendPartDelta("p1", "text", "!");
  transcript.upsertPart({ id: "f1", sessionID: "s1", messageID: "u1", type: "file", filename: "image.png" });
  transcript.upsertPart({ id: "f2", sessionID: "s1", messageID: "u1", type: "file", filename: "image.png" });

  transcript.upsertMessage({ id: "a1", sessionID: "s1", role: "assistant", mode: "plan", providerID: "provider", modelID: "model", cost: 1.25, tokens: { input: 10, output: 20, reasoning: 3, cache: { read: 4, write: 5 } }, time: { created: 105, completed: 150 } });
  transcript.upsertPart({ id: "r1", sessionID: "s1", messageID: "a1", type: "reasoning", text: "think", time: { start: 110 } });
  transcript.upsertPart({ id: "r1", sessionID: "s1", messageID: "a1", type: "reasoning", text: "think", time: { start: 110 } }, "ing");
  transcript.upsertPart({ id: "r1", sessionID: "s1", messageID: "a1", type: "reasoning", text: "thinking", time: { start: 110, end: 130 } });
  transcript.upsertPart({ id: "t1", sessionID: "s1", messageID: "a1", type: "tool", callID: "call1", tool: "search", state: { status: "pending", input: { q: 1 } } });
  transcript.upsertPart({ id: "t1", sessionID: "s1", messageID: "a1", type: "tool", callID: "call1", tool: "search", state: { status: "running", input: { q: 2 }, title: "Searching", metadata: { count: 1 }, time: { start: 120 } } });
  transcript.upsertPart({ id: "t1", sessionID: "s1", messageID: "a1", type: "tool", callID: "call1", tool: "search", state: { status: "completed", input: { q: 2 }, title: "Search done", output: "result", time: { start: 120, end: 140 } } });
  transcript.upsertPart({ id: "step1", sessionID: "s1", messageID: "a1", type: "step-start" });

  transcript.upsertMessage({ id: "a2", sessionID: "s1", role: "assistant", mode: "build", providerID: "provider", modelID: "model", cost: 0.5, tokens: {}, time: { created: 160 }, error: { name: "APIError", data: { message: "fetch failed for host" } } });
  transcript.addPermission({ id: "perm1", sessionID: "s1", type: "bash", title: "Run command", pattern: ["npm test"] });
  transcript.addPermission({ id: "perm1", sessionID: "s1", type: "bash", title: "duplicate" });
  transcript.addPermission({ id: "perm2", sessionID: "s1", type: "edit", title: "Edit file", pattern: "*.go" });
  transcript.resolvePermission("perm1");
  transcript.addQuestion({ id: "q1", sessionID: "s1", questions: [{ header: "Choice", question: "Pick", options: [{ label: "A" }] }] });
  transcript.addQuestion({ id: "q2", sessionID: "s1", questions: [{ question: "Continue?", options: [{ label: "Yes", description: "Proceed" }], multiple: true }] });
  transcript.resolveQuestion("q1");
  transcript.setPhase("retry");
  transcript.setReconnecting(true);
  transcript.setPhase("busy");
  transcript.setSession({ id: "s1", title: "Updated" });

  const output = JSON.stringify({ streamingAndMetadata: project(transcript) }, null, 2) + "\n";
  if (check) {
    const current = existsSync(outFile) ? readFileSync(outFile, "utf8") : "";
    if (current !== output) { console.error("gen-transcript-goldens: goldens are stale; re-run without --check"); process.exitCode = 1; }
    else console.log("transcript goldens are current");
  } else {
    mkdirSync(join(root, "internal", "opencode", "testdata"), { recursive: true });
    writeFileSync(outFile, output, { flag: "w" });
    console.log(`wrote ${relative(root, outFile)}`);
  }
} finally {
  Date.now = originalNow;
}
