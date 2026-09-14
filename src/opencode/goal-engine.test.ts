/**
 * Evidence tests for the ACTUAL external OpenCode goal engine.
 *
 * These tests import the published `@prevalentware/opencode-goal-plugin@0.1.48`
 * server entrypoint (pinned as an exact dev dependency) and drive its real hooks,
 * tools, persistence, and continuation timers through a bounded fake OpenCode
 * SDK/event adapter. They never run `opencode serve`, call a model, touch the board,
 * or read private user goal state: every run uses a private temp state file and
 * sandboxed HOME/XDG/MIDAS/PI roots established before the plugin is imported.
 *
 * See docs/goal-engine-evidence.md for the evidence matrix, the confirmed V1
 * fast-turn defect, and the explicit boundary between this actual-engine proof and
 * the still-pending frontend bridge and manual/live acceptance.
 */
import assert from "node:assert/strict";
import { test } from "node:test";
import {
  createSandbox,
  createV1Harness,
  createV2Harness,
  installSandboxRoots,
  installedPluginIdentity,
  loadGoalEngine,
  nowSeconds,
  PINNED_PLUGIN_VERSION,
  PLUGIN_PACKAGE,
  sleep,
  type AnyRecord,
  type V1Harness,
  type V2Harness,
} from "./goal-engine-fixture.ts";

// Sandbox roots must exist before the plugin module is imported.
const rootSandbox = createSandbox("midas-goal-engine-root-");
installSandboxRoots(rootSandbox);
const engine = await loadGoalEngine();

async function v1(pluginOptions: AnyRecord = {}, sessionID = "ses_goal_v1"): Promise<V1Harness> {
  return createV1Harness({ engine, sandbox: createSandbox(), pluginOptions, sessionID });
}

async function v2(pluginOptions: AnyRecord = {}, sessionID = "ses_goal_v2"): Promise<V2Harness> {
  return createV2Harness({ engine, sandbox: createSandbox(), pluginOptions, sessionID });
}

function progressEvent(sessionID: string, id: string, text: string): AnyRecord {
  return {
    type: "message.updated",
    properties: {
      sessionID,
      info: { id, role: "assistant", parts: [{ type: "text", text }] },
    },
  };
}

function executionEvents(sessionID: string, messageID: string, text: string, created: number): AnyRecord[] {
  return [
    { type: "session.execution.started", data: { sessionID }, created },
    { type: "session.step.started", data: { sessionID, assistantMessageID: messageID, agent: "build" }, created },
    { type: "session.text.ended", data: { sessionID, assistantMessageID: messageID, text }, created },
    { type: "session.step.ended", data: { sessionID, assistantMessageID: messageID, tokens: { output: 100 } }, created },
    { type: "session.execution.succeeded", data: { sessionID }, created },
  ];
}

test("artifact identity: pinned published goal plugin 0.1.48 exposes server/setup", () => {
  const identity = installedPluginIdentity();
  assert.equal(identity.name, PLUGIN_PACKAGE);
  assert.equal(identity.version, PINNED_PLUGIN_VERSION);
  assert.ok(identity.exports["./server"], "plugin must publish ./server");
  assert.ok(identity.exports["./tui"], "plugin must publish ./tui");
  assert.equal(engine.id, "local.goal-mode.server");
  assert.equal(typeof engine.server, "function");
  assert.equal(typeof engine.setup, "function");
});

test("V1 create/reuse/conflict follow the documented non-closed-goal rules", async () => {
  const h = await v1();
  try {
    const created = JSON.parse(await h.callTool("create_goal", { objective: "  ship the goal engine check  " }));
    assert.equal(created.goal.status, "active");
    assert.equal(created.goal.objective, "ship the goal engine check");
    assert.equal(created.goal.tokenBudget, null);
    assert.equal(created.goal.maxAutoTurns, null, "per-goal auto-turn limit is unset until configured");
    const createdHistory = (await h.getGoal())?.history as Array<{ type: string }>;
    assert.ok(createdHistory.some((entry) => entry.type === "created"));

    const reused = JSON.parse(await h.callTool("create_goal", { objective: "ship the goal engine check" }));
    assert.equal(reused.goal_reused, true);
    assert.match(reused.duplicate_goal_notice, /already exists/);
    assert.equal(reused.goal.objective, "ship the goal engine check");

    const conflict = JSON.parse(await h.callTool("create_goal", { objective: "a completely different objective" }));
    assert.equal(conflict.goal_conflict, true);
    assert.match(conflict.goal_conflict_notice, /different non-closed goal/);
    assert.equal((await h.getGoal())?.objective, "ship the goal engine check");
  } finally {
    await h.dispose();
  }
});

