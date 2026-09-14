import assert from "node:assert/strict";
import { test } from "node:test";
import { SessionController, newMessageId, shouldSteer, steerPrompt } from "./session.ts";

function session(id: string, title: string) {
  return { id, projectID: "p", directory: "/x", title, version: "1", time: { created: 0, updated: 0 } };
}

function fakeClient(stored: { id: string; title: string }) {
  return {
    session: {
      get: async () => session(stored.id, stored.title),
      messages: async () => [],
      update: async (options: { body?: { title?: string } }) => {
        stored.title = options.body?.title ?? stored.title;
        return session(stored.id, stored.title);
      },
    },
    event: {
      subscribe: async () => ({ stream: (async function* () {})() }),
    },
  };
}

test("resume restores the stored session title", async () => {
  const stored = { id: "s1", title: "Preparation for deployment" };
  const controller = new SessionController({ client: fakeClient(stored) as never, cwd: "/x" });
  await controller.resume("s1");
  assert.equal(controller.title, "Preparation for deployment");
});

test("setTitle persists the title and keeps it on the controller", async () => {
  const stored = { id: "s2", title: "" };
  const controller = new SessionController({ client: fakeClient(stored) as never, cwd: "/x" });
  await controller.resume("s2");
  await controller.setTitle("Fix the parser bug");
  assert.equal(controller.title, "Fix the parser bug");
  assert.equal(stored.title, "Fix the parser bug");
});

test("reconnect swaps clients and reloads the same session", async () => {
  const stored = { id: "s3", title: "Before" };
  const controller = new SessionController({ client: fakeClient(stored) as never, cwd: "/x" });
  await controller.resume("s3");

  const reloaded = { id: "s3", title: "After restart" };
  let messagesCalls = 0;
  const next = {
    session: {
      get: async () => session(reloaded.id, reloaded.title),
      messages: async () => {
        messagesCalls += 1;
        return [];
      },
    },
    event: { subscribe: async () => ({ stream: (async function* () {})() }) },
  };
  await controller.reconnect({ client: next as never });

  assert.equal(controller.id, "s3", "the same session is kept across the restart");
  assert.equal(controller.title, "After restart", "the reloaded session comes from the new client");
  assert.equal(messagesCalls, 1);
});

test("resume falls back to an empty title when the session lookup fails", async () => {
  const client = {
    session: {
      get: async () => {
        throw new Error("not found");
      },
      messages: async () => [],
    },
    event: { subscribe: async () => ({ stream: (async function* () {})() }) },
  };
  const controller = new SessionController({ client: client as never, cwd: "/x" });
  await controller.resume("missing");
  assert.equal(controller.title, undefined);
});

/** Records v1 `promptAsync` and v2 `v2.session.prompt` calls for prompt tests. */
function promptFake(admit: "ok" | "undefined" | "throw" = "ok") {
  const v1: Array<{ path?: Record<string, unknown>; query?: Record<string, unknown>; body?: Record<string, unknown> }> = [];
  const v2: Array<Record<string, unknown>> = [];
  const client = {
    session: {
      get: async () => session("s1", "t"),
      messages: async () => [],
      promptAsync: async (options: { path?: Record<string, unknown>; query?: Record<string, unknown>; body?: Record<string, unknown> }) => {
        v1.push(options);
      },
    },
    event: { subscribe: async () => ({ stream: (async function* () {})() }) },
  };
  const clientV2 = {
    v2: {
      session: {
        prompt: async (options: Record<string, unknown>) => {
          v2.push(options);
          if (admit === "throw") throw new Error("v2 route unavailable");
          if (admit === "undefined") return undefined;
          return { data: { admittedSeq: 1 } };
        },
      },
    },
  };
  return { client, clientV2, v1, v2 };
}

test("generated message ids follow opencode's ascending message convention", () => {
  const ids = new Set<string>();
  for (let i = 0; i < 200; i++) {
    const id = newMessageId();
    assert.match(id, MESSAGE_ID_PATTERN, "the id is `msg_` + 12 hex + 14 base62");
    ids.add(id);
  }
  assert.equal(ids.size, 200, "ids stay unique across rapid generation");
});

