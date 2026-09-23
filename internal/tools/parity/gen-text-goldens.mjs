#!/usr/bin/env node
/**
 * Generate differential goldens for the pi-tui text layer.
 *
 * Every value in the output is produced by the live vendored build in
 * `third_party/pi-tui/dist`, running on the Node/ICU that the reference ships with.
 * The Go port is then tested against these recorded answers, so "the Go width
 * matches the TypeScript width" is a checked fact rather than a claim about
 * Unicode tables.
 *
 * Grapheme segmentation is recorded from a default `Intl.Segmenter`, which is
 * exactly how the reference constructs its own segmenter.
 *
 * Usage: node tools/parity/gen-text-goldens.mjs [--check]
 *   --check  exit non-zero if the generated file is missing or stale
 */
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { join, relative, resolve } from "node:path";
import {
  applyBackgroundToLine,
  extractAnsiCode,
  extractSegments,
  getActiveBackgroundAnsi,
  getGraphemeCellRange,
  getGraphemeSegmenter,
  getOsc8LinkAtColumn,
  roundedFrameRow,
  isPunctuationChar,
  isWhitespaceChar,
  normalizeTerminalOutput,
  sliceByColumn,
  sliceWithWidth,
  stripTerminalSequences,
  truncateToWidth,
  visibleWidth,
  wrapTextWithAnsi,
} from "../../third_party/pi-tui/dist/utils.js";

const repoRoot = resolve(import.meta.dirname, "..", "..");
const outFile = join(repoRoot, "tui", "text", "testdata", "reference.json");
const check = process.argv.includes("--check");

// These live in `dist/utils.js`; several are not re-exported from the package
// index. Using the reference's own segmenter instance keeps the recorded
// segmentation identical to what the reference computes internally.
const segmenter = getGraphemeSegmenter();
const cluster = (s) => [...segmenter.segment(s)].map((part) => part.segment);

/** Corpus of strings exercising width, ANSI stripping and normalisation. */
const STRINGS = [
  "",
  "a",
  "hello",
  "  spaced  ",
  "0x20..0x7e !~",
  "\t",
  "a\tb",
  "\t\t",
  "line\nbreak",
  "\r\n",
  "\x00",
  "\x7f",
  "café",
  "cafe\u0301",
  "e\u0301\u0327",
  "\u0301",
  "\u0301\u0302",
  "क",
  "नमस्ते",
  "क्‍ष",
  "\u0e33",
  "กำ",
  "\u0e4d\u0e32",
  "\u0eb3",
  "ກຳ",
  "\u065f",
  "\u0f7f",
  "\u102b",
  "\u1031",
  "\u103a\u103e",
  "你好",
  "你好世界",
  "こんにちは",
  "カタカナ",
  "한글",
  "한국어",
  "ｆｕｌｌｗｉｄｔｈ",
  "ﾊﾝｶｸ",
  "🀄",
  "😀",
  "😀😀",
  "👍🏽",
  "☺️",
  "❤️",
  "©️",
  "🇦🇺",
  "🇦🇺🇳🇿",
  "1️⃣",
  "#️⃣",
  "🏴󠁧󠁢󠁳󠁣󠁴󠁿",
  "👨‍👩‍👧‍👦",
  "👩🏽‍💻",
  "a😀b",
  "🇦",
  "\x1b[31mred\x1b[0m",
  "\x1b[31mred\x1b[0m plain",
  "\x1b[1;38;5;208mstyled\x1b[22m",
  "\x1b]8;;https://example.com\x07link\x1b]8;;\x07",
  "\x1b]8;;https://example.com\x1b\\link\x1b]8;;\x1b\\",
  "\x1b_pi:c\x07",
  "\x1b[?2026h",
  "\x1b[2K",
  "\x1b[1;2H",
  "\x1b[31",
  "\x1b]8;;https://example.com",
  "\x1b",
  "\x1b[",
  "\x1bX",
  "\x1b[31m\ttab inside\x1b[0m",
  "\x1b[31m😀\x1b[0m",
  "mixed \x1b[32m🀄\x1b[0m and 你好",
  "\x1b[0m\x1b]8;;\x07",
  "\u0e33 mixed \t and \x1b[31mstyled\x1b[0m",
];

// Indic conjunct coverage. GB9c is the one UAX #29 rule where uniseg and V8
// disagree, so the corpus exercises it systematically rather than by anecdote:
// consonant + linker combinations with extenders on either side, across three
// scripts, plus the real words that first exposed the difference.
const INDIC_CONSONANTS = ["\u0915", "\u0937", "\u0995"];
const INDIC_LINKERS = ["\u094D", "\u09CD", "\u0BCD"];
const INDIC_EXTENDS = ["\u093E", "\u200D", "\u0300"];
for (const consonant of INDIC_CONSONANTS) {
  for (const linker of INDIC_LINKERS) {
    STRINGS.push(consonant + linker + consonant);
    for (const extend of INDIC_EXTENDS) {
      STRINGS.push(consonant + linker + extend + consonant);
      STRINGS.push(consonant + extend + linker + consonant);
    }
  }
}
STRINGS.push(
  "\u0928\u092E\u0938\u094D\u0924\u0947",
  "\u0915\u094D\u0937",
  "\u0915\u094D\u0937\u093F",
  "\u0915\u094D\u200D\u0937",
  "\u0915\u094D\u0937\u094D\u0937",
);

