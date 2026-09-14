import test from "node:test";
import assert from "node:assert/strict";
import { stripTerminalSequences } from "@earendil-works/pi-tui";
import { RoundedDialogFrame } from "./rounded-frame.ts";

const plain = (lines: string[]): string[] => lines.map(stripTerminalSequences);

// Minimal stand-in for the editor: a box rule above/below the input plus an
// autocomplete menu row below the bottom rule, which is what pi-tui emits.
const editor = {
  invalidate() {},
  render: (): string[] => ["──────", "/m", "──────", "→ model", "  mcps"],
};

test("autocomplete rows align the command under the first typed character", () => {
  const frame = new RoundedDialogFrame(() => 1);
  frame.addChild(editor);
  const lines = plain(frame.render(40));

  const body = lines.find((line) => line.includes("/m"))!;
  const selected = lines.find((line) => line.includes("model"))!;
  const other = lines.find((line) => line.includes("mcps"))!;

  // `/m` sits two columns in (border + gutter); the menu names line up on the
  // `m` the user typed, not on the slash.
  assert.equal(body.indexOf("/m") + 1, selected.indexOf("model"));
  assert.equal(body.indexOf("/m") + 1, other.indexOf("mcps"));
});

test("autocomplete rows still fill the full frame width", () => {
  const frame = new RoundedDialogFrame(() => 1);
  frame.addChild(editor);
  for (const line of plain(frame.render(40))) assert.equal(line.length, 40);
});

test("box-drawing characters in the input do not disable the frame", () => {
  // Pasting a diagram into the editor puts `│`/`╰`/`╭` in the content rows; that
  // must not be mistaken for the child already drawing a rounded frame.
  const pasted = {
    invalidate() {},
    render: (): string[] => ["──────", "│ [x] Other │", "╰──────────╯", "──────"],
  };
  const frame = new RoundedDialogFrame(() => 1);
  frame.addChild(pasted);
  const lines = plain(frame.render(20));
  assert.ok(lines[0]!.includes("╭") && lines[0]!.includes("╮"), `top rule not rounded: ${lines[0]}`);
  assert.ok(lines.at(-1)!.includes("╰") && lines.at(-1)!.includes("╯"), `bottom rule not rounded: ${lines.at(-1)}`);
  assert.ok(lines.some((line) => line.includes("│ [x] Other │")), "pasted content row missing");
});