test("shouldSteer treats only an idle session as a fresh turn", () => {
  assert.equal(shouldSteer("idle"), false);
  assert.equal(shouldSteer("busy"), true);
  assert.equal(shouldSteer("retry"), true);
});

/** A client whose event stream is fed by `send`, so event timing is deterministic. */
function eventClient() {
  const queue: unknown[] = [];
  const waiters: Array<() => void> = [];
  const stream = (async function* () {
    for (;;) {
      if (queue.length === 0) await new Promise<void>((resolve) => waiters.push(resolve));
      const next = queue.shift();
      if (next === undefined) return;
      yield next;
    }
  })();
  const promptBodies: Array<Record<string, unknown>> = [];
  const commandBodies: Array<Record<string, unknown>> = [];
  return {
    client: {
      session: {
        get: async () => session("s1", "t"),
        messages: async () => [],
        promptAsync: async (options: { body?: Record<string, unknown> }) => {
          promptBodies.push(options.body ?? {});
        },
        command: async (options: { body?: Record<string, unknown> }) => {
          commandBodies.push(options.body ?? {});
        },
        abort: async () => {},
      },
      event: { subscribe: async () => ({ stream }) },
    },
    promptBodies,
    commandBodies,
    send(event: unknown) {
      queue.push(event);
      waiters.shift()?.();
    },
  };
}

/** The exact message id Midas supplied for the last fresh prompt. */
function lastPromptId(promptBodies: Array<Record<string, unknown>>): string {
  const id = promptBodies.at(-1)?.messageID;
  assert.equal(typeof id, "string", "a fresh prompt supplies an exact message id");
  return id as string;
}

const MESSAGE_ID_PATTERN = /^msg_[0-9a-f]{12}[0-9A-Za-z]{14}$/;

const settle = async (): Promise<void> => {
  for (let i = 0; i < 5; i++) await new Promise((resolve) => setTimeout(resolve, 0));
};

const retryEvent = (message: string) => ({
  type: "session.status",
  properties: { sessionID: "s1", status: { type: "retry", attempt: 1, message, next: 100 } },
});

test("a retry caused by a dropped connection marks the session as reconnecting", async () => {
  const { client, send } = eventClient();
  const controller = new SessionController({ client: client as never, cwd: "/x" });
  await controller.resume("s1");
  send(retryEvent("TypeError: fetch failed"));
  await settle();
  assert.equal(controller.transcript.phase, "retry");
  assert.equal(controller.transcript.reconnecting, true);
});

test("a retry without a network cause stays a plain retry", async () => {
  const { client, send } = eventClient();
  const controller = new SessionController({ client: client as never, cwd: "/x" });
  await controller.resume("s1");
  send(retryEvent("Rate limit exceeded for model"));
  await settle();
  assert.equal(controller.transcript.phase, "retry");
  assert.equal(controller.transcript.reconnecting, false);
});

test("a network error mid-turn flips to reconnecting, and idle clears it", async () => {
  const { client, send } = eventClient();
  const controller = new SessionController({ client: client as never, cwd: "/x" });
  await controller.resume("s1");
  // The turn must be in flight for an error to mean "reconnecting".
  controller.transcript.setPhase("busy");
  send({
    type: "session.error",
    properties: {
      sessionID: "s1",
      error: { name: "APIError", data: { message: "Cannot connect to API: Unable to connect." } },
    },
  });
  await settle();
  assert.equal(controller.transcript.phase, "retry");
  assert.equal(controller.transcript.reconnecting, true);

  send({ type: "session.idle", properties: { sessionID: "s1" } });
  await settle();
  assert.equal(controller.transcript.phase, "idle");
  assert.equal(controller.transcript.reconnecting, false, "idle clears reconnecting");
});

test("steerPrompt maps attachments to v2 file attachments", () => {
  assert.deepEqual(steerPrompt("look", [{ mime: "image/png", filename: "a.png", url: "data:image/png;base64,AA" }]), {
    prompt: { text: "look", files: [{ uri: "data:image/png;base64,AA", name: "a.png" }] },
    delivery: "steer",
  });
  assert.deepEqual(steerPrompt("plain"), { prompt: { text: "plain" }, delivery: "steer" });
});

