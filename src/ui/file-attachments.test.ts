import assert from "node:assert/strict";
import { closeSync, ftruncateSync, mkdirSync, mkdtempSync, openSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { after, test } from "node:test";
import type { Editor, Terminal } from "@earendil-works/pi-tui";
import type { PromptAttachment } from "../lib/attachments.ts";
import { MAX_BATCH_FILES, MAX_TEXT_BYTES } from "../lib/attachments.ts";
import type { FileChip, FrozenFilePayload } from "./app.ts";

/**
 * Midas UI file-attachment tests. These drive the real editor, the real
 * `handleSubmit`/queue/steer paths and the real reader against a fake controller
 * and a fake terminal. They assert the exact bytes/text handed to
 * `controller.prompt`, and they round-trip drafts/queue state through isolated
 * temp config roots. Nothing here calls a model, an opencode server, the real
 * board or the user's clipboard.
 */

// Isolate every path the module graph could consult before it is imported, so
// no test can read or write a real config, cache, session, draft or goal file.
const sandbox = mkdtempSync(join(tmpdir(), "midas-file-attach-"));
const ISOLATED_ENV = [
  "HOME",
  "MIDAS_CONFIG_DIR",
  "PI_CONFIG_DIR",
  "XDG_CONFIG_HOME",
  "XDG_DATA_HOME",
  "XDG_CACHE_HOME",
  "XDG_STATE_HOME",
  "OPENCODE_GOAL_STATE_PATH",
  "MIDAS_NO_UPDATE",
] as const;
const savedEnv = new Map<string, string | undefined>();
for (const key of ISOLATED_ENV) savedEnv.set(key, process.env[key]);
process.env.HOME = join(sandbox, "home");
process.env.MIDAS_CONFIG_DIR = join(sandbox, "midas");
process.env.PI_CONFIG_DIR = join(sandbox, "pi");
process.env.XDG_CONFIG_HOME = join(sandbox, "config");
process.env.XDG_DATA_HOME = join(sandbox, "data");
process.env.XDG_CACHE_HOME = join(sandbox, "cache");
process.env.XDG_STATE_HOME = join(sandbox, "state");
process.env.OPENCODE_GOAL_STATE_PATH = join(sandbox, "goals.json");
process.env.MIDAS_NO_UPDATE = "1";

const { MidasApp, formatUntrustedFileSections, describeFileAttachmentError } = await import("./app.ts");
const { Transcript } = await import("../state/transcript.ts");
const { readSessionState } = await import("../lib/session-state.ts");
const { initTheme } = await import("../theme/theme.ts");
const { initTheme: initPiTheme } = await import("@earendil-works/pi-coding-agent");

initPiTheme(undefined, false);
initTheme(undefined);

const workDir = join(sandbox, "files");
mkdirSync(workDir, { recursive: true });

after(() => {
  for (const [key, value] of savedEnv) {
    if (value === undefined) delete process.env[key];
    else process.env[key] = value;
  }
  rmSync(sandbox, { recursive: true, force: true });
});

// --- fixtures -------------------------------------------------------------

class TestTerminal implements Terminal {
  columns = 120;
  rows = 40;
  kittyProtocolActive = false;
  started = false;

  start(): void { this.started = true; }
  stop(): void { this.started = false; }
  async drainInput(): Promise<void> {}
  write(): void {}
  moveBy(): void {}
  hideCursor(): void {}
  showCursor(): void {}
  clearLine(): void {}
  clearFromCursor(): void {}
  clearScreen(): void {}
  setTitle(): void {}
  setProgress(): void {}
}

interface PromptCall {
  text: string;
  attachments: PromptAttachment[];
}

interface DraftShape {
  text: string;
  attachments?: Array<{ marker: string; path: string }>;
  files?: FileChip[];
}

interface QueueShape {
  text: string;
  attachments: PromptAttachment[];
  files?: FileChip[];
  frozenFiles?: FrozenFilePayload[];
}

interface AppHarness {
  app: InstanceType<typeof MidasApp>;
  controller: Record<string, unknown>;
  transcript: InstanceType<typeof Transcript>;
  editor: Editor;
  promptCalls: PromptCall[];
  failures: string[];
  warnings: string[];
  internals: {
    queue: QueueShape[];
    activeAgent: string;
    currentDraft(): DraftShape;
    saveDraftNow(): void;
    restoreDraft(sessionId: string | undefined): void;
    persistSessionState(): void;
    restoreSessionShellState(sessionId: string | undefined): Promise<void>;
    maybeFlushQueue(): void;
    editQueued(index: number): void;
    steerTyped(text: string): void;
    steerQueued(): void;
    navigateEditorHistory(direction: -1 | 1): boolean;
    handleSubmit(text: string, images?: Array<{ marker: string; path: string }>, files?: FileChip[]): Promise<void>;
    preparePrompt(text: string): unknown;
  };
  close(): void;
}

function makeHarness(options: { busy?: boolean } = {}): AppHarness {
  const transcript = new Transcript();
  if (options.busy) transcript.setPhase("busy");
  const promptCalls: PromptCall[] = [];
  const controller: Record<string, unknown> = {
    id: "session-1",
    title: undefined,
    transcript,
    async prompt(text: string, attachments: PromptAttachment[] = []): Promise<void> {
      promptCalls.push({ text, attachments });
    },
    async runCommand(): Promise<void> {},
    async abort(): Promise<void> {},
    setCwd(): void {},
    setAgent(): void {},
    setModel(): void {},
    setVariant(): void {},
    dispose(): void {},
    async listAgents(): Promise<unknown[]> {
      return [];
    },
    async listModels(): Promise<unknown[]> {
      return [];
    },
    async listCommands(): Promise<unknown[]> {
      return [];
    },
    async mcpServerNames(): Promise<string[]> {
      return [];
    },
    async defaultModel(): Promise<undefined> {
      return undefined;
    },
  };
  const app = new MidasApp({
    controller: controller as never,
    cwd: workDir,
    settings: { tuiMode: "regular", voicePreload: false },
    agent: "main",
    model: { providerID: "test", modelID: "test-model", name: "Test Model", providerName: "Test" },
    terminal: new TestTerminal(),
  });
  const failures: string[] = [];
  const warnings: string[] = [];
  const raw = app as unknown as {
    editor: Editor;
    queue: QueueShape[];
    activeAgent: string;
    currentDraft(): DraftShape;
    saveDraftNow(): void;
    restoreDraft(id: string | undefined): void;
    persistSessionState(): void;
    restoreSessionShellState(id: string | undefined): Promise<void>;
    maybeFlushQueue(): void;
    editQueued(index: number): void;
    steerTyped(text: string): void;
    steerQueued(): void;
    navigateEditorHistory(direction: -1 | 1): boolean;
    handleSubmit(text: string, images?: Array<{ marker: string; path: string }>, files?: FileChip[]): Promise<void>;
    preparePrompt(text: string): unknown;
    maybeGenerateTitle(...args: unknown[]): void;
    fail(message: string): void;
    warn(message: string): void;
  };
  raw.maybeGenerateTitle = () => {};
  raw.fail = (message: string) => {
    failures.push(message);
  };
  raw.warn = (message: string) => {
    warnings.push(message);
  };
  return {
    app,
    controller,
    transcript,
    editor: raw.editor,
    promptCalls,
    failures,
    warnings,
    internals: {
      // A getter, because restoring session state reassigns `this.queue`.
      get queue(): QueueShape[] {
        return raw.queue;
      },
      activeAgent: raw.activeAgent,
      currentDraft: () => raw.currentDraft(),
      saveDraftNow: () => raw.saveDraftNow(),
      restoreDraft: (id) => raw.restoreDraft(id),
      persistSessionState: () => raw.persistSessionState(),
      restoreSessionShellState: (id) => raw.restoreSessionShellState(id),
      maybeFlushQueue: () => raw.maybeFlushQueue(),
      editQueued: (index) => raw.editQueued(index),
      steerTyped: (text) => raw.steerTyped(text),
      steerQueued: () => raw.steerQueued(),
      navigateEditorHistory: (direction) => raw.navigateEditorHistory(direction),
      handleSubmit: (text, images, files) => raw.handleSubmit(text, images, files),
      preparePrompt: (text) => raw.preparePrompt(text),
    },
    close(): void {
      try {
        app.quit();
      } catch {
        // teardown is best-effort in tests
      }
    },
  };
}

let fileCounter = 0;
/** Write a real temp file and return its absolute path. */
function makeFile(content: string | Buffer): string {
  const path = join(workDir, `file-${++fileCounter}${typeof content === "string" ? ".txt" : ".bin"}`);
  writeFileSync(path, content);
  return path;
}

/** A sparse file of exactly `size` bytes, without materializing it. */
function makeSparseFile(name: string, size: number): string {
  const path = join(workDir, name);
  const fd = openSync(path, "w");
  try {
    ftruncateSync(fd, size);
  } finally {
    closeSync(fd);
  }
  return path;
}

async function flush(): Promise<void> {
  for (let i = 0; i < 3; i++) await new Promise<void>((resolve) => setImmediate(resolve));
}

/** Deliver `text` as a terminal bracketed paste. */
function paste(editor: Editor, text: string): void {
  editor.handleInput(`\x1b[200~${text}\x1b[201~`);
}

/** Submit the current editor contents through the real onSubmit path. */
async function submit(editor: Editor): Promise<void> {
  editor.handleInput("\r");
  await flush();
}

function sectionFor(name: string, content: string): string {
  return `----- BEGIN UNTRUSTED FILE CONTENT: ${name} (text/plain, ${Buffer.byteLength(content, "utf8")} bytes) -----\n${content.replace(/\n$/, "")}\n----- END UNTRUSTED FILE CONTENT: ${name} -----`;
}

test("formatUntrustedFileSections keeps content exact and labels it untrusted", () => {
  const content = "a\tb  c\r\n日本語 🎉\n";
  const delivered = formatUntrustedFileSections("hi", [{ name: "notes.txt", mime: "text/plain", byteLength: 20, content }]);
  assert.ok(delivered.startsWith("hi\n\n"));
  assert.ok(delivered.includes(content), "content survives verbatim");
  assert.ok(delivered.includes("UNTRUSTED FILE CONTENT"));
  assert.equal(formatUntrustedFileSections("hi", []), "hi");
});

test("describeFileAttachmentError names the file and distinguishes causes", () => {
  assert.match(describeFileAttachmentError({ code: "unsupported-binary", message: "Binary files are not supported", path: "/x/a.pdf" }), /a\.pdf.*binary/i);
  assert.match(describeFileAttachmentError({ code: "missing", message: "File not found", path: "/x/a.txt" }), /a\.txt.*no longer exists/);
  assert.match(describeFileAttachmentError({ code: "directory", message: "Path is a directory", path: "/x/sub" }), /sub.*directory/);
  assert.match(describeFileAttachmentError({ code: "oversize", message: "File exceeds 1048576 bytes", path: "/x/big.txt" }), /big\.txt.*exceeds/);
  assert.match(describeFileAttachmentError({ code: "batch-too-many", message: "At most 8 files can be attached" }), /At most 8 files/);
});

// ---------------------------------------------------------------------------
// Paste -> chip
// ---------------------------------------------------------------------------

test("a path-only paste becomes a removable file chip; prose paste does not", () => {
  const h = makeHarness();
  try {
    const path = makeFile("hello");
    paste(h.editor, path);
    assert.equal(h.editor.getText(), `[File: ${path.split("/").pop()}] `);
    assert.deepEqual(h.editor.getFileAttachments().map((chip) => chip.path), [path]);

    // Two backspaces delete the separator and then the whole chip.
    h.editor.handleInput("\x7f");
    h.editor.handleInput("\x7f");
    assert.equal(h.editor.getText(), "");
    assert.deepEqual(h.editor.getFileAttachments(), []);

    // A sentence that merely mentions a path is left as ordinary text.
    paste(h.editor, `please read ${path} for me`);
    assert.equal(h.editor.getText(), `please read ${path} for me`);
    assert.deepEqual(h.editor.getFileAttachments(), []);
  } finally {
    h.close();
  }
});

test("multi-line path pastes each become their own chip", () => {
  const h = makeHarness();
  try {
    const a = makeFile("aaa");
    const b = makeFile("bbb");
    paste(h.editor, `${a}\n${b}`);
    assert.deepEqual(h.editor.getFileAttachments().map((chip) => chip.path), [a, b]);
  } finally {
    h.close();
  }
});

// ---------------------------------------------------------------------------
// Idle submit delivers content
// ---------------------------------------------------------------------------

test("idle submit delivers exact UTF-8 content in a labelled untrusted section", async () => {
  const h = makeHarness();
  try {
    const content = "line one\r\n\tindented  two\r\n日本語 — emoji 🎉\n";
    const path = makeFile(content);
    paste(h.editor, path);
    await submit(h.editor);

    assert.equal(h.promptCalls.length, 1);
    const name = path.split("/").pop()!;
    const delivered = h.promptCalls[0]!.text;
    assert.ok(delivered.includes(content), "the exact content (tabs, CRLF, Unicode, trailing newline) is present");
    assert.ok(delivered.includes(sectionFor(name, content)), "one labelled untrusted section is appended");
    assert.equal(delivered.split(content.replace(/\n$/, "")).length - 1, 1, "content is appended once");
    assert.deepEqual(h.promptCalls[0]!.attachments, []);
    assert.deepEqual(h.failures, []);
  } finally {
    h.close();
  }
});

test("double spaces, tabs and an empty file bypass prompt whitespace normalization", async () => {
  const h = makeHarness();
  try {
    const content = "a  b\tc\n\n";
    const path = makeFile(content);
    h.editor.insertFileAttachment(path);
    await submit(h.editor);
    assert.ok(h.promptCalls[0]!.text.includes(content), "inner whitespace is untouched");
  } finally {
    h.close();
  }
});

test("two same-basename files stay distinct and each content is delivered once", async () => {
  const h = makeHarness();
  try {
    const dirA = join(workDir, "a");
    const dirB = join(workDir, "b");
    mkdirSync(dirA, { recursive: true });
    mkdirSync(dirB, { recursive: true });
    const first = join(dirA, "report.txt");
    const second = join(dirB, "report.txt");
    writeFileSync(first, "FIRST-CONTENT");
    writeFileSync(second, "SECOND-CONTENT");

    h.editor.insertFileAttachment(first);
    h.editor.insertFileAttachment(second);
    const chips = h.editor.getFileAttachments();
    assert.equal(chips.length, 2);
    assert.notEqual(chips[0]!.marker, chips[1]!.marker, "distinct visible identities");
    assert.notEqual(chips[0]!.id, chips[1]!.id);

    await submit(h.editor);
    const delivered = h.promptCalls[0]!.text;
    assert.ok(delivered.includes("FIRST-CONTENT"));
    assert.ok(delivered.includes("SECOND-CONTENT"));
    assert.equal(delivered.split("FIRST-CONTENT").length - 1, 1);
    assert.equal(delivered.split("SECOND-CONTENT").length - 1, 1);
  } finally {
    h.close();
  }
});

test("deleting the second same-basename chip excludes only its payload", async () => {
  const h = makeHarness();
  try {
    const dirA = join(workDir, "c");
    const dirB = join(workDir, "d");
    mkdirSync(dirA, { recursive: true });
    mkdirSync(dirB, { recursive: true });
    const first = join(dirA, "same.md");
    const second = join(dirB, "same.md");
    writeFileSync(first, "KEEP-ME");
    writeFileSync(second, "DROP-ME");

    h.editor.insertFileAttachment(first);
    h.editor.insertFileAttachment(second);
    // Delete the separator and the second chip.
    h.editor.handleInput("\x7f");
    h.editor.handleInput("\x7f");
    await submit(h.editor);

    const delivered = h.promptCalls[0]!.text;
    assert.ok(delivered.includes("KEEP-ME"));
    assert.ok(!delivered.includes("DROP-ME"), "the deleted chip's payload is never read or sent");
  } finally {
    h.close();
  }
});

test("undo after deleting a chip restores its payload, not an inert label", async () => {
  const h = makeHarness();
  try {
    const first = makeFile("ONE");
    const second = makeFile("TWO");
    h.editor.insertFileAttachment(first);
    h.editor.insertFileAttachment(second);
    // Delete the separator and the second chip, then undo it back.
    h.editor.handleInput("\x7f");
    h.editor.handleInput("\x7f");
    assert.deepEqual(h.editor.getFileAttachments().map((chip) => chip.path), [first]);

    h.editor.handleInput("\x1f");
    assert.deepEqual(h.editor.getFileAttachments().map((chip) => chip.path), [first, second]);

    await submit(h.editor);
    const delivered = h.promptCalls[0]!.text;
    assert.ok(delivered.includes("ONE"));
    assert.ok(delivered.includes("TWO"), "the restored chip submits its file, not a bare label");
  } finally {
    h.close();
  }
});

// ---------------------------------------------------------------------------
// Image regression
// ---------------------------------------------------------------------------

test("image chips keep their exact data URLs and add no file section", async () => {
  const h = makeHarness();
  try {
    const bytes = Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x01, 0x02, 0xff]);
    const image = join(workDir, "shot.png");
    writeFileSync(image, bytes);
    h.editor.insertImageAttachment(image);
    await submit(h.editor);

    assert.equal(h.promptCalls[0]!.text, "[Image: shot.png]");
    assert.equal(h.promptCalls[0]!.attachments.length, 1);
    assert.equal(h.promptCalls[0]!.attachments[0]!.mime, "image/png");
    assert.equal(h.promptCalls[0]!.attachments[0]!.filename, "shot.png");
    const base64 = h.promptCalls[0]!.attachments[0]!.url.slice("data:image/png;base64,".length);
    assert.deepEqual(Buffer.from(base64, "base64"), bytes);
    assert.ok(!h.promptCalls[0]!.text.includes("UNTRUSTED FILE CONTENT"));
  } finally {
    h.close();
  }
});