test("V1 objective edits update the objective, status, and history", async () => {
  const h = await v1();
  try {
    await h.callTool("create_goal", { objective: "first objective" });

    const paused = JSON.parse(await h.callTool("update_goal_objective", { objective: "second objective", status: "paused" }));
    assert.equal(paused.goal.objective, "second objective");
    assert.equal(paused.goal.status, "paused");

    const resumed = JSON.parse(await h.callTool("update_goal_objective", { objective: "third objective", status: "active" }));
    assert.equal(resumed.goal.objective, "third objective");
    assert.equal(resumed.goal.status, "active");

    const goal = await h.getGoal();
    const historyTypes = (goal?.history as Array<{ type: string }>).map((entry) => entry.type);
    assert.ok(historyTypes.includes("updated"));
  } finally {
    await h.dispose();
  }
});

test("V1 pause/resume/clear work and a closed goal cannot be resumed", async () => {
  const h = await v1();
  try {
    await h.callTool("create_goal", { objective: "lifecycle" });

    const paused = JSON.parse(await h.callTool("update_goal_status", { status: "paused" }));
    assert.equal(paused.goal.status, "paused");
    assert.equal(paused.goal.stopReason, "paused");

    const resumed = JSON.parse(await h.callTool("update_goal_status", { status: "active" }));
    assert.equal(resumed.goal.status, "active");
    assert.equal(resumed.goal.stopReason, null);

    const closed = JSON.parse(await h.callTool("update_goal", { status: "complete", evidence: "verified" }));
    assert.equal(closed.goal.status, "complete");
    await assert.rejects(() => h.callTool("update_goal_status", { status: "active" }), /closed/);

    const cleared = JSON.parse(await h.callTool("clear_goal", {}));
    assert.equal(cleared.cleared, true);
    assert.equal(await h.getGoal(), null);
  } finally {
    await h.dispose();
  }
});

test("V1 complete requires evidence and unmet requires a blocker", async () => {
  const h = await v1();
  try {
    await h.callTool("create_goal", { objective: "close safely" });
    await assert.rejects(() => h.callTool("update_goal", { status: "complete" }), /completion evidence/);
    assert.equal((await h.getGoal())?.status, "active");

    const complete = JSON.parse(
      await h.callTool("update_goal", { status: "complete", evidence: "tests pass and artifact inspected" }),
    );
    assert.equal(complete.goal.status, "complete");
    assert.equal(complete.goal.completionEvidence, "tests pass and artifact inspected");
    assert.match(complete.completion_report, /Goal achieved/);

    await h.callTool("clear_goal", {});
    await h.callTool("create_goal", { objective: "blocked safely" });
    await assert.rejects(() => h.callTool("update_goal", { status: "unmet" }), /blocker/);

    const unmet = JSON.parse(await h.callTool("update_goal", { status: "unmet", blocker: "external artifact missing" }));
    assert.equal(unmet.goal.status, "unmet");
    assert.equal(unmet.goal.blocker, "external artifact missing");
    assert.match(unmet.unmet_report, /Goal unmet/);
  } finally {
    await h.dispose();
  }
});

test("V1 plan-agent create is paused, resume is refused, and opt-out allows execution", async () => {
  const restricted = await v1();
  try {
    const created = JSON.parse(await restricted.callTool("create_goal", { objective: "plan only" }, { agent: "plan" }));
    assert.equal(created.goal.status, "paused");
    assert.match(created.plan_mode_notice, /Plan mode/);
    assert.equal(created.goal.stopReason, "plan mode");

    await assert.rejects(
      () => restricted.callTool("update_goal_status", { status: "active" }, { agent: "plan" }),
      /Plan mode/,
    );

    const edited = JSON.parse(
      await restricted.callTool("update_goal_objective", { objective: "plan edit", status: "active" }, { agent: "plan" }),
    );
    assert.equal(edited.goal.status, "paused");
  } finally {
    await restricted.dispose();
  }

  const optedOut = await v1({ allow_goal_execution_from_plan: true });
  try {
    const created = JSON.parse(await optedOut.callTool("create_goal", { objective: "opt out" }, { agent: "plan" }));
    assert.equal(created.goal.status, "active");
  } finally {
    await optedOut.dispose();
  }
});