test("a busy prompt is admitted as a v2 steer with mapped files", async () => {
  const { client, clientV2, v1, v2 } = promptFake();
  const controller = new SessionController({ client: client as never, clientV2: clientV2 as never, cwd: "/x" });
  await controller.resume("s1");
  controller.transcript.setPhase("busy");
  await controller.prompt("steer me", [{ mime: "image/png", filename: "shot.png", url: "data:image/png;base64,AAAA" }]);
  assert.equal(v1.length, 0);
  assert.deepEqual(v2, [
    {
      sessionID: "s1",
      delivery: "steer",
      prompt: { text: "steer me", files: [{ uri: "data:image/png;base64,AAAA", name: "shot.png" }] },
    },
  ]);
});

test("an idle prompt keeps the v1 path with model, thinking variant and agent", async () => {
  const { client, clientV2, v1, v2 } = promptFake();
  const controller = new SessionController({ client: client as never, clientV2: clientV2 as never, cwd: "/x" });
  await controller.resume("s1");
  controller.setModel({ providerID: "anthropic", modelID: "claude-sonnet" });
  controller.setVariant("high");
  controller.setAgent("plan");
  await controller.prompt("hello");
  assert.equal(v2.length, 0);
  const body = v1[0]?.body ?? {};
  assert.match(String(body.messageID), MESSAGE_ID_PATTERN, "a supported exact message id is supplied");
  const { messageID: _id, ...rest } = body;
  assert.deepEqual(rest, {
    parts: [{ type: "text", text: "hello" }],
    model: { providerID: "anthropic", modelID: "claude-sonnet" },
    variant: "high",
    agent: "plan",
  });
  assert.deepEqual(v1[0]?.path, { id: "s1" });
  assert.deepEqual(v1[0]?.query, { directory: "/x" });
});

test("clearing the thinking variant omits it from a fresh prompt", async () => {
  const { client, v1 } = promptFake();
  const controller = new SessionController({ client: client as never, cwd: "/x" });
  await controller.resume("s1");
  controller.setVariant("high");
  controller.setVariant(undefined);
  await controller.prompt("hello");
  assert.equal(v1[0]?.body?.variant, undefined);
});

test("a busy prompt falls back to v1 when the v2 steer is rejected or throws", async () => {
  for (const admit of ["undefined", "throw"] as const) {
    const { client, clientV2, v1, v2 } = promptFake(admit);
    const controller = new SessionController({ client: client as never, clientV2: clientV2 as never, cwd: "/x" });
    await controller.resume("s1");
    controller.transcript.setPhase("busy");
    await controller.prompt("still delivered");
    assert.equal(v2.length, 1, `${admit}: v2 should be attempted`);
    assert.deepEqual(v1[0]?.body?.parts, [{ type: "text", text: "still delivered" }]);
  }
});

test("a busy prompt without a v2 client keeps the v1 path", async () => {
  const { client, v1 } = promptFake();
  const controller = new SessionController({ client: client as never, cwd: "/x" });
  await controller.resume("s1");
  controller.transcript.setPhase("busy");
  await controller.prompt("no v2 here");
  assert.deepEqual(v1[0]?.body?.parts, [{ type: "text", text: "no v2 here" }]);
});

test("addContext still appends without a reply and never steers", async () => {
  let pressed: Record<string, unknown> | undefined;
  const client = {
    session: {
      get: async () => session("s1", "t"),
      messages: async () => [],
      prompt: async (options: { body?: Record<string, unknown> }) => {
        pressed = options.body;
        return {};
      },
    },
    event: { subscribe: async () => ({ stream: (async function* () {})() }) },
  };
  const controller = new SessionController({ client: client as never, cwd: "/x" });
  await controller.resume("s1");
  await controller.addContext("imported context");
  assert.deepEqual(pressed, { parts: [{ type: "text", text: "imported context" }], noReply: true });
});

