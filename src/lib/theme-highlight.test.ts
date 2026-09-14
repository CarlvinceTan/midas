import assert from "node:assert/strict";
import { test } from "node:test";
import { initTheme as initPiTheme } from "@earendil-works/pi-coding-agent";
import { getMarkdownTheme, initTheme, theme } from "../theme/theme.ts";

// getMarkdownTheme delegates colouring to pi's highlighter, which reads pi's
// theme singleton; initialise both exactly as the app does at startup.
initPiTheme("onedark", false);
initTheme("onedark");

test("markdown theme highlights fenced code with per-token syntax colours", () => {
  const md = getMarkdownTheme();
  assert.equal(typeof md.highlightCode, "function");

  const lines = md.highlightCode!("#include <omp.h>\nint main() {\n  // TODO: run\n}\n", "cpp");
  const keyword = theme().getFgAnsi("syntaxKeyword");
  const comment = theme().getFgAnsi("syntaxComment");

  assert.ok(
    lines.some((line) => line.includes("int") && line.includes(keyword)),
    `keyword was not syntax-coloured: ${JSON.stringify(lines)}`,
  );
  assert.ok(
    lines.some((line) => line.includes("TODO") && line.includes(comment)),
    `comment was not syntax-coloured: ${JSON.stringify(lines)}`,
  );
  // Syntax colours must differ from the flat code-block colour, otherwise the
  // code would render monochrome.
  assert.notEqual(keyword, theme().getFgAnsi("mdCodeBlock"));
});

test("unlabelled code blocks keep the flat code-block colour", () => {
  const md = getMarkdownTheme();
  assert.deepEqual(md.highlightCode!("no language here", undefined), [
    theme().fg("mdCodeBlock", "no language here"),
  ]);
});