// ---------------------------------------------------------------------------
// Explicit selection only
// ---------------------------------------------------------------------------

test("typed lookalikes and prose paths never read a file", async () => {
  const h = makeHarness();
  try {
    const secret = makeFile("SECRET-CONTENT");
    const name = secret.split("/").pop()!;
    for (const char of `[File: ${name}]`) h.editor.handleInput(char);
    await submit(h.editor);
    assert.equal(h.promptCalls[0]!.text, `[File: ${name}]`);
    assert.ok(!h.promptCalls[0]!.text.includes("SECRET-CONTENT"));

    // A path mentioned inside prose is not an attachment.
    h.editor.setText(`summarize ${secret}`);
    await submit(h.editor);
    assert.equal(h.promptCalls[1]!.text, `summarize ${secret}`);
    assert.ok(!h.promptCalls[1]!.text.includes("SECRET-CONTENT"));
  } finally {
    h.close();
  }
});

test("a pasted unknown marker never triggers a read", async () => {
  const h = makeHarness();
  try {
    makeFile("UNKNOWN-CONTENT");
    paste(h.editor, "[File: does-not-exist.txt]");
    assert.deepEqual(h.editor.getFileAttachments(), []);
    await submit(h.editor);
    assert.equal(h.promptCalls[0]!.text, "[File: does-not-exist.txt]");
    assert.ok(!h.promptCalls[0]!.text.includes("UNKNOWN-CONTENT"));
  } finally {
    h.close();
  }
});