/** Records v2 question reply/reject calls and can fail either route. */
function questionFake(fail: "reply" | "reject" | "none" = "none") {
  const replies: Array<Record<string, unknown>> = [];
  const rejects: Array<Record<string, unknown>> = [];
  const clientV2 = {
    question: {
      reply: async (parameters: Record<string, unknown>) => {
        if (fail === "reply") throw new Error("reply route unavailable");
        replies.push(parameters);
      },
      reject: async (parameters: Record<string, unknown>) => {
        if (fail === "reject") throw new Error("reject route unavailable");
        rejects.push(parameters);
      },
    },
  };
  const client = { event: { subscribe: async () => ({ stream: (async function* () {})() }) } };
  return { client, clientV2, replies, rejects };
}

test("answering and rejecting a question call the v2 question routes", async () => {
  const { client, clientV2, replies, rejects } = questionFake();
  const controller = new SessionController({ client: client as never, clientV2: clientV2 as never, cwd: "/x" });
  controller.transcript.addQuestion({ id: "req-1", questions: [{ question: "Q?", options: [{ label: "A" }] }] });
  await controller.answerQuestion("req-1", [["Staging"]]);
  assert.deepEqual(replies, [{ requestID: "req-1", directory: "/x", answers: [["Staging"]] }]);
  assert.equal(controller.transcript.questions.length, 0);

  controller.transcript.addQuestion({ id: "req-2", questions: [{ question: "Q?", options: [{ label: "A" }] }] });
  await controller.rejectQuestion("req-2");
  assert.deepEqual(rejects, [{ requestID: "req-2", directory: "/x" }]);
  assert.equal(controller.transcript.questions.length, 0);
});

test("a failed question reply or reject leaves the prompt pending", async () => {
  for (const fail of ["reply", "reject"] as const) {
    const { client, clientV2 } = questionFake(fail);
    const controller = new SessionController({ client: client as never, clientV2: clientV2 as never, cwd: "/x" });
    controller.transcript.addQuestion({ id: "req-3", questions: [{ question: "Q?", options: [{ label: "A" }] }] });
    const call = fail === "reply" ? controller.answerQuestion("req-3", [["A"]]) : controller.rejectQuestion("req-3");
    await assert.rejects(call, new RegExp(`${fail} route unavailable`));
    assert.equal(controller.transcript.questions.length, 1, `${fail}: prompt should stay pending`);
  }
});

// --- explicit-turn completion correlation ----------------------------------

const userInfo = (id: string, sessionID = "s1") => ({ id, sessionID, role: "user", agent: "main", time: { created: 0 } });

const assistantInfo = (id: string, parentID: string, finish?: string, aborted = false) => ({
  id,
  sessionID: "s1",
  role: "assistant",
  parentID,
  mode: "main",
  providerID: "p",
  modelID: "m",
  cost: 0,
  tokens: { input: 0, output: 0, reasoning: 0, cache: { read: 0, write: 0 } },
  time: { created: 0, completed: 0 },
  ...(finish ? { finish } : {}),
  ...(aborted ? { error: { name: "MessageAbortedError", data: { message: "aborted" } } } : {}),
});

const messageEvent = (info: unknown) => ({ type: "message.updated", properties: { info } });
const idleEvent = () => ({ type: "session.idle", properties: { sessionID: "s1" } });

test("consumeQueueCompletion proves only a terminal message for the explicit turn's own parent", async () => {
  const { client, send, promptBodies } = eventClient();
  const controller = new SessionController({ client: client as never, cwd: "/x" });
  await controller.resume("s1");
  await controller.prompt("go");
  assert.equal(controller.transcript.phase, "busy");
  const expected = lastPromptId(promptBodies);

  // A terminal for another parent (and a bare idle) proves nothing.
  send(messageEvent(assistantInfo("a-other", "u-other", "stop")));
  await settle();
  send(idleEvent());
  await settle();
  assert.equal(controller.consumeQueueCompletion(), false);

  // The exact supplied user echo admits the turn; a tool step is not terminal.
  send(messageEvent(userInfo(expected)));
  send(messageEvent(assistantInfo("a-step", expected, "tool-calls")));
  await settle();
  assert.equal(controller.consumeQueueCompletion(), false);

  // The matching terminal step completes, and is consumed exactly once.
  send(messageEvent(assistantInfo("a-final", expected, "stop")));
  await settle();
  assert.equal(controller.consumeQueueCompletion(), true);
  assert.equal(controller.consumeQueueCompletion(), false, "a completion is consumed once");
});