test("V1 auto-continue pauses instead of continuing under a restricted agent", async () => {
  const h = await v1({ min_continue_interval_seconds: 0 });
  try {
    await h.callTool("create_goal", { objective: "restricted continue" }, { agent: "build" });
    await h.hooks["chat.message"]({ sessionID: h.sessionID, agent: "plan" }, { message: { sessionID: h.sessionID, agent: "plan" } });
    await h.emit({ type: "session.idle", properties: { sessionID: h.sessionID } });

    assert.equal(h.prompts.length, 0);
    const goal = await h.getGoal();
    assert.equal(goal?.status, "paused");
    assert.equal(goal?.stopReason, "plan mode");
  } finally {
    await h.dispose();
  }
});

test("V1 injects the orchestrator goal policy into system context", async () => {
  const h = await v1();
  try {
    const output: { system: string[] } = { system: [] };
    await h.hooks["experimental.chat.system.transform"]({ sessionID: h.sessionID }, output);
    assert.equal(output.system.length, 1);
    assert.match(output.system[0]!, /OpenCode goal mode policy/);
    assert.match(output.system[0]!, /Manage goals only through the goal tools/);
    assert.match(output.system[0]!, /Plan mode or another restricted agent/);

    await h.hooks["experimental.chat.system.transform"]({ sessionID: h.sessionID }, output);
    assert.equal(output.system.length, 1, "policy injection must be idempotent");
  } finally {
    await h.dispose();
  }
});

test("V1 duplicate idle delivery does not double-reserve a continuation", async () => {
  const h = await v1({ min_continue_interval_seconds: 0 });
  try {
    await h.callTool("create_goal", { objective: "dedupe idle" });

    await h.emit({ type: "session.idle", properties: { sessionID: h.sessionID } });
    assert.equal(h.prompts.length, 1);

    await h.emit({ type: "session.idle", properties: { sessionID: h.sessionID } });
    await h.emit({ type: "session.status", properties: { sessionID: h.sessionID, status: { type: "idle" } } });
    assert.equal(h.prompts.length, 1, "duplicate idle deliveries must not send a second prompt");
    assert.equal((await h.getGoal())?.autoTurns, 1);

    await h.emit(progressEvent(h.sessionID, "m1", "continuation made progress"));
    await h.emit({ type: "session.idle", properties: { sessionID: h.sessionID } });
    assert.equal(h.prompts.length, 2);
    assert.equal((await h.getGoal())?.autoTurns, 2);
  } finally {
    await h.dispose();
  }
});

test("V1 auto-turn limit triggers exactly one wrap-up handoff", async () => {
  const h = await v1({ min_continue_interval_seconds: 0, max_auto_turns: 2 });
  try {
    await h.callTool("create_goal", { objective: "bounded turns" });

    for (let i = 1; i <= 2; i += 1) {
      await h.emit({ type: "session.idle", properties: { sessionID: h.sessionID } });
      await h.emit(progressEvent(h.sessionID, `m${i}`, `progress ${i}`));
    }
    assert.equal(h.prompts.length, 2);

    await h.emit({ type: "session.idle", properties: { sessionID: h.sessionID } });
    assert.equal((await h.getGoal())?.status, "usageLimited");
    assert.equal(h.prompts.length, 3);
    assert.equal(h.promptTexts().filter((text) => /safety limit/i.test(text)).length, 1);

    await h.emit({ type: "session.idle", properties: { sessionID: h.sessionID } });
    assert.equal(h.prompts.length, 3, "the wrap-up handoff is one-shot");
  } finally {
    await h.dispose();
  }
});

test("V1 token budget reaches budgetLimited with a single wrap-up", async () => {
  const h = await v1({ min_continue_interval_seconds: 0 });
  try {
    await h.callTool("create_goal", { objective: "bounded tokens", token_budget: 100 });

    await h.hooks["experimental.chat.messages.transform"](
      { sessionID: h.sessionID },
      { messages: [{ info: { sessionID: h.sessionID, tokens: { total: 1000 } } }] },
    );
    assert.equal((await h.getGoal())?.tokensUsed, 0);

    await h.hooks["experimental.chat.messages.transform"](
      { sessionID: h.sessionID },
      { messages: [{ info: { sessionID: h.sessionID, tokens: { total: 1200 } } }] },
    );
    const limited = await h.getGoal();
    assert.equal(limited?.status, "budgetLimited");
    assert.equal(limited?.tokensUsed, 200);
    assert.equal(limited?.remainingTokens, 0);

    await h.emit({ type: "session.idle", properties: { sessionID: h.sessionID } });
    assert.equal(h.prompts.length, 1);
    assert.equal(h.promptTexts().filter((text) => /safety limit/i.test(text)).length, 1);

    await h.emit({ type: "session.idle", properties: { sessionID: h.sessionID } });
    assert.equal(h.prompts.length, 1);
  } finally {
    await h.dispose();
  }
});

