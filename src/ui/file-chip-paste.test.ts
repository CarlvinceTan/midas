import test from "node:test";
import assert from "node:assert/strict";
// Import the worktree copy directly: `@earendil-works/pi-tui` may resolve through
// node_modules to another checkout's vendor tree, so a package import would not
// see edits made here.
import { Editor } from "../../vendor/pi-tui/dist/components/editor.js";
import type { EditorFileAttachment } from "../../vendor/pi-tui/dist/components/editor.js";

const tui = {
  terminal: { rows: 40, columns: 80 },
  requestRender(): void {},
  setFocus(): void {},
  addInputListener(): () => void {
    return () => {};
  },
} as never;
const theme = {
  borderColor: (text: string): string => text,
  selectList: {
    selectedText: (text: string): string => text,
    text: (text: string): string => text,
    muted: (text: string): string => text,
    selectedBg: (text: string): string => text,
  },
} as never;

const yellow = (lines: string[]): boolean => lines.some((line) => line.includes("\x1b[33m"));

/** Deliver `text` as a bracketed paste, the path used by terminal paste. */
const paste = (editor: Editor, text: string): void => {
  editor.handleInput(`\x1b[200~${text}\x1b[201~`);
};

test("programmatic file chips render yellow, are atomic, and delete as a unit", () => {
  const editor = new Editor(tui, theme, { paddingX: 0 });
  editor.insertFileAttachment("/tmp/My Report.pdf");
  // A plain space follows the chip so text typed next doesn't glue onto it.
  assert.equal(editor.getText(), "[File: My Report.pdf] ");
  assert.deepEqual(editor.getFileAttachments(), [
    { marker: "[File: My Report.pdf]", path: "/tmp/My Report.pdf", id: "file-1", name: "My Report.pdf" },
  ]);
  assert.ok(yellow(editor.render(60)));
  editor.handleInput("\x7f");
  assert.equal(editor.getText(), "[File: My Report.pdf]");
  editor.handleInput("\x7f");
  assert.equal(editor.getText(), "");
  assert.deepEqual(editor.getFileAttachments(), []);
});

test("file chips navigate as one unit in both directions", () => {
  const editor = new Editor(tui, theme, { paddingX: 0 });
  editor.insertFileAttachment("/tmp/a.txt");
  editor.handleInput("x");
  assert.equal(editor.getText(), "[File: a.txt] x");
  // Move over "x" and the separator space, landing right after the chip.
  editor.handleInput("\x1b[D");
  editor.handleInput("\x1b[D");
  assert.equal(editor.getCursor().col, 13);
  // One more step hops the whole chip instead of a single character.
  editor.handleInput("\x1b[D");
  assert.equal(editor.getCursor().col, 0);
  editor.handleInput("\x1b[C");
  assert.equal(editor.getCursor().col, 13);
});

test("same-basename files from different directories keep distinct chips", () => {
  const editor = new Editor(tui, theme, { paddingX: 0 });
  editor.insertFileAttachment("/tmp/a/report.pdf");
  editor.insertFileAttachment("/tmp/b/report.pdf");
  assert.equal(editor.getText(), "[File: report.pdf] [File: report.pdf (2)] ");

  const attachments = editor.getFileAttachments();
  assert.deepEqual(
    attachments.map((attachment) => attachment.path),
    ["/tmp/a/report.pdf", "/tmp/b/report.pdf"],
  );
  assert.notEqual(attachments[0]?.id, attachments[1]?.id, "identities distinguish duplicate names");

  // Deleting the second chip must remove only its own payload.
  editor.handleInput("\x7f");
  editor.handleInput("\x7f");
  assert.equal(editor.getText(), "[File: report.pdf] ");
  assert.deepEqual(editor.getFileAttachments(), [
    { marker: "[File: report.pdf]", path: "/tmp/a/report.pdf", id: attachments[0]?.id, name: "report.pdf" },
  ]);
});

test("removing a file chip and undoing restores only that chip", () => {
  const editor = new Editor(tui, theme, { paddingX: 0 });
  editor.insertFileAttachment("/tmp/a/report.pdf");
  editor.insertFileAttachment("/tmp/b/report.pdf");
  assert.equal(editor.getFileAttachments().length, 2);

  // Delete the separator and the second chip.
  editor.handleInput("\x7f");
  editor.handleInput("\x7f");
  assert.deepEqual(
    editor.getFileAttachments().map((attachment) => attachment.path),
    ["/tmp/a/report.pdf"],
  );

  // Undo restores the deleted chip and its payload, alongside the first.
  editor.handleInput("\x1f");
  assert.deepEqual(
    editor.getFileAttachments().map((attachment) => attachment.path),
    ["/tmp/a/report.pdf", "/tmp/b/report.pdf"],
  );
});