// ---------------------------------------------------------------------------
// Errors stay recoverable
// ---------------------------------------------------------------------------

test("unsupported, missing, directory and oversize files fail specifically and keep the chip", async (t) => {
  const cases: Array<{ name: string; path: string; pattern: RegExp }> = [];
  const pdf = join(workDir, "doc.pdf");
  writeFileSync(pdf, "%PDF-1.4\n1 0 obj\n<<>>\nendobj\n%%EOF\n", "utf8");
  cases.push({ name: "pdf", path: pdf, pattern: /doc\.pdf.*binary/i });

  cases.push({ name: "missing", path: join(workDir, "nope.txt"), pattern: /nope\.txt.*no longer exists/ });

  const dir = join(workDir, "subdir");
  mkdirSync(dir, { recursive: true });
  cases.push({ name: "directory", path: dir, pattern: /subdir.*directory/ });

  cases.push({ name: "oversize", path: makeSparseFile("big.txt", MAX_TEXT_BYTES + 1), pattern: /big\.txt.*exceeds/ });

  for (const entry of cases) {
    const h = makeHarness();
    try {
      h.editor.insertFileAttachment(entry.path);
      const marker = h.editor.getText().trim();
      await submit(h.editor);
      assert.equal(h.promptCalls.length, 0, `${entry.name}: nothing is sent`);
      assert.equal(h.failures.length, 1, `${entry.name}: one specific error`);
      assert.match(h.failures[0]!, entry.pattern);
      assert.ok(h.editor.getText().includes(marker), `${entry.name}: the chip is restored and recoverable`);
      assert.equal(h.editor.getFileAttachments().length, 1, `${entry.name}: the chip keeps its payload`);
    } finally {
      h.close();
    }
    t.diagnostic(`checked ${entry.name}`);
  }
});