test("consumeQueueCompletion requires the session to be idle", async () => {
  const { client, send, promptBodies } = eventClient();
  const controller = new SessionController({ client: client as never, cwd: "/x" });
  await controller.resume("s1");
  await controller.prompt("go");
  const expected = lastPromptId(promptBodies);
  send(messageEvent(userInfo(expected)));
  send(messageEvent(assistantInfo("a-final", expected, "stop")));
  await settle();
  assert.equal(controller.transcript.phase, "busy");
  assert.equal(controller.consumeQueueCompletion(), false, "a terminal while busy is not a completion yet");

  send(idleEvent());
  await settle();
  assert.equal(controller.consumeQueueCompletion(), true, "the paired idle releases it");
});

test("an aborted assistant message fails the explicit turn closed", async () => {
  const { client, send, promptBodies } = eventClient();
  const controller = new SessionController({ client: client as never, cwd: "/x" });
  await controller.resume("s1");
  await controller.prompt("go");
  const expected = lastPromptId(promptBodies);
  send(messageEvent(userInfo(expected)));
  send(messageEvent(assistantInfo("a-abort", expected, undefined, true)));
  send(messageEvent(assistantInfo("a-final", expected, "stop")));
  send(idleEvent());
  await settle();
  assert.equal(controller.consumeQueueCompletion(), false, "an aborted turn can never prove completion");
});

test("a late or replayed old-user message never binds the fresh turn's identity", async () => {
  const { client, send, promptBodies } = eventClient();
  const controller = new SessionController({ client: client as never, cwd: "/x" });
  await controller.resume("s1");
  await controller.prompt("go");
  const expected = lastPromptId(promptBodies);
  controller.transcript.setPhase("idle");

  // An already-seen user update and a previously unseen delayed one both land
  // before the expected user echo. Neither may bind as the turn's identity, and
  // their already-completed parents must not release the queue.
  send(messageEvent(userInfo("u-old-seen")));
  send(messageEvent(userInfo("u-old-seen")));
  send(messageEvent(userInfo("u-old-delayed")));
  send(messageEvent(assistantInfo("a-old", "u-old-seen", "stop")));
  send(messageEvent(assistantInfo("a-delayed", "u-old-delayed", "stop")));
  await settle();
  assert.equal(controller.consumeQueueCompletion(), false, "old parents never release the queue");

  // Only the exact supplied id proves the current turn.
  send(messageEvent(userInfo(expected)));
  send(messageEvent(assistantInfo("a-final", expected, "stop")));
  await settle();
  assert.equal(controller.consumeQueueCompletion(), true);
});

test("a stale duplicate completion for an earlier submission never proves the newest", async () => {
  const { client, send, promptBodies } = eventClient();
  const controller = new SessionController({ client: client as never, cwd: "/x" });
  await controller.resume("s1");
  await controller.prompt("same text");
  const first = lastPromptId(promptBodies);
  controller.transcript.setPhase("idle");
  await controller.prompt("same text");
  const second = lastPromptId(promptBodies);
  assert.notEqual(first, second, "repeated text still gets a fresh exact identity");
  assert.match(second, MESSAGE_ID_PATTERN);
  controller.transcript.setPhase("idle");

  // A duplicate/replayed terminal for the OLDER submission proves nothing now.
  send(messageEvent(userInfo(first)));
  send(messageEvent(assistantInfo("a-first", first, "stop")));
  await settle();
  assert.equal(controller.consumeQueueCompletion(), false, "the stale submission cannot prove the newest turn");

  // Only the newest submission's own parent does.
  send(messageEvent(userInfo(second)));
  send(messageEvent(assistantInfo("a-second", second, "stop")));
  await settle();
  assert.equal(controller.consumeQueueCompletion(), true);
});

