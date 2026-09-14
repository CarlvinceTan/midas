import assert from "node:assert/strict";
import { test } from "node:test";
import { initTheme as initPiTheme } from "@earendil-works/pi-coding-agent";
import { stripAnsi } from "../../lib/ansi.ts";
import { initTheme } from "../../theme/theme.ts";
import { UserPromptCard } from "./user-prompt.ts";

initPiTheme(undefined, false);
initTheme(undefined);

test("UserPromptCard renders image chips in yellow", () => {
  const card = new UserPromptCard("look at [Image: shot.png] please", 2, (text) => text);
  const rendered = card.render(80).join("\n");
  assert.ok(rendered.includes("\x1b[33m[Image: shot.png]"), "the pasted image chip should be yellow");
  assert.ok(stripAnsi(rendered).includes("look at [Image: shot.png] please"), "the prompt text is unchanged");
});

test("UserPromptCard renders every image marker as a chip", () => {
  const card = new UserPromptCard("an example is [Image: example.png] like so", 2, (text) => text);
  const rendered = card.render(80).join("\n");
  assert.ok(rendered.includes("\x1b[33m[Image: example.png]"), "any [Image: …] marker renders as the chip");
  assert.ok(stripAnsi(rendered).includes("[Image: example.png]"));
});

test("UserPromptCard renders generic file chips in yellow", () => {
  const card = new UserPromptCard("summarize [File: report.pdf] please", 2, (text) => text);
  const rendered = card.render(80).join("\n");
  assert.ok(rendered.includes("\x1b[33m[File: report.pdf]"), "the pasted file chip should be yellow");
  assert.ok(stripAnsi(rendered).includes("summarize [File: report.pdf] please"), "the prompt text is unchanged");
});

test("UserPromptCard renders image and file chips together", () => {
  const card = new UserPromptCard("see [Image: shot.png] and [File: notes.md]", 2, (text) => text);
  const rendered = card.render(80).join("\n");
  assert.ok(rendered.includes("\x1b[33m[Image: shot.png]"), "the image chip is yellow");
  assert.ok(rendered.includes("\x1b[33m[File: notes.md]"), "the file chip is yellow");
  assert.ok(stripAnsi(rendered).includes("[Image: shot.png] and [File: notes.md]"));
});

test("UserPromptCard hides machine-facing file-content sections", () => {
  const delivered =
    "[File: notes.md] summarize\n\n" +
    "----- BEGIN UNTRUSTED FILE CONTENT: notes.md (text/markdown, 11 bytes) -----\n" +
    "SECRET BODY\n" +
    "----- END UNTRUSTED FILE CONTENT: notes.md -----";
  const card = new UserPromptCard(delivered, 2, (text) => text);
  const rendered = stripAnsi(card.render(80).join("\n"));
  assert.ok(rendered.includes("[File: notes.md] summarize"), "the visible prompt and chip remain");
  assert.ok(!rendered.includes("SECRET BODY"), "the file content is not shown in the transcript");
  assert.ok(!rendered.includes("UNTRUSTED FILE CONTENT"));
});

test("UserPromptCard matches macOS screenshot names with narrow spaces", () => {
  const filename = "Screenshot 2026-09-13 at 12.40.09\u202Fpm.png";
  const card = new UserPromptCard(`[Image: ${filename}]`, 2, (text) => text);
  assert.ok(card.render(80).join("\n").includes("\x1b[33m"), "narrow no-break spaces are normalized for display");
});
