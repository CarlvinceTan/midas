#!/usr/bin/env node

import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import {
  AltScreenSearchComponent,
  AltScreenSearchIndex,
  findAltScreenSearchMatches,
  getAltScreenSearchMatchKey,
} from "../../third_party/pi-tui/dist/alt-screen-search.js";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
const output = path.join(root, "tui/testdata/alt-screen-search-reference.json");
const check = process.argv.includes("--check");

const matchCases = [
  { name: "empty", lines: ["foo"], query: "   " },
  { name: "ascii-case-ansi", lines: ["\x1b[31mFoo  bar\x1b[0m", "baz.foo", "foofoo"], query: "foo" },
  { name: "cross-whitespace", lines: ["alpha  ", "", "\tbeta"], query: "alpha beta" },
  { name: "literal-punctuation", lines: ["a.b axb A.B"], query: "a.b" },
  { name: "unicode-cells", lines: ["A界é🙂Z"], query: "界é🙂" },
  { name: "combining-submatch", lines: ["A界é🙂Z"], query: "́" },
  { name: "astral-prefix", lines: ["🙂🙂 target"], query: "target" },
  { name: "bom-whitespace", lines: ["one\uFEFFtwo"], query: "one two" },
  { name: "nel-not-whitespace", lines: ["one\u0085two"], query: "one two" },
].map((test) => {
  const matches = findAltScreenSearchMatches(test.lines, test.query);
  return { ...test, matches, keys: matches.map(getAltScreenSearchMatchKey) };
});

const index = new AltScreenSearchIndex();
const indexInputs = [
  { label: "initial", lines: ["Foo bar", "foo"], query: "foo" },
  { label: "same", lines: ["Foo bar", "foo"], query: "foo" },
  { label: "normalized-same", lines: ["Foo bar", "foo"], query: "  foo  " },
  { label: "case-change", lines: ["Foo bar", "foo"], query: "FOO" },
  { label: "source-ansi-change", lines: ["\x1b[31mFoo\x1b[0m bar", "foo"], query: "FOO" },
  { label: "source-text-change", lines: ["none", "foo"], query: "FOO" },
];
const indexSteps = indexInputs.map((input) => {
  const result = index.search(input.lines, input.query);
  return { label: input.label, changed: result.changed, matches: result.matches };
});

const queries = [];
const component = new AltScreenSearchComponent((query) => queries.push(query), (text, hovered) => hovered ? `<${text}>` : text);
component.focused = true;
const componentSteps = [];
const snapshot = (label, width) => {
  const lines = component.render(width);
  componentSteps.push({
    label, width, lines, query: component.input.getValue(),
    resultIndex: component.resultIndex, resultCount: component.resultCount,
    previousButtonStart: component.previousButtonStart, previousButtonEnd: component.previousButtonEnd,
    nextButtonStart: component.nextButtonStart, nextButtonEnd: component.nextButtonEnd,
    directionAtPrevious: component.getNavigationDirectionAt(2, component.previousButtonStart),
    directionAtNext: component.getNavigationDirectionAt(2, component.nextButtonStart),
    queries: [...queries],
  });
};
snapshot("empty-width-1", 1);
snapshot("empty-width-16", 16);
snapshot("empty-width-32", 32);
component.handleInput("Foo");
component.setResult(1, 4);
snapshot("query-results", 40);
component.handleInput("\x1b[D");
component.handleInput("\x7f");
snapshot("edit", 40);
component.handleInput("\x1b[200~ a\r\nb\tc \x1b[201~");
component.setResult(-1, 0);
snapshot("paste-no-matches", 24);
component.setHoveredNavigationDirection(-1);
snapshot("hover-previous", 40);
component.setHoveredNavigationDirection(1);
snapshot("hover-next", 40);

const contents = JSON.stringify({ matchCases, indexSteps, componentSteps }, null, 2) + "\n";
if (check) {
  const current = fs.existsSync(output) ? fs.readFileSync(output, "utf8") : "";
  if (current !== contents) {
    console.error("gen-tui-alt-screen-search-goldens: goldens are stale; re-run without --check");
    process.exit(1);
  }
  console.log("alternate-screen search goldens are current");
  process.exit(0);
}
fs.mkdirSync(path.dirname(output), { recursive: true });
fs.writeFileSync(output, contents);
console.log(`wrote ${path.relative(root, output)}: ${matchCases.length + indexSteps.length + componentSteps.length} differential cases`);
