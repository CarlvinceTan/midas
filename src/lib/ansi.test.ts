import assert from "node:assert/strict";
import { test } from "node:test";
import { CONTENT_END, CONTENT_START, DECORATION, markRenderedLines, stripBold } from "./ansi.ts";

test("markRenderedLines bounds visible content and leaves padding outside", () => {
  assert.deepEqual(markRenderedLines(["  hello world   "]), [`  ${CONTENT_START}hello world${CONTENT_END}   `]);
});

test("markRenderedLines skips zero-width escapes and content-free lines", () => {
  // A pi shell-integration mark with no visible text has nothing to select.
  assert.deepEqual(markRenderedLines(["\x1b]133;A\x07"]), ["\x1b]133;A\x07"]);
  const [styled] = markRenderedLines(["\x1b]133;B\x07  \x1b[38;2;1;2;3mhi\x1b[39m  "]);
  assert.ok(
    styled!.includes(`\x1b[38;2;1;2;3m${CONTENT_START}hi${CONTENT_END}\x1b[39m`),
    `styled content was not bounded: ${JSON.stringify(styled)}`,
  );
  assert.ok(styled!.startsWith("\x1b]133;B\x07  "), `leading padding was not preserved: ${JSON.stringify(styled)}`);
});

test("markRenderedLines preserves existing bounds and decoration", () => {
  const marked = `  ${CONTENT_START}x${CONTENT_END}`;
  assert.deepEqual(markRenderedLines([marked]), [marked]);
  const decoration = `${DECORATION}╭─╮`;
  assert.deepEqual(markRenderedLines([decoration]), [decoration]);
});

test("stripBold drops bold without disturbing the surrounding colour", () => {
  assert.equal(stripBold("\x1b[38;2;1;2;3m\x1b[1m$ ls\x1b[22m\x1b[39m"), "\x1b[38;2;1;2;3m$ ls\x1b[22m\x1b[39m");
  assert.equal(stripBold("plain"), "plain");
});
