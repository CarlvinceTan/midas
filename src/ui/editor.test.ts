import test from "node:test";
import assert from "node:assert/strict";
import { Editor } from "@earendil-works/pi-tui";

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

const plain = (lines: string[]): string[] => lines.map((line) => line.replace(/\x1b\[[0-9;]*m/g, ""));
const yellow = (lines: string[]): boolean => lines.some((line) => line.includes("\x1b[33m"));

test("programmatic image chips render yellow, are atomic, and delete as a unit", () => {
  const editor = new Editor(tui, theme, { paddingX: 0 });
  editor.insertImageAttachment("/tmp/My Screenshot 2026.png");
  // A plain space follows the chip so text typed next doesn't glue onto it.
  assert.equal(editor.getText(), "[Image: My Screenshot 2026.png] ");
  assert.deepEqual(editor.getImageAttachments(), [
    { marker: "[Image: My Screenshot 2026.png]", path: "/tmp/My Screenshot 2026.png" },
  ]);
  assert.ok(yellow(editor.render(60)));
  // The trailing space is ordinary text; the chip itself still deletes as one unit.
  editor.handleInput("\x7f");
  assert.equal(editor.getText(), "[Image: My Screenshot 2026.png]");
  editor.handleInput("\x7f");
  assert.equal(editor.getText(), "");
  assert.deepEqual(editor.getImageAttachments(), []);
});

test("a typed image path collapses into a chip", () => {
  const editor = new Editor(tui, theme, { paddingX: 0 });
  for (const char of "/tmp/another shot.png") editor.handleInput(char);
  assert.equal(editor.getText(), "[Image: another shot.png] ");
  assert.equal(editor.getImageAttachments()[0]?.path, "/tmp/another shot.png");
});

test("submitting hands image chips to onSubmit before the editor clears", () => {
  const editor = new Editor(tui, theme, { paddingX: 0 });
  editor.insertImageAttachment("/tmp/submitted shot.png");
  let submitted: string | undefined;
  let attachments: Array<{ marker: string; path: string }> = [];
  editor.onSubmit = (text, images) => {
    submitted = text;
    attachments = images;
  };
  editor.handleInput("\r");
  assert.equal(submitted, "[Image: submitted shot.png]");
  assert.deepEqual(attachments, [
    { marker: "[Image: submitted shot.png]", path: "/tmp/submitted shot.png" },
  ]);
  assert.equal(editor.getText(), "");
});

test("setText drops stale chips and setImageAttachments restores a saved draft", () => {
  const editor = new Editor(tui, theme, { paddingX: 0 });
  editor.insertImageAttachment("/tmp/shot.png");
  editor.setText("[Image: shot.png]");
  // A wholesale replace must not keep the old chip's path mapping.
  assert.deepEqual(editor.getImageAttachments(), []);
  editor.setImageAttachments([{ marker: "[Image: shot.png]", path: "/tmp/shot.png" }]);
  assert.deepEqual(editor.getImageAttachments(), [{ marker: "[Image: shot.png]", path: "/tmp/shot.png" }]);
});

test("the leading ! of a shell command takes the border color", () => {
  const editor = new Editor(tui, theme, { paddingX: 0 });
  editor.borderColor = (text: string): string => `\x1b[34m${text}\x1b[39m`;
  editor.setText("! ls -la");
  const line = editor.render(40).find((value) => value.includes("ls -la"));
  assert.ok(line);
  assert.match(line, /\x1b\[34m!\x1b\[39m/);
});

/** Editor body rows (border rules removed) with trailing padding trimmed. */
const body = (editor: Editor, width: number): string[] =>
  plain(editor.render(width))
    .slice(1, -1)
    .map((line) => line.replace(/ +$/, ""));

test("a wrapped `!` command hangs its continuation under the command text", () => {
  const editor = new Editor(tui, theme, { paddingX: 0 });
  editor.setText("! echo aaaaaaaaaa bbbbbbbbbb cccccccccc");
  assert.deepEqual(body(editor, 30), ["! echo aaaaaaaaaa bbbbbbbbbb", "  cccccccccc"]);
});

test("a multiline `!` command indents later lines by the prefix width", () => {
  const editor = new Editor(tui, theme, { paddingX: 0 });
  editor.setText("! one\nsecond\nthird");
  assert.deepEqual(body(editor, 30), ["! one", "  second", "  third"]);

  const bang = new Editor(tui, theme, { paddingX: 0 });
  bang.setText("!! one\ntwo");
  assert.deepEqual(body(bang, 30), ["!! one", "   two"]);
});

test("deleting the space after ! disables the hanging indent", () => {
  const editor = new Editor(tui, theme, { paddingX: 0 });
  editor.setText("!echo aaaaaaaaaa bbbbbbbbbb cccccccccc");
  assert.deepEqual(body(editor, 30), ["!echo aaaaaaaaaa bbbbbbbbbb", "cccccccccc"]);
});

test("a voice-mode blue border colors the editor's rules", () => {
  const editor = new Editor(tui, theme, { paddingX: 0 });
  // The app installs this color while `/voice` is listening.
  editor.borderColor = (text: string): string => `\x1b[34m${text}\x1b[39m`;
  editor.setText("dictated words");
  const blue = editor.render(40).filter((line) => line.includes("\x1b[34m"));
  assert.ok(blue.length >= 2, "top and bottom rules take the blue border");
});

test("bracketed paste of an image path becomes a chip, large text a yellow marker", () => {
  const image = new Editor(tui, theme, { paddingX: 0 });
  image.handleInput("\x1b[200~/tmp/pasted shot.png\x1b[201~");
  assert.equal(image.getText(), "[Image: pasted shot.png] ");
  assert.equal(image.getImageAttachments()[0]?.path, "/tmp/pasted shot.png");

  const text = new Editor(tui, theme, { paddingX: 0 });
  text.handleInput(`\x1b[200~${"line\n".repeat(20)}\x1b[201~`);
  assert.match(text.getText(), /^\[paste #1 \+21 lines\] $/);
  assert.ok(yellow(text.render(80)));
});
