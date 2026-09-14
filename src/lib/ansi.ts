/**
 * Minimal 24-bit ANSI styling with correct nested close codes, so styles can be
 * composed (e.g. bold inside a colored frame) without resetting the outer style.
 */

import { truncateToWidth, visibleWidth } from "@earendil-works/pi-tui";

const RESET = "\x1b[0m";

function hexToRgb(hex: string): [number, number, number] {
  const h = hex.replace("#", "");
  return [
    Number.parseInt(h.slice(0, 2), 16),
    Number.parseInt(h.slice(2, 4), 16),
    Number.parseInt(h.slice(4, 6), 16),
  ];
}

export function fg(hex: string, text: string): string {
  const [r, g, b] = hexToRgb(hex);
  return `\x1b[38;2;${r};${g};${b}m${text}\x1b[39m`;
}

export function bg(hex: string, text: string): string {
  const [r, g, b] = hexToRgb(hex);
  return `\x1b[48;2;${r};${g};${b}m${text}\x1b[49m`;
}

export const bold = (t: string): string => `\x1b[1m${t}\x1b[22m`;
export const dim = (t: string): string => `\x1b[2m${t}\x1b[22m`;
export const italic = (t: string): string => `\x1b[3m${t}\x1b[23m`;
export const underline = (t: string): string => `\x1b[4m${t}\x1b[24m`;
export const inverse = (t: string): string => `\x1b[7m${t}\x1b[27m`;
export const strikethrough = (t: string): string => `\x1b[9m${t}\x1b[29m`;

/** Strip all SGR sequences so text can be measured/styled cleanly. */
export function stripAnsi(text: string): string {
  return text.replace(/\x1b\[[0-9;]*m/g, "");
}

/** Remove bold SGR so already-styled text renders at normal weight. */
export function stripBold(text: string): string {
  return text.replace(/\x1b\[1m/g, "");
}

/**
 * Truncate styled text to `maxWidth` visible columns, appending an ellipsis in
 * the same colour as the text it replaces (pi-tui's truncation resets the colour
 * before its ellipsis).
 */
export function truncateColored(text: string, maxWidth: number, ellipsis = "…"): string {
  if (maxWidth <= 0) return "";
  if (visibleWidth(text) <= maxWidth) return text;
  const cut = truncateToWidth(text, maxWidth, ellipsis);
  const index = cut.lastIndexOf(ellipsis);
  if (index === -1) return cut;
  const before = cut.slice(0, index);
  const codes = [...before.matchAll(/\x1b\[[0-9;]*m/g)].map((match) => match[0]);
  const color =
    [...codes].reverse().find((code) => code !== "\x1b[0m" && code !== "\x1b[39m" && code !== "\x1b[22m" && code !== "\x1b[49m") ?? "";
  return before + color + ellipsis + "\x1b[0m";
}

export { RESET };

/**
 * Zero-width content bounds used by pi-tui's selection logic. Padding placed
 * outside these markers is excluded when drag-selecting text to copy.
 */
export const CONTENT_START = "\x1b]777;pi-content-start\x07";
export const CONTENT_END = "\x1b]777;pi-content-end\x07";

export function markContent(text: string): string {
  return CONTENT_START + text + CONTENT_END;
}

/** Zero-width ANSI/OSC sequences: SGR/CSI and OSC (pi shell-integration marks). */
const ESCAPE = /\x1b\][^\x07]*(?:\x07|\x1b\\)|\x1b\[[0-9;?]*[ -/]*[@-~]/g;

/**
 * Wrap already-rendered lines (e.g. from a pi component that does not emit its
 * own bounds) so drag-selection excludes leading/trailing padding. Existing
 * content bounds and decoration lines are left untouched.
 */
export function markRenderedLines(lines: string[]): string[] {
  return lines.map((line) => {
    if (line.includes(CONTENT_START) || line.includes(DECORATION)) return line;
    let first = -1;
    let last = -1;
    let index = 0;
    while (index < line.length) {
      ESCAPE.lastIndex = index;
      const match = ESCAPE.exec(line);
      if (match && match.index === index) {
        index += match[0].length;
        continue;
      }
      if (line[index] !== " ") {
        if (first < 0) first = index;
        last = index;
      }
      index += 1;
    }
    if (first < 0) return line; // Whitespace/zero-width only: nothing to select.
    return line.slice(0, first) + CONTENT_START + line.slice(first, last + 1) + CONTENT_END + line.slice(last + 1);
  });
}

/** Lines containing this marker are skipped when a selection is copied. */
export const DECORATION = "\x1b]777;pi-decoration\x07";

export function markDecoration(text: string): string {
  return DECORATION + text;
}