test("V1 max-duration limit fires and paused time is excluded from elapsed time", async () => {
  const h = await v1({ min_continue_interval_seconds: 0 });
  try {
    await h.callTool("create_goal", { objective: "bounded duration", max_duration_seconds: 3600 });

    h.patchPersistedGoal({ timeUsedSeconds: 5, lastAccountedAt: nowSeconds() - 10 });
    const active = await h.getGoal();
    assert.ok((active?.timeUsedSeconds ?? 0) >= 14, `active wall-clock time should accrue, got ${active?.timeUsedSeconds}`);

    await h.callTool("update_goal_status", { status: "paused" });
    const atPause = h.readPersistedGoal()?.timeUsedSeconds as number;
    await sleep(1100);
    assert.equal(h.readPersistedGoal()?.timeUsedSeconds, atPause, "paused duration must not accrue");

    await h.callTool("update_goal_status", { status: "active" });
    const afterResume = await h.getGoal();
    assert.ok((afterResume?.timeUsedSeconds ?? 0) <= atPause + 1, `resume leaked paused time: ${afterResume?.timeUsedSeconds}`);

    h.patchPersistedGoal({
      timeUsedSeconds: 3600,
      maxDurationSeconds: 3600,
      lastAccountedAt: null,
      status: "active",
      stopReason: null,
    });
    await h.emit({ type: "session.idle", properties: { sessionID: h.sessionID } });
    const limited = await h.getGoal();
    assert.equal(limited?.status, "usageLimited");
    assert.match(String(limited?.stopReason), /max duration reached/);
    assert.equal(h.promptTexts().filter((text) => /safety limit/i.test(text)).length, 1);
  } finally {
    await h.dispose();
  }
});

test("V1 reload persists counters and a pending attempt across an engine restart", async () => {
  const sandbox = createSandbox();
  const first = await createV1Harness({ engine, sandbox, pluginOptions: { min_continue_interval_seconds: 0 } });
  try {
    await first.callTool("create_goal", { objective: "persist across reload" });
    await first.emit({ type: "session.idle", properties: { sessionID: first.sessionID } });
    assert.equal(first.prompts.length, 1);
    assert.equal(first.readPersistedGoal()?.autoTurns, 1);
    assert.ok(first.readPersistedGoal()?.pendingAttempt, "pending attempt should be persisted");
  } finally {
    await first.dispose();
  }

  const second = await createV1Harness({ engine, sandbox, pluginOptions: { min_continue_interval_seconds: 0 } });
  try {
    assert.equal((await second.getGoal())?.autoTurns, 1);
    assert.ok(second.readPersistedGoal()?.pendingAttempt, "pending attempt should survive reload");

    await second.emit(progressEvent(second.sessionID, "m2", "progress after reload"));
    await second.emit({ type: "session.idle", properties: { sessionID: second.sessionID } });
    assert.equal(second.prompts.length, 1);
    assert.equal((await second.getGoal())?.autoTurns, 2);
  } finally {
    await second.dispose();
  }
});

test("V1 compaction preserves the goal snapshot and suppresses generic autocontinue", async () => {
  const h = await v1();
  try {
    const withoutGoal = { context: [] as string[] };
    await h.hooks["experimental.session.compacting"]({ sessionID: "ses_without_goal" }, withoutGoal);
    assert.equal(withoutGoal.context.length, 0);

    await h.callTool("create_goal", { objective: "survive compaction" });
    const compacting = { context: [] as string[] };
    await h.hooks["experimental.session.compacting"]({ sessionID: h.sessionID }, compacting);
    assert.equal(compacting.context.length, 1);
    assert.match(compacting.context[0]!, /<goal_snapshot>/);
    assert.match(compacting.context[0]!, /survive compaction/);

    const autocontinue = { enabled: true };
    await h.hooks["experimental.compaction.autocontinue"]({ sessionID: h.sessionID }, autocontinue);
    assert.equal(autocontinue.enabled, false);
  } finally {
    await h.dispose();
  }
});