test("file chips restored from a saved draft become active again", () => {
  const editor = new Editor(tui, theme, { paddingX: 0 });
  editor.setText("[File: draft.pdf]");
  editor.setFileAttachments([
    { marker: "[File: draft.pdf]", path: "/tmp/draft.pdf", id: "file-9", name: "draft.pdf" },
  ]);
  assert.deepEqual(editor.getFileAttachments(), [
    { marker: "[File: draft.pdf]", path: "/tmp/draft.pdf", id: "file-9", name: "draft.pdf" },
  ]);
  assert.ok(yellow(editor.render(60)));

  // Legacy `{ marker, path }` entries remain accepted; optional fields fill in.
  const legacy = new Editor(tui, theme, { paddingX: 0 });
  legacy.setText("[File: legacy.pdf]");
  legacy.setFileAttachments([{ marker: "[File: legacy.pdf]", path: "/tmp/legacy.pdf" }]);
  const [restored] = legacy.getFileAttachments();
  assert.equal(restored?.path, "/tmp/legacy.pdf");
  assert.equal(restored?.name, "legacy.pdf");
  assert.ok(restored?.id);
});

test("a chip copied from the transcript and pasted back re-attaches its file", () => {
  const editor = new Editor(tui, theme, { paddingX: 0 });
  editor.insertFileAttachment("/tmp/copied report.pdf");
  const [before] = editor.getFileAttachments();
  // The host clears the input after submit; session memory must survive it.
  editor.setText("");
  assert.deepEqual(editor.getFileAttachments(), []);

  paste(editor, "[File: copied report.pdf]");
  assert.equal(editor.getText(), "[File: copied report.pdf]");
  assert.deepEqual(editor.getFileAttachments(), [before]);
  assert.ok(yellow(editor.render(80)));
});

test("a known file chip embedded in pasted prose is registered", () => {
  const editor = new Editor(tui, theme, { paddingX: 0 });
  editor.insertFileAttachment("/tmp/embedded.txt");
  const [before] = editor.getFileAttachments();
  editor.setText("");

  paste(editor, "see [File: embedded.txt] here");
  assert.equal(editor.getText(), "see [File: embedded.txt] here");
  assert.deepEqual(editor.getFileAttachments(), [before]);
});

test("typed lookalikes and unknown pasted markers stay plain", () => {
  // Typing marker text never creates a chip, even for a name we remember.
  const typed = new Editor(tui, theme, { paddingX: 0 });
  typed.insertFileAttachment("/tmp/known.txt");
  typed.setText("");
  for (const char of "[File: known.txt]") typed.handleInput(char);
  assert.deepEqual(typed.getFileAttachments(), []);
  assert.ok(!yellow(typed.render(80)), "typed marker is not highlighted");

  // A pasted marker with no remembered source path stays plain too.
  const unknown = new Editor(tui, theme, { paddingX: 0 });
  unknown.insertFileAttachment("/tmp/known.txt");
  unknown.setText("");
  paste(unknown, "[File: unknown.txt]");
  assert.equal(unknown.getText(), "[File: unknown.txt]");
  assert.deepEqual(unknown.getFileAttachments(), []);
  assert.ok(!yellow(unknown.render(80)), "unknown marker is not highlighted");
});

test("a typed lookalike never revives a deleted chip, and undo still restores it", () => {
  const editor = new Editor(tui, theme, { paddingX: 0 });
  editor.insertFileAttachment("/tmp/known.txt");
  // Delete the separator and the chip; its payload is still remembered.
  editor.handleInput("\x7f");
  editor.handleInput("\x7f");
  assert.deepEqual(editor.getFileAttachments(), []);

  // Typing the exact marker text must not attach the remembered file.
  for (const char of "[File: known.txt]") editor.handleInput(char);
  assert.deepEqual(editor.getFileAttachments(), []);
  assert.ok(!yellow(editor.render(80)), "typed lookalike is not highlighted");

  // Undo still brings back the deleted chip with its payload.
  for (let i = 0; i < 5 && editor.getFileAttachments().length === 0; i++) {
    editor.handleInput("\x1f");
  }
  assert.deepEqual(editor.getFileAttachments().map((attachment) => attachment.path), ["/tmp/known.txt"]);
});

test("typing a marker that is still active elsewhere keeps its chip", () => {
  const editor = new Editor(tui, theme, { paddingX: 0 });
  editor.insertFileAttachment("/tmp/active.txt");
  editor.handleInput("\n");
  for (const char of "[File: active.txt]") editor.handleInput(char);

  // The real chip on the first line must keep its payload.
  assert.deepEqual(editor.getFileAttachments(), [
    { marker: "[File: active.txt]", path: "/tmp/active.txt", id: "file-1", name: "active.txt" },
  ]);
});

