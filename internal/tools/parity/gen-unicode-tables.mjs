#!/usr/bin/env node
/**
 * Generate the Go Unicode tables that back the pi-tui text layer.
 *
 * The reference implementation classifies characters with JavaScript
 * Unicode-property regexes evaluated by V8's ICU. Go has no equivalent for the
 * binary properties it uses (`Default_Ignorable_Code_Point`, `Spacing_Mark`,
 * `RGI_Emoji`, script extensions, ...), so this script asks the *very same*
 * engine — the Node process running it — and emits the answers as Go data.
 * That keeps character classification tied to the reference runtime instead of
 * to a guessed Unicode version.
 *
 * Multi-codepoint emoji are the one thing a per-code-point sweep cannot answer,
 * so the sequence list is read from the vendored Unicode emoji data files and
 * then re-validated through `\p{RGI_Emoji}` before being emitted. Anything the
 * reference runtime disagrees with is dropped and reported, so the generated
 * table can never claim a sequence the reference would reject.
 *
 * Usage: node tools/parity/gen-unicode-tables.mjs [--check]
 *   --check  exit non-zero if the generated file is missing or stale
 */
import { readFileSync, writeFileSync, mkdirSync } from "node:fs";
import { join, relative, resolve } from "node:path";
import { eastAsianWidth } from "get-east-asian-width";

const repoRoot = resolve(import.meta.dirname, "..", "..");
const outFile = join(repoRoot, "tui", "text", "unicode_tables.go");
const dataDir = join(repoRoot, "tools", "parity", "data");
const check = process.argv.includes("--check");

const MAX_CP = 0x10ffff;

/** Binary (non-emoji) properties, in the order the Go file declares them. */
const CODE_POINT_PROPS = [
  ["defaultIgnorable", /^(?:\p{Default_Ignorable_Code_Point})+$/v],
  ["control", /^(?:\p{Control})+$/v],
  ["mark", /^(?:\p{Mark})+$/v],
  // `\p{Surrogate}` is deliberately absent: the sweep cannot stringify lone
  // surrogates, and the Go text layer classifies UTF-16 surrogate code units
  // explicitly instead of going through a table.
  ["format", /^(?:\p{Format})+$/v],
  ["spacingMark", /^(?:[\p{Spacing_Mark}--[\u1734\u302E\u302F]])+$/v],
  [
    "terminalSpacingExtra",
    /^(?:[\u065F\u0F7F\u102B\u102C\u1031\u1033-\u1035\u1038\u103A-\u103E])+$/v,
  ],
  [
    "cjk",
    /^[\p{Script_Extensions=Han}\p{Script_Extensions=Hiragana}\p{Script_Extensions=Katakana}\p{Script_Extensions=Hangul}\p{Script_Extensions=Bopomofo}]$/u,
  ],
  ["rgiEmojiSingle", /^\p{RGI_Emoji}$/v],
];

/**
 * East Asian width, swept through the very package the reference calls, so the
 * package's internal fast paths and range merging are captured exactly rather
 * than re-derived. `eastAsianWidth(cp, {ambiguousAsWide:false})` returns 2 for
 * fullwidth and wide, and 1 otherwise.
 */
function eastAsianWideRanges() {
  const ranges = [];
  let start = -1;
  for (let cp = 0; cp <= MAX_CP; cp += 1) {
    if (cp >= 0xd800 && cp <= 0xdfff) {
      if (start >= 0) ranges.push([start, cp - 1]);
      start = -1;
      continue;
    }
    const wide = eastAsianWidth(cp) === 2;
    if (wide && start < 0) start = cp;
    else if (!wide && start >= 0) {
      ranges.push([start, cp - 1]);
      start = -1;
    }
  }
  if (start >= 0) ranges.push([start, MAX_CP]);
  return ranges;
}