test("V1 registers goal commands and pause_goal commits a durable pause", async () => {
  const h = await v1({ min_continue_interval_seconds: 0 });
  try {
    await h.callTool("create_goal", { objective: "pause via command" });

    const config: AnyRecord = {};
    await h.hooks.config(config);
    assert.ok(config.command.goal, "goal command must be registered");
    assert.ok(config.command.pause_goal, "pause_goal command must be registered");
    assert.ok(config.command.resume_goal, "resume_goal command must be registered");

    const template: string = config.command.pause_goal.template;
    await h.hooks["command.execute.before"](
      { command: "pause_goal", sessionID: h.sessionID },
      { parts: [{ type: "text", text: template }] },
    );
    assert.equal((await h.getGoal())?.status, "paused");
  } finally {
    await h.dispose();
  }
});

test("V2 registers commands/tools, pauses plan-agent goals, and refuses plan resume", async () => {
  const h = await v2();
  try {
    assert.ok(h.tools.has("create_goal"));
    assert.ok(h.tools.has("get_goal"));
    assert.equal(h.tools.size, 9);
    assert.ok(h.commands.has("goal"));
    assert.ok(h.commands.has("pause_goal"));
    assert.ok(h.commands.has("resume_goal"));

    const created = JSON.parse(await h.callTool("create_goal", { objective: "v2 plan" }, { agent: "plan" }));
    assert.equal(created.goal.status, "paused");
    assert.match(created.plan_mode_notice, /Plan mode/);
    await assert.rejects(() => h.callTool("update_goal_status", { status: "active" }, { agent: "plan" }), /Plan mode/);

    const contextHook = h.sessionHooks.get("session.context");
    assert.ok(contextHook, "V2 must register a session context hook");
    const sessionContext = { system: [] as Array<{ type: string; text: string }> };
    contextHook!({ system: sessionContext.system });
    assert.match(sessionContext.system[0]!.text, /OpenCode goal mode policy/);
  } finally {
    await h.dispose();
  }
});

test("V1 fast-turn gap vs V2: only V2 schedules a wakeup inside minimum interval", async (t) => {
  // Confirmed upstream defect: V1's reserveContinuation returns null when a
  // continuation finishes inside min_continue_interval_seconds, and V1 returns
  // without scheduling a later wakeup. V2 schedules one. This is a bounded
  // diagnostic of the real engine, not a claimed pass.
  const v1h = await v1({ min_continue_interval_seconds: 1 });
  try {
    await v1h.callTool("create_goal", { objective: "fast turn v1" });
    await v1h.emit({ type: "session.idle", properties: { sessionID: v1h.sessionID } });
    assert.equal(v1h.prompts.length, 1);

    // Pin the reservation to the current second so the interval condition is
    // deterministic (no second-boundary flake), then finish the turn "quickly".
    v1h.patchPersistedGoal({ lastContinuationAt: nowSeconds() });
    await v1h.emit(progressEvent(v1h.sessionID, "m1", "continuation finished quickly"));
    await v1h.emit({ type: "session.idle", properties: { sessionID: v1h.sessionID } });
    assert.equal(v1h.prompts.length, 1, "a second idle inside the interval must not re-reserve");

    await sleep(2200);
    const v1Count = v1h.prompts.length;
    t.diagnostic(
      `CONFIRMED UPSTREAM DEFECT (V1): a continuation completing inside min_continue_interval_seconds produced ` +
        `${v1Count} prompt(s) after the interval with no unrelated activity; V1 returned without scheduling a wakeup.`,
    );
    assert.equal(v1Count, 1);
  } finally {
    await v1h.dispose();
  }

  const v2h = await v2({ min_continue_interval_seconds: 1 });
  try {
    await v2h.callTool("create_goal", { objective: "fast turn v2" });

    v2h.push(...executionEvents(v2h.sessionID, "m1", "first turn", Date.now()));
    await v2h.settle();
    assert.equal(v2h.prompts.length, 1);

    // Same deterministic interval pin as the V1 branch.
    v2h.patchPersistedGoal({ lastContinuationAt: nowSeconds() });
    v2h.push(...executionEvents(v2h.sessionID, "m2", "continuation finished quickly", Date.now()));
    await v2h.settle();
    assert.equal(v2h.prompts.length, 1, "V2 also defers the immediate reserve");

    await sleep(2200);
    assert.equal(v2h.prompts.length, 2, "V2 schedules a later wakeup without unrelated activity");
  } finally {
    await v2h.dispose();
  }
});