test("multiple files, spaces and unicode survive in a safe visible label", () => {
  const editor = new Editor(tui, theme, { paddingX: 0 });
  editor.insertFileAttachment("/tmp/Ré sumé — final.pdf");
  editor.insertFileAttachment("/tmp/Screen\u202fShot.png");
  assert.equal(editor.getText(), "[File: Ré sumé — final.pdf] [File: Screen Shot.png] ");
  assert.deepEqual(editor.getFileAttachments(), [
    { marker: "[File: Ré sumé — final.pdf]", path: "/tmp/Ré sumé — final.pdf", id: "file-1", name: "Ré sumé — final.pdf" },
    { marker: "[File: Screen Shot.png]", path: "/tmp/Screen\u202fShot.png", id: "file-2", name: "Screen Shot.png" },
  ]);
  assert.ok(yellow(editor.render(80)));

  // The full unicode chip deletes as one unit.
  editor.handleInput("\x7f");
  editor.handleInput("\x7f");
  assert.equal(editor.getText(), "[File: Ré sumé — final.pdf] ");
});

test("an app-supplied paste handler turns a pasted file path into a chip", () => {
  const editor = new Editor(tui, theme, { paddingX: 0 });
  const seen: string[] = [];
  editor.setFilePasteHandler((text) => {
    seen.push(text);
    return text.trim() === "/tmp/from handler.pdf" ? { path: text.trim() } : undefined;
  });

  paste(editor, "/tmp/from handler.pdf");
  assert.equal(editor.getText(), "[File: from handler.pdf] ");
  assert.deepEqual(editor.getFileAttachments().map((attachment) => attachment.path), [
    "/tmp/from handler.pdf",
  ]);
  assert.deepEqual(seen, ["/tmp/from handler.pdf"]);
});

test("a paste handler can claim multiple files at once", () => {
  const editor = new Editor(tui, theme, { paddingX: 0 });
  editor.setFilePasteHandler((text) =>
    text.trim() === "/tmp/one.txt /tmp/two.txt"
      ? [{ path: "/tmp/one.txt" }, { path: "/tmp/two.txt" }]
      : undefined,
  );

  paste(editor, "/tmp/one.txt /tmp/two.txt");
  assert.equal(editor.getText(), "[File: one.txt] [File: two.txt] ");
  assert.deepEqual(editor.getFileAttachments().map((attachment) => attachment.path), [
    "/tmp/one.txt",
    "/tmp/two.txt",
  ]);
});

test("a no-match paste handler preserves ordinary, multiline and large pastes", () => {
  const ordinary = new Editor(tui, theme, { paddingX: 0 });
  ordinary.setFilePasteHandler(() => undefined);
  paste(ordinary, "hello world");
  assert.equal(ordinary.getText(), "hello world");
  assert.deepEqual(ordinary.getFileAttachments(), []);

  const multiline = new Editor(tui, theme, { paddingX: 0 });
  multiline.setFilePasteHandler(() => undefined);
  paste(multiline, "hello\nworld");
  assert.equal(multiline.getText(), "hello\nworld");
  assert.deepEqual(multiline.getFileAttachments(), []);

  const large = new Editor(tui, theme, { paddingX: 0 });
  large.setFilePasteHandler(() => undefined);
  paste(large, "line\n".repeat(20));
  assert.match(large.getText(), /^\[paste #1 \+21 lines\] $/);
  assert.ok(yellow(large.render(80)));
});

test("image detection is unchanged when a file paste handler is installed", () => {
  const editor = new Editor(tui, theme, { paddingX: 0 });
  let handlerCalls = 0;
  editor.setFilePasteHandler(() => {
    handlerCalls++;
    return [{ path: "/tmp/wrong-file.txt" }];
  });

  paste(editor, "/tmp/pasted shot.png");
  assert.equal(editor.getText(), "[Image: pasted shot.png] ");
  assert.deepEqual(editor.getImageAttachments(), [
    { marker: "[Image: pasted shot.png]", path: "/tmp/pasted shot.png" },
  ]);
  assert.deepEqual(editor.getFileAttachments(), []);
  assert.equal(handlerCalls, 0, "image paths keep their existing detection");
});

test("submitting hands file chips to onSubmit before the editor clears", () => {
  const editor = new Editor(tui, theme, { paddingX: 0 });
  editor.insertFileAttachment("/tmp/a/report.pdf");
  editor.insertFileAttachment("/tmp/b/report.pdf");
  const expected = editor.getFileAttachments();

  let submitted: string | undefined;
  let images: Array<{ marker: string; path: string }> = [];
  let files: EditorFileAttachment[] = [];
  editor.onSubmit = (text, imageAttachments, fileAttachments) => {
    submitted = text;
    images = imageAttachments;
    files = fileAttachments;
  };

  editor.handleInput("\r");
  assert.equal(submitted, "[File: report.pdf] [File: report.pdf (2)]");
  assert.deepEqual(images, []);
  assert.deepEqual(files, expected);
  assert.equal(editor.getText(), "");
  assert.deepEqual(editor.getFileAttachments(), []);
});