/** Collect the set of code points matching `re` and collapse it to ranges. */
function rangesFor(re) {
  const ranges = [];
  let start = -1;
  for (let cp = 0; cp <= MAX_CP; cp += 1) {
    // Lone surrogates can be matched by the Surrogate property but cannot be
    // stringified by fromCodePoint; the reference only ever sees them inside
    // well-formed strings, so classify them as non-matching here.
    if (cp >= 0xd800 && cp <= 0xdfff) {
      if (start >= 0) ranges.push([start, cp - 1]);
      start = -1;
      continue;
    }
    const hit = re.test(String.fromCodePoint(cp));
    if (hit && start < 0) start = cp;
    else if (!hit && start >= 0) {
      ranges.push([start, cp - 1]);
      start = -1;
    }
  }
  if (start >= 0) ranges.push([start, MAX_CP]);
  return ranges;
}

/** Pull `code ; name` sequence lines out of a vendored Unicode emoji data file. */
function readSequences(fileName) {
  const text = readFileSync(join(dataDir, fileName), "utf8");
  const sequences = [];
  for (const rawLine of text.split("\n")) {
    const line = rawLine.split("#")[0].trim();
    if (!line) continue;
    const [codes] = line.split(";");
    if (!codes) continue;
    const trimmed = codes.trim();
    // Only multi-codepoint sequences matter; single code points are covered by
    // the per-code-point sweep.
    if (!trimmed.includes(" ")) continue;
    const points = trimmed.split(/\s+/).map((hex) => Number.parseInt(hex, 16));
    if (points.some((cp) => !Number.isInteger(cp))) continue;
    sequences.push(String.fromCodePoint(...points));
  }
  return sequences;
}

/**
 * Parse the Indic_Conjunct_Break property from the vendored Unicode extract.
 *
 * Node 22 does not expose `\p{InCB=...}` to regular expressions, so this one
 * property cannot be swept the way the others are. It backs the GB9c rule that
 * V8's Intl.Segmenter applies but uniseg does not, so the Go segmenter merges
 * clusters across these code points; the merge is validated differentially
 * against ICU rather than trusted.
 */
function readInCB(wanted) {
  const text = readFileSync(join(dataDir, "indic-conjunct-break-16.0.0.txt"), "utf8");
  const ranges = [];
  for (const rawLine of text.split("\n")) {
    const line = rawLine.split("#")[0].trim();
    if (!line) continue;
    const parts = line.split(";").map((part) => part.trim());
    if (parts.length < 3 || parts[1] !== "InCB" || parts[2] !== wanted) continue;
    const [start, end] = parts[0].split("..");
    const lo = Number.parseInt(start, 16);
    const hi = end === undefined ? lo : Number.parseInt(end, 16);
    if (!Number.isInteger(lo) || !Number.isInteger(hi)) continue;
    ranges.push([lo, hi]);
  }
  ranges.sort((a, b) => a[0] - b[0]);
  return ranges;
}

const rgiEmojiRegex = /^\p{RGI_Emoji}$/v;
const sequences = new Set();
let rejected = 0;
for (const file of ["emoji-sequences.txt", "emoji-zwj-sequences.txt"]) {
  for (const sequence of readSequences(file)) {
    if (!rgiEmojiRegex.test(sequence)) {
      rejected += 1;
      continue;
    }
    sequences.add(sequence);
  }
}
/**
 * Sort by code point, which is also Go's byte order for UTF-8. JavaScript's
 * default sort compares UTF-16 code units, which orders supplementary
 * characters before BMP characters in U+E000..U+FFFF — the opposite of code
 * point order — so the Go binary search would disagree with the emitted order.
 */
function compareByCodePoint(a, b) {
  // `[...str]` yields single-character strings, not code points; `-` on those
  // is NaN, which a sort comparator treats as "equal" and silently skips work.
  const left = Array.from(a, (ch) => ch.codePointAt(0));
  const right = Array.from(b, (ch) => ch.codePointAt(0));
  const shared = Math.min(left.length, right.length);
  for (let i = 0; i < shared; i += 1) {
    if (left[i] !== right[i]) return left[i] - right[i];
  }
  return left.length - right.length;
}
const sortedSequences = [...sequences].sort(compareByCodePoint);