test("completion evidence from another session never proves the turn", async () => {
  const { client, send, promptBodies } = eventClient();
  const controller = new SessionController({ client: client as never, cwd: "/x" });
  await controller.resume("s1");
  await controller.prompt("go");
  const expected = lastPromptId(promptBodies);
  controller.transcript.setPhase("idle");

  send(messageEvent(userInfo(expected, "other-session")));
  send(messageEvent({ ...assistantInfo("a-other", expected, "stop"), sessionID: "other-session" }));
  await settle();
  assert.equal(controller.consumeQueueCompletion(), false, "wrong-session evidence is ignored");

  send(messageEvent(userInfo(expected)));
  send(messageEvent(assistantInfo("a-final", expected, "stop")));
  await settle();
  assert.equal(controller.consumeQueueCompletion(), true);
});

test("an abort acknowledgement for a previous session never idles a newly resumed one", async () => {
  let releaseAbort!: () => void;
  const client = {
    session: {
      get: async (options: { path: { id: string } }) => session(options.path.id, "t"),
      messages: async () => [],
      promptAsync: async () => {},
      abort: () =>
        new Promise<void>((resolve) => {
          releaseAbort = resolve;
        }),
    },
    event: { subscribe: async () => ({ stream: (async function* () {})() }) },
  };
  const controller = new SessionController({ client: client as never, cwd: "/x" });
  await controller.resume("s1");
  controller.transcript.setPhase("busy");

  // The abort is dispatched for s1 but its acknowledgement is still pending.
  const aborting = controller.abort();
  // The user switches to (and starts a live run on) s2 with no new prompt on it.
  await controller.resume("s2");
  controller.transcript.setPhase("busy");
  assert.equal(controller.id, "s2");

  releaseAbort();
  await aborting;
  assert.equal(controller.transcript.phase, "busy", "the s1 acknowledgement did not idle s2");
});

test("a reconnect invalidates an in-flight abort acknowledgement", async () => {
  let releaseAbort!: () => void;
  let current = "s1";
  const client = {
    session: {
      get: async () => session(current, "t"),
      messages: async () => [],
      promptAsync: async () => {},
      abort: () =>
        new Promise<void>((resolve) => {
          releaseAbort = resolve;
        }),
    },
    event: { subscribe: async () => ({ stream: (async function* () {})() }) },
  };
  const controller = new SessionController({ client: client as never, cwd: "/x" });
  await controller.resume("s1");
  controller.transcript.setPhase("busy");
  const aborting = controller.abort();
  // Reconnecting the same session reloads history: the run is no longer owned.
  await controller.reconnect({ client: client as never });
  controller.transcript.setPhase("busy");
  releaseAbort();
  await aborting;
  assert.equal(controller.transcript.phase, "busy", "the pre-reconnect ack did not idle the reloaded session");
});

test("a late abort acknowledgement does not reset a newer prompt to idle", async () => {
  let releaseAbort!: () => void;
  const gate = new Promise<void>((resolve) => {
    releaseAbort = resolve;
  });
  const client = {
    session: {
      get: async () => session("s1", "t"),
      messages: async () => [],
      promptAsync: async () => {},
      abort: () => gate,
    },
    event: { subscribe: async () => ({ stream: (async function* () {})() }) },
  };
  const controller = new SessionController({ client: client as never, cwd: "/x" });
  await controller.resume("s1");
  controller.transcript.setPhase("busy");
  const aborting = controller.abort();
  await controller.prompt("fresh");
  assert.equal(controller.transcript.phase, "busy");
  releaseAbort();
  await aborting;
  assert.equal(controller.transcript.phase, "busy", "the late acknowledgement did not reset the newer turn");
});

test("a resumed session's completed history never proves the explicit turn", async () => {
  const client = {
    session: {
      get: async () => session("s1", "t"),
      messages: async () => [
        { info: userInfo("u1"), parts: [] },
        { info: assistantInfo("a-final", "u1", "stop"), parts: [] },
      ],
    },
    event: { subscribe: async () => ({ stream: (async function* () {})() }) },
  };
  const controller = new SessionController({ client: client as never, cwd: "/x" });
  await controller.resume("s1");
  assert.equal(controller.consumeQueueCompletion(), false, "unknown ownership fails closed");
});