test("too many file chips fail before any read", async () => {
  const h = makeHarness();
  try {
    for (let i = 0; i <= MAX_BATCH_FILES; i++) h.editor.insertFileAttachment(makeFile(`f${i}`));
    await submit(h.editor);
    assert.equal(h.promptCalls.length, 0);
    assert.equal(h.failures.length, 1);
    assert.match(h.failures[0]!, /at most 8 files/i);
  } finally {
    h.close();
  }
});

// ---------------------------------------------------------------------------
// Queue: frozen, edit, reorder
// ---------------------------------------------------------------------------

test("a queued file prompt freezes its content and never re-reads a changed file", async () => {
  const h = makeHarness({ busy: true });
  try {
    const path = makeFile("ORIGINAL-CONTENT");
    h.editor.insertFileAttachment(path);
    await submit(h.editor);
    assert.equal(h.promptCalls.length, 0, "busy input queues instead of sending");
    assert.equal(h.internals.queue.length, 1);

    // The file changes on disk before the queue drains.
    writeFileSync(path, "CHANGED-CONTENT");
    h.transcript.setPhase("idle");
    h.internals.maybeFlushQueue();
    await flush();

    assert.equal(h.promptCalls.length, 1);
    assert.ok(h.promptCalls[0]!.text.includes("ORIGINAL-CONTENT"));
    assert.ok(!h.promptCalls[0]!.text.includes("CHANGED-CONTENT"));
    assert.equal(h.internals.queue.length, 0);
  } finally {
    h.close();
  }
});