/**
 * The Go side looks ranges up with a binary search, which silently returns
 * wrong answers if the table is unsorted or overlapping. Sweeps cannot produce
 * that, but the parsed InCB data could, so assert before emitting.
 */
function assertSortedDisjoint(name, ranges) {
  for (let i = 0; i < ranges.length; i += 1) {
    const [lo, hi] = ranges[i];
    if (lo > hi) throw new Error(`${name}: range ${i} is inverted (${lo}..${hi})`);
    if (i > 0 && lo <= ranges[i - 1][1]) {
      throw new Error(
        `${name}: range ${i} (${lo}..${hi}) overlaps or touches the previous range ending at ${ranges[i - 1][1]}`,
      );
    }
  }
}

function formatRanges(name, ranges, indent = "\t") {
  const lines = [`var ${name} = []rune{`];
  for (let i = 0; i < ranges.length; i += 4) {
    const chunk = ranges
      .slice(i, i + 4)
      .map(([lo, hi]) => `0x${lo.toString(16)}, 0x${hi.toString(16)},`)
      .join(" ");
    lines.push(`${indent}${chunk}`);
  }
  lines.push("}");
  return lines.join("\n");
}

/** Code-point properties, computed once. */
const codePointRanges = new Map();
for (const [name, re] of CODE_POINT_PROPS) {
  codePointRanges.set(name, rangesFor(re));
}
codePointRanges.set("eastAsianWide", eastAsianWideRanges());
for (const name of ["Linker", "Consonant", "Extend"]) {
  codePointRanges.set(`indic${name}`, readInCB(name));
}
// Emitted in this order; eastAsianWide and the InCB sets come from other
// sources than the regex sweep.
const EMITTED_PROPS = [
  ...CODE_POINT_PROPS.map(([name]) => name),
  "eastAsianWide",
  "indicLinker",
  "indicConsonant",
  "indicExtend",
];

const sections = [
  "// Code generated by tools/parity/gen-unicode-tables.mjs. DO NOT EDIT.",
  "",
  "package text",
  "",
  "// Unicode property tables derived from the Node/ICU tables the TypeScript",
  "// reference classifies with, so the Go port agrees with it by construction.",
  "// Each table is a flat list of inclusive [lo, hi] rune pairs.",
  "",
  "// rgiEmojiSequences holds every multi-codepoint RGI emoji sequence, sorted.",
  "var rgiEmojiSequences = []string{",
];
for (const sequence of sortedSequences) {
  // JSON string encoding is a valid Go string literal for these code points.
  sections.push(`\t${JSON.stringify(sequence)},`);
}
sections.push("}");
sections.push("");

for (const name of EMITTED_PROPS) {
  const ranges = codePointRanges.get(name);
  assertSortedDisjoint(name, ranges);
  sections.push(`// ${name} matches ${ranges.length} range(s).`);
  sections.push(formatRanges(`${name}Ranges`, ranges));
  sections.push("");
}

// Trailing blank sections would leave the file gofmt-dirty.
const output = `${sections.join("\n").replace(/\n+$/, "")}\n`;

if (check) {
  let current;
  try {
    current = readFileSync(outFile, "utf8");
  } catch {
    current = undefined;
  }
  if (current !== output) {
    process.stderr.write("gen-unicode-tables: generated file is stale; re-run without --check\n");
    process.exit(1);
  }
  process.stdout.write("unicode tables are current\n");
  process.exit(0);
}

mkdirSync(join(repoRoot, "tui", "text"), { recursive: true });
writeFileSync(outFile, output, "utf8");

const summary = CODE_POINT_PROPS.map(([name]) => `${name}=${codePointRanges.get(name).length}`).join(" ");
process.stdout.write(
  `wrote ${relative(repoRoot, outFile)}: ${sortedSequences.length} emoji sequences ` +
    `(${rejected} rejected by the reference runtime), ranges ${summary}\n`,
);
