import assert from "node:assert/strict";
import { test } from "node:test";
import { EventEmitter } from "node:events";
import { PassThrough } from "node:stream";
import { VoiceController, composeVoiceText, parseSttLine } from "./stt.ts";

test("parseSttLine accepts protocol events and ignores junk", () => {
  assert.deepEqual(parseSttLine('{"type":"partial","text":"hi"}'), { type: "partial", text: "hi", message: undefined });
  assert.deepEqual(parseSttLine('{"type":"ready"}'), { type: "ready", text: undefined, message: undefined });
  assert.deepEqual(parseSttLine('{"type":"listening"}'), { type: "listening", text: undefined, message: undefined });
  assert.deepEqual(parseSttLine('{"type":"paused"}'), { type: "paused", text: undefined, message: undefined });
  assert.deepEqual(parseSttLine('{"type":"error","message":"boom"}'), { type: "error", text: undefined, message: "boom" });
  assert.equal(parseSttLine("not json"), undefined);
  assert.equal(parseSttLine('{"type":"other"}'), undefined);
  assert.equal(parseSttLine(""), undefined);
});

test("composeVoiceText joins base, committed and partial", () => {
  assert.equal(composeVoiceText("base ", "hello ", "wor"), "base hello wor");
});

/** A fake helper with an stdin that records every command Midas sends. */
function fakeChild(): {
  child: EventEmitter & { stdout: PassThrough; stdin: PassThrough; kill(): void };
  stdout: PassThrough;
  written: string[];
  state: { kills: number };
} {
  const stdout = new PassThrough();
  const stdin = new PassThrough();
  const written: string[] = [];
  stdin.on("data", (chunk) => { written.push(chunk.toString()); });
  const state = { kills: 0 };
  const child = Object.assign(new EventEmitter(), {
    stdout,
    stdin,
    kill: () => { state.kills += 1; },
  });
  return { child, stdout, written, state };
}

const sentCommands = (written: string[]): string[] =>
  written.join("").trim().split("\n").filter(Boolean).map((line) => JSON.parse(line).type as string);

test("VoiceController preloads, then listens and pauses with the model warm", () => {
  const { child, stdout, written, state } = fakeChild();
  const texts: Array<[string, string]> = [];
  let ready = 0;
  const controller = new VoiceController({
    command: "fake",
    spawn: () => child as never,
    onText: (committed, partial) => texts.push([committed, partial]),
    onError: () => {},
    onReady: () => { ready += 1; },
  });

  controller.preload();
  assert.deepEqual(sentCommands(written), [], "preload must not open the microphone");
  assert.equal(controller.ready, false);

  stdout.write('{"type":"ready"}\n');
  assert.equal(controller.ready, true);
  assert.equal(ready, 1);

  controller.listen();
  assert.deepEqual(sentCommands(written), ["listen"]);
  stdout.write('{"type":"partial","text":"hel"}\n');
  stdout.write('{"type":"final","text":"hello "}\n');
  stdout.write('{"type":"partial","text":"wor"}\n');
  assert.deepEqual(texts, [["", "hel"], ["hello ", ""], ["hello ", "wor"]]);

  controller.pause();
  assert.deepEqual(sentCommands(written), ["listen", "pause"]);
  stdout.write('{"type":"partial","text":"ignored"}\n');
  assert.equal(texts.length, 3, "text after pause is not surfaced");

  controller.listen();
  assert.deepEqual(sentCommands(written), ["listen", "pause", "listen"]);
  // A fresh listening session restarts the transcript so re-entry never duplicates.
  stdout.write('{"type":"partial","text":"again"}\n');
  assert.deepEqual(texts[3], ["", "again"]);

  controller.stop();
  assert.equal(state.kills, 1);
  assert.deepEqual(sentCommands(written), ["listen", "pause", "listen", "stop"]);
});

test("a silent gap does not wipe the in-progress transcript", () => {
  const { child, stdout } = fakeChild();
  const texts: Array<[string, string]> = [];
  const controller = new VoiceController({
    command: "fake",
    spawn: () => child as never,
    onText: (committed, partial) => texts.push([committed, partial]),
    onError: () => {},
  });
  controller.listen();
  stdout.write('{"type":"partial","text":"hello world"}\n');
  // During a noisy pause the streaming model drops its un-finalized tail and
  // reports an empty partial; that must not erase what was already recognised.
  stdout.write('{"type":"partial","text":""}\n');
  assert.deepEqual(texts.at(-1), ["", "hello world"], "empty partial cleared the transcript");
  // Real speech after the gap still replaces the transcript normally.
  stdout.write('{"type":"partial","text":"hello world again"}\n');
  assert.deepEqual(texts.at(-1), ["", "hello world again"]);
  controller.stop();
});

test("VoiceController reports ready once, on the ready event or first text", () => {
  const { child, stdout } = fakeChild();
  let ready = 0;
  const controller = new VoiceController({
    command: "fake",
    spawn: () => child as never,
    onText: () => {},
    onError: () => {},
    onReady: () => { ready += 1; },
  });
  controller.listen();
  assert.equal(ready, 0, "still loading before the helper signals");
  stdout.write('{"type":"ready"}\n');
  assert.equal(ready, 1);
  stdout.write('{"type":"partial","text":"hi"}\n');
  assert.equal(ready, 1, "text after ready does not fire it again");
  controller.stop();

  const second = fakeChild();
  let implicit = 0;
  const implicitController = new VoiceController({
    command: "fake",
    spawn: () => second.child as never,
    onText: () => {},
    onError: () => {},
    onReady: () => { implicit += 1; },
  });
  implicitController.listen();
  second.stdout.write('{"type":"partial","text":"hi"}\n');
  assert.equal(implicit, 1, "first text implies the helper is listening");
  implicitController.stop();
});

test("VoiceController stops and reports on a helper error", () => {
  const { child, stdout, state } = fakeChild();
  const errors: string[] = [];
  const controller = new VoiceController({
    command: "fake",
    spawn: () => child as never,
    onText: () => {},
    onError: (message) => errors.push(message),
  });
  controller.listen();
  stdout.write('{"type":"error","message":"mic denied"}\n');
  assert.deepEqual(errors, ["mic denied"]);
  assert.equal(state.kills, 1);
  controller.stop();
  assert.equal(state.kills, 1, "stop is idempotent");
});