test("editing a queued file prompt neither duplicates content nor re-reads the file", async () => {
  const h = makeHarness({ busy: true });
  try {
    const path = makeFile("FROZEN-BODY");
    h.editor.insertFileAttachment(path);
    await submit(h.editor);
    assert.equal(h.internals.queue.length, 1);

    // Editing restores only the visible chip text, never the inlined content.
    h.internals.editQueued(0);
    const visible = h.editor.getText();
    assert.ok(visible.includes("[File:"));
    assert.ok(!visible.includes("FROZEN-BODY"), "the frozen content is not put back in the editor");
    assert.equal(h.editor.getFileAttachments().length, 1);

    // Re-submit unchanged; it requeues at the same slot with the frozen payload.
    writeFileSync(path, "LATER-BODY");
    await submit(h.editor);
    assert.equal(h.internals.queue.length, 1);
    assert.equal(h.internals.queue[0]!.frozenFiles?.[0]?.content, "FROZEN-BODY");

    h.transcript.setPhase("idle");
    h.internals.maybeFlushQueue();
    await flush();
    const delivered = h.promptCalls[0]!.text;
    assert.equal(delivered.split("FROZEN-BODY").length - 1, 1, "content is appended exactly once");
    assert.ok(!delivered.includes("LATER-BODY"));
  } finally {
    h.close();
  }
});