/** Strings whose segmentation and per-cluster widths are recorded. */
const CLUSTER_SOURCES = [
  ...STRINGS,
  "e\u0301",
  "👨‍👩‍👧‍👦",
  "🏴󠁧󠁢󠁳󠁣󠁴󠁿",
  "👍🏽",
  "☺️",
  "1️⃣",
  "🇦🇺",
  "क्ष",
  "क्षि",
  "नमस्ते",
  "กำ",
  "\u0e33\u0e32",
  "ﾊﾝｶｸ",
  "ｆｕｌｌ",
];

const widths = STRINGS.map((s) => ({ s, width: visibleWidth(s) }));

// Segmentation, plus per-cluster width. Clusters containing an escape or a tab
// are excluded from the width table: `visibleWidth` on such a fragment does not
// reduce to `graphemeWidth`, so it would be a misleading golden.
const segmentation = [];
const clusterWidths = new Map();
for (const s of CLUSTER_SOURCES) {
  const clusters = cluster(s);
  segmentation.push({ s, clusters });
  for (const c of clusters) {
    if (c.includes("\x1b") || c.includes("\t")) continue;
    if (!clusterWidths.has(c)) clusterWidths.set(c, visibleWidth(c));
  }
}

const strip = STRINGS.map((s) => ({ s, out: stripTerminalSequences(s) }));
const normalize = STRINGS.map((s) => ({ s, out: normalizeTerminalOutput(s) }));

// extractAnsiCode at every position of every string, so partial and malformed
// sequences are covered as thoroughly as well-formed ones.
//
// `pos` is a UTF-16 code-unit index, which is what the reference uses and what
// the Go port cannot use. `posByte` is the same position expressed as a UTF-8
// byte offset, computed here so the Go side compares against byte offsets
// without silently conflating the two index models.
const ansi = [];
for (const s of STRINGS) {
  const byteLengthOfUnit = (units) => Buffer.byteLength(s.slice(0, units), "utf8");
  for (let pos = 0; pos <= s.length; pos += 1) {
    const found = extractAnsiCode(s, pos);
    if (!found) continue;
    ansi.push({ s, pos, posByte: byteLengthOfUnit(pos), code: found.code, length: found.length });
  }
}

// Truncation is exercised as a matrix: the same corpus against several widths,
// both ellipsis styles, and padded or not. Ellipsis strings are chosen to cover
// the branches where the ellipsis is wider than the budget.
const TRUNCATE_STRINGS = [
  "hello world",
  "hello",
  "a",
  "",
  "  leading and trailing  ",
  "café",
  "e\u0301\u0327",
  "\x1b[31mred text here\x1b[0m",
  "\x1b[31mred\x1b[0m plain tail",
  "\x1b]8;;https://example.com\x07a link that is long\x1b]8;;\x07",
  "\x1b[1;38;5;208mcolour 256\x1b[22m",
  "\t tabbed content",
  "tab\tinside",
  "你好世界你好世界",
  "こんにちは世界",
  "한국어 텍스트",
  "🀄🀄🀄",
  "😀 emoji then text",
  "👨‍👩‍👧‍👦 family",
  "🇦🇺🇳🇿 flags",
  "1️⃣ keycap",
  "नमस्ते दुनिया",
  "🎉 party 🎉",
  "\x1b[31m",
  "\x1b[31mabc",
];
const TRUNCATE_WIDTHS = [1, 3, 5, 10, 20];
const TRUNCATE_ELLIPSES = ["...", "…", ""];
const truncate = [];
for (const s of TRUNCATE_STRINGS) {
  for (const maxWidth of TRUNCATE_WIDTHS) {
    for (const ellipsis of TRUNCATE_ELLIPSES) {
      for (const pad of [false, true]) {
        truncate.push({ s, maxWidth, ellipsis, pad, out: truncateToWidth(s, maxWidth, ellipsis, pad) });
      }
    }
  }
}

// Background application and active-background extraction, both of which drive
// line composition in the renderers.
const bgFn = (text) => `\x1b[41m${text}\x1b[0m`;
const background = STRINGS.map((s, i) => ({
  s,
  width: [0, 1, 5, 12][i % 4],
  out: applyBackgroundToLine(s, [0, 1, 5, 12][i % 4], bgFn),
}));
const activeBackground = STRINGS.map((s) => ({ s, out: getActiveBackgroundAnsi(s) }));

// Wrapping: the corpus plus multi-line and long-unbroken-word inputs that force
// the breakLongWord path, at several widths.
const WRAP_STRINGS = [
  ...STRINGS,
  "the quick brown fox jumps over the lazy dog",
  "line one\nline two\nline three",
  "carriage\r\nreturns\r\nhere",
  "blank\n\nlines\n",
  "supercalifragilisticexpialidocious",
  "\x1b[31mred words that need wrapping across lines\x1b[0m",
  "\x1b[1;4mbold and underlined text that will wrap\x1b[0m",
  "\x1b]8;;https://example.com\x07a long hyperlink label that wraps\x1b]8;;\x07",
  "trailing spaces   ",
  "   leading spaces",
  "word\u00a0with\u00a0nbsp",
  "你好世界你好世界你好世界你好世界",
  "\x1b[31m你\x1b[0m好世界",
];
const wrap = [];
for (const s of WRAP_STRINGS) {
  for (const width of [1, 3, 5, 10, 20, 40]) {
    wrap.push({ s, width, out: wrapTextWithAnsi(s, width) });
  }
}

// Column slicing and overlay segmentation. These lines deliberately place ANSI
// codes exactly on column boundaries, which is where the vendored patch differs
// from upstream.
const SLICE_LINES = [
  "abcdefghij",
  "\x1b[31mabcdefghij\x1b[0m",
  "ab\x1b[31mcd\x1b[0mefghij",
  "\x1b[31mab\x1b[0m\x1b[32mcd\x1b[0mef",
  "\x1b[1mab\x1b[22mcd\x1b[4mef\x1b[24mgh",
  "你好世界",
  "a你b好c",
  "\x1b[31m你好\x1b[0m世界",
  "😀ab😀cd",
  "\x1b]8;;https://e.com\x07abcdef\x1b]8;;\x07ghi",
];
const slice = [];
const segments = [];
for (const line of SLICE_LINES) {
  for (const startCol of [0, 1, 2, 3, 5, 9]) {
    for (const length of [1, 2, 3, 5, 20]) {
      for (const strict of [false, true]) {
        const r = sliceWithWidth(line, startCol, length, strict);
        slice.push({ line, startCol, length, strict, text: sliceByColumn(line, startCol, length, strict), width: r.width });
      }
    }
    for (const beforeEnd of [0, 2, 4]) {
      for (const afterLen of [0, 3]) {
        const seg = extractSegments(line, beforeEnd, 5, afterLen, false);
        segments.push({ line, beforeEnd, afterStart: 5, afterLen, strictAfter: false, ...seg });
      }
    }
  }
}

// The two exported character classifiers.
const charClass = [
  " ",
  "\t",
  "\u00a0",
  "\ufeff",
  "\u0085",
  "a",
  "]",
  "`",
  "你好",
  "a b",
].map((s) => ({ s, whitespace: isWhitespaceChar(s), punctuation: isPunctuationChar(s) }));

// Column lookup helpers used by the alternate-screen selection code, plus the
// rounded frame row (a local addition to the vendored build).
const cellRange = [];
const osc8Link = [];
for (const line of SLICE_LINES) {
  for (let column = 0; column <= 12; column += 1) {
    const range = getGraphemeCellRange(line, column);
    cellRange.push({ line, column, found: range !== undefined, start: range?.start ?? 0, end: range?.end ?? 0 });
    const link = getOsc8LinkAtColumn(line, column);
    osc8Link.push({ line, column, found: link !== undefined, url: link ?? "" });
  }
}

const frameColor = (text) => `\x1b[36m${text}\x1b[39m`;
const roundedFrame = [];
for (const edge of ["top", "bottom", "body"]) {
  for (const middle of ["", "content", "\x1b[1mbold\x1b[0m"]) {
    roundedFrame.push({ middle, edge, out: roundedFrameRow(middle, edge, frameColor) });
  }
}

const golden = {
  generator: "tools/parity/gen-text-goldens.mjs",
  node: process.version,
  widths,
  truncate,
  wrap,
  slice,
  segments,
  charClass,
  cellRange,
  osc8Link,
  roundedFrame,
  background,
  activeBackground,
  clusters: [...clusterWidths.entries()].map(([c, width]) => ({ c, width })),
  segmentation,
  strip,
  normalize,
  ansi,
};

const output = `${JSON.stringify(golden, null, 2)}\n`;

if (check) {
  let current;
  try {
    current = readFileSync(outFile, "utf8");
  } catch {
    current = undefined;
  }
  if (current !== output) {
    process.stderr.write("gen-text-goldens: goldens are stale; re-run without --check\n");
    process.exit(1);
  }
  process.stdout.write("text goldens are current\n");
  process.exit(0);
}

mkdirSync(join(repoRoot, "tui", "text", "testdata"), { recursive: true });
writeFileSync(outFile, output, "utf8");
process.stdout.write(
  `wrote ${relative(repoRoot, outFile)}: ${widths.length} widths, ${clusterWidths.size} clusters, ` +
    `${segmentation.length} segmentations, ${strip.length} strips, ${normalize.length} normalizations, ${ansi.length} ANSI hits\n`,
);