test("queue state round-trips through isolated roots and rehydrates on flush", async () => {
  const h = makeHarness({ busy: true });
  try {
    const path = makeFile("PERSISTED-BODY");
    h.editor.insertFileAttachment(path);
    await submit(h.editor);
    const persisted = readSessionState("session-1");
    assert.equal(persisted?.queue?.length, 1);
    assert.equal(persisted?.queue?.[0]?.files?.length, 1);
    assert.equal(persisted?.queue?.[0]?.frozenFiles?.[0]?.content, "PERSISTED-BODY");

    // Simulate a resume: drop the in-memory queue, restore it from disk.
    h.internals.queue.length = 0;
    await h.internals.restoreSessionShellState("session-1");
    assert.equal(h.internals.queue.length, 1);
    h.transcript.setPhase("idle");
    h.internals.maybeFlushQueue();
    await flush();
    assert.ok(h.promptCalls[0]!.text.includes("PERSISTED-BODY"));
  } finally {
    h.close();
  }
});

test("re-queuing an edited follow-up reorders without duplicating its payload", async () => {
  const h = makeHarness({ busy: true });
  try {
    const path = makeFile("REORDER-BODY");
    h.editor.insertFileAttachment(path);
    await submit(h.editor);
    // A second, plain follow-up behind it.
    h.editor.setText("second follow-up");
    await submit(h.editor);
    assert.equal(h.internals.queue.length, 2);

    h.internals.editQueued(0);
    // Type the edit so the chip mapping stays live (a wholesale setText would
    // clear it, exactly as the vendor editor documents).
    for (const char of " (edited)") h.editor.handleInput(char);
    await submit(h.editor);
    assert.equal(h.internals.queue.length, 2);
    assert.match(h.internals.queue[0]!.text, /edited/);
    assert.equal(h.internals.queue[0]!.frozenFiles?.[0]?.content, "REORDER-BODY");

    h.transcript.setPhase("idle");
    h.internals.maybeFlushQueue();
    await flush();
    assert.equal(h.promptCalls[0]!.text.split("REORDER-BODY").length - 1, 1);
  } finally {
    h.close();
  }
});

// ---------------------------------------------------------------------------
// Steer
// ---------------------------------------------------------------------------

test("steering typed input delivers the exact file content", async () => {
  const h = makeHarness({ busy: true });
  try {
    const path = makeFile("STEER-BODY");
    h.editor.insertFileAttachment(path);
    h.internals.steerTyped(h.editor.getExpandedText());
    await flush();

    assert.equal(h.promptCalls.length, 1);
    assert.ok(h.promptCalls[0]!.text.includes("STEER-BODY"));
    assert.ok(h.promptCalls[0]!.text.includes("UNTRUSTED FILE CONTENT"));
    assert.equal(h.internals.queue.length, 0);
  } finally {
    h.close();
  }
});

test("steering the top queued file prompt uses its frozen payload", async () => {
  const h = makeHarness({ busy: true });
  try {
    const path = makeFile("QUEUED-STEER");
    h.editor.insertFileAttachment(path);
    await submit(h.editor);
    writeFileSync(path, "AFTER-QUEUE");
    h.internals.steerQueued();
    await flush();

    assert.equal(h.promptCalls.length, 1);
    assert.ok(h.promptCalls[0]!.text.includes("QUEUED-STEER"));
    assert.ok(!h.promptCalls[0]!.text.includes("AFTER-QUEUE"));
  } finally {
    h.close();
  }
});

// ---------------------------------------------------------------------------
// Drafts and history recall
// ---------------------------------------------------------------------------

test("a saved draft restores its file chip and still submits the content", async () => {
  const path = makeFile("DRAFT-BODY");
  const first = makeHarness();
  try {
    first.editor.insertFileAttachment(path);
    first.editor.insertTextAtCursor("summarize this");
    const draft = first.internals.currentDraft();
    assert.equal(draft.files?.length, 1, "the draft snapshot carries the file chip");
    first.internals.saveDraftNow();
  } finally {
    first.close();
  }

  // A second app with the same session id models a resume.
  const resumed = makeHarness();
  try {
    resumed.internals.restoreDraft("session-1");
    assert.ok(resumed.editor.getText().includes("summarize this"));
    assert.deepEqual(resumed.editor.getFileAttachments().map((chip) => chip.path), [path]);

    await submit(resumed.editor);
    assert.ok(resumed.promptCalls[0]!.text.includes("DRAFT-BODY"));
  } finally {
    resumed.close();
  }
});

test("recalling a submitted prompt from history revives its file chip", async () => {
  const h = makeHarness();
  try {
    const path = makeFile("HISTORY-BODY");
    h.editor.insertFileAttachment(path);
    await submit(h.editor);
    assert.deepEqual(h.editor.getFileAttachments(), [], "the empty input reports no chips");

    // Up arrow recalls the submitted message; its chip must come back active.
    assert.equal(h.internals.navigateEditorHistory(-1), true);
    assert.ok(h.editor.getText().includes("[File:"));
    assert.equal(h.editor.getFileAttachments().length, 1, "the recalled label is a live chip");
  } finally {
    h.close();
  }
});
