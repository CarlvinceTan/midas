import { readFileSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";
import { getCapabilities, type EditorTheme, type MarkdownTheme, type SelectListTheme } from "@earendil-works/pi-tui";
import { highlightCode as highlightPiCode } from "@earendil-works/pi-coding-agent";
import { strikethrough, underline, bold as ansiBold, inverse as ansiInverse } from "../lib/ansi.ts";
import { piAgentDir, loadPiSettings } from "../config/pi.ts";

/**
 * Faithful port of pi's theme engine (modes/interactive/theme/theme.ts).
 * Loads the same theme JSON schema (vars + colors + optional bg) and produces
 * identical ANSI output so midas matches `pi` visually.
 */

export type ThemeColor =
  | "accent" | "border" | "borderAccent" | "borderMuted" | "success" | "error" | "warning"
  | "muted" | "dim" | "text" | "thinkingText" | "scrollbarTrack" | "scrollbarThumb"
  | "searchMatchText" | "userMessageText" | "customMessageText" | "customMessageLabel"
  | "toolTitle" | "toolOutput" | "mdHeading" | "mdLink" | "mdLinkUrl" | "mdCode"
  | "mdCodeBlock" | "mdCodeBlockBorder" | "mdQuote" | "mdQuoteBorder" | "mdHr" | "mdListBullet"
  | "toolDiffAdded" | "toolDiffRemoved" | "toolDiffContext"
  | "syntaxComment" | "syntaxKeyword" | "syntaxFunction" | "syntaxVariable" | "syntaxString"
  | "syntaxNumber" | "syntaxType" | "syntaxOperator" | "syntaxPunctuation"
  | "thinkingOff" | "thinkingMinimal" | "thinkingLow" | "thinkingMedium" | "thinkingHigh"
  | "thinkingXhigh" | "thinkingMax" | "bashMode" | "startupHeading" | "multitask";

export type ThemeBg =
  | "selectedBg" | "searchMatchBg" | "userMessageBg" | "customMessageBg"
  | "toolPendingBg" | "toolSuccessBg" | "toolErrorBg";

type OptionalThemeColor = "scrollbarTrack" | "scrollbarThumb" | "thinkingMax" | "searchMatchText";
type OptionalThemeBg = "searchMatchBg";
export type ColorMode = "truecolor" | "256color";

interface ThemeJson {
  name?: string;
  vars?: Record<string, string | number>;
  colors: Record<string, string | number>;
}

function hexToRgb(hex: string): { r: number; g: number; b: number } {
  const cleaned = hex.replace("#", "");
  if (cleaned.length !== 6) throw new Error(`Invalid hex color: ${hex}`);
  const r = Number.parseInt(cleaned.slice(0, 2), 16);
  const g = Number.parseInt(cleaned.slice(2, 4), 16);
  const b = Number.parseInt(cleaned.slice(4, 6), 16);
  if (Number.isNaN(r) || Number.isNaN(g) || Number.isNaN(b)) throw new Error(`Invalid hex color: ${hex}`);
  return { r, g, b };
}

const CUBE_VALUES = [0, 95, 135, 175, 215, 255];
const GRAY_VALUES = Array.from({ length: 24 }, (_, i) => 8 + i * 10);

function closestIndex(value: number, values: number[]): number {
  let best = 0;
  let bestDist = Number.POSITIVE_INFINITY;
  values.forEach((candidate, index) => {
    const dist = Math.abs(value - candidate);
    if (dist < bestDist) {
      bestDist = dist;
      best = index;
    }
  });
  return best;
}

function colorDistance(r1: number, g1: number, b1: number, r2: number, g2: number, b2: number): number {
  const dr = r1 - r2;
  const dg = g1 - g2;
  const db = b1 - b2;
  return dr * dr * 0.299 + dg * dg * 0.587 + db * db * 0.114;
}

function rgbTo256(r: number, g: number, b: number): number {
  const ri = closestIndex(r, CUBE_VALUES);
  const gi = closestIndex(g, CUBE_VALUES);
  const bi = closestIndex(b, CUBE_VALUES);
  const cubeIndex = 16 + 36 * ri + 6 * gi + bi;
  const cubeDist = colorDistance(r, g, b, CUBE_VALUES[ri]!, CUBE_VALUES[gi]!, CUBE_VALUES[bi]!);
  const gray = Math.round(0.299 * r + 0.587 * g + 0.114 * b);
  const grayIndex = closestIndex(gray, GRAY_VALUES);
  const grayValue = GRAY_VALUES[grayIndex]!;
  const grayDist = colorDistance(r, g, b, grayValue, grayValue, grayValue);
  const spread = Math.max(r, g, b) - Math.min(r, g, b);
  if (spread < 10 && grayDist < cubeDist) return 232 + grayIndex;
  return cubeIndex;
}

function fgAnsi(color: string | number, mode: ColorMode): string {
  if (color === "") return "\x1b[39m";
  if (typeof color === "number") return `\x1b[38;5;${color}m`;
  if (color.startsWith("#")) {
    if (mode === "truecolor") {
      const { r, g, b } = hexToRgb(color);
      return `\x1b[38;2;${r};${g};${b}m`;
    }
    return `\x1b[38;5;${rgbTo256(...Object.values(hexToRgb(color)) as [number, number, number])}m`;
  }
  throw new Error(`Invalid color value: ${color}`);
}

function bgAnsi(color: string | number, mode: ColorMode): string {
  if (color === "") return "\x1b[49m";
  if (typeof color === "number") return `\x1b[48;5;${color}m`;
  if (color.startsWith("#")) {
    if (mode === "truecolor") {
      const { r, g, b } = hexToRgb(color);
      return `\x1b[48;2;${r};${g};${b}m`;
    }
    return `\x1b[48;5;${rgbTo256(...Object.values(hexToRgb(color)) as [number, number, number])}m`;
  }
  throw new Error(`Invalid color value: ${color}`);
}

function resolveVarRefs(value: string | number, vars: Record<string, string | number>, visited = new Set<string>()): string | number {
  if (typeof value === "number" || value === "" || value.startsWith("#")) return value;
  if (visited.has(value)) throw new Error(`Circular variable reference: ${value}`);
  if (!(value in vars)) throw new Error(`Variable reference not found: ${value}`);
  visited.add(value);
  return resolveVarRefs(vars[value]!, vars, visited);
}

function resolveThemeColors(colors: Record<string, string | number>, vars: Record<string, string | number> = {}): Record<string, string | number> {
  const resolved: Record<string, string | number> = {};
  for (const [key, value] of Object.entries(colors)) resolved[key] = resolveVarRefs(value, vars);
  return resolved;
}

function withThemeColorFallbacks(colors: Record<string, string | number>): Record<string, string | number> {
  return {
    ...colors,
    scrollbarTrack: colors.scrollbarTrack ?? colors.muted ?? "",
    scrollbarThumb: colors.scrollbarThumb ?? colors.text ?? "",
    thinkingMax: colors.thinkingMax ?? colors.thinkingXhigh ?? "",
    searchMatchBg: colors.searchMatchBg ?? colors.selectedBg ?? "",
    searchMatchText: colors.searchMatchText ?? colors.text ?? "",
    // Startup column headers ([Context], [Agents], …) are purple in onedark;
    // map them to the theme's purple label color with a heading fallback.
    startupHeading: colors.startupHeading ?? colors.customMessageLabel ?? colors.mdHeading ?? "",
    // Multitask mode accent (opencode task-mode tomato).
    multitask: colors.multitask ?? colors.error ?? "",
  };
}

function thinkingBorderColor(level: string): ThemeColor {
  const map: Record<string, ThemeColor> = {
    off: "thinkingOff", minimal: "thinkingMinimal", low: "thinkingLow",
    medium: "thinkingMedium", high: "thinkingHigh", xhigh: "thinkingXhigh", max: "thinkingMax",
  };
  return map[level] ?? "thinkingOff";
}

const BG_KEYS = new Set([
  "selectedBg", "searchMatchBg", "userMessageBg", "customMessageBg",
  "toolPendingBg", "toolSuccessBg", "toolErrorBg",
]);

export class Theme {
  readonly name?: string;
  readonly sourcePath?: string;
  private fgColors = new Map<string, string>();
  private bgColors = new Map<string, string>();
  private mode: ColorMode;

  constructor(
    fgColors: Record<Exclude<ThemeColor, OptionalThemeColor>, string | number> & Partial<Record<OptionalThemeColor, string | number>>,
    bgColors: Record<Exclude<ThemeBg, OptionalThemeBg>, string | number> & Partial<Record<OptionalThemeBg, string | number>>,
    mode: ColorMode,
    options: { name?: string; sourcePath?: string } = {},
  ) {
    this.name = options.name;
    this.sourcePath = options.sourcePath;
    this.mode = mode;
    const colors = {
      ...fgColors,
      scrollbarTrack: fgColors.scrollbarTrack ?? fgColors.muted,
      scrollbarThumb: fgColors.scrollbarThumb ?? fgColors.text,
      thinkingMax: fgColors.thinkingMax ?? fgColors.thinkingXhigh,
      searchMatchText: fgColors.searchMatchText ?? fgColors.text,
      startupHeading: fgColors.startupHeading ?? fgColors.customMessageLabel ?? fgColors.mdHeading,
    } as Record<string, string | number>;
    for (const [key, value] of Object.entries(colors)) this.fgColors.set(key, fgAnsi(value, mode));
    const backgrounds = { ...bgColors, searchMatchBg: bgColors.searchMatchBg ?? bgColors.selectedBg } as Record<string, string | number>;
    for (const [key, value] of Object.entries(backgrounds)) this.bgColors.set(key, bgAnsi(value, mode));
  }

  fg(color: ThemeColor, text: string): string {
    const ansi = this.fgColors.get(color);
    if (!ansi) throw new Error(`Unknown theme color: ${color}`);
    return `${ansi}${text}\x1b[39m`;
  }

  bg(color: ThemeBg, text: string): string {
    const ansi = this.bgColors.get(color);
    if (!ansi) throw new Error(`Unknown theme background color: ${color}`);
    return `${ansi}${text}\x1b[49m`;
  }

  getFgAnsi(color: ThemeColor): string {
    const ansi = this.fgColors.get(color);
    if (!ansi) throw new Error(`Unknown theme color: ${color}`);
    return ansi;
  }

  bold = ansiBold;
  /** Italics are disabled throughout midas; the text is returned unchanged. */
  italic = (text: string): string => text;
  underline = underline;
  inverse = ansiInverse;
  strikethrough = strikethrough;

  getThinkingBorderColor(level: string): (text: string) => string {
    return (text) => this.fg(thinkingBorderColor(level), text);
  }

  getBashModeBorderColor(): (text: string) => string {
    return (text) => this.fg("bashMode", text);
  }
}

const FALLBACK_ONEDARK: ThemeJson = {
  name: "onedark",
  colors: {
    accent: "#61afef", border: "#3e4452", borderAccent: "#61afef", borderMuted: "#2c313c",
    success: "#98c379", error: "#e06c75", warning: "#e06c75", muted: "#7f848e", dim: "#5c6370",
    text: "#abb2bf", thinkingText: "#7f848e", selectedBg: "#3e4451", scrollbarTrack: "#21252b",
    scrollbarThumb: "#3e4452", searchMatchBg: "#d19a66", searchMatchText: "#282c34",
    userMessageBg: "#2c313c", userMessageText: "#abb2bf", customMessageBg: "#21252b",
    customMessageText: "#abb2bf", customMessageLabel: "#c678dd", toolPendingBg: "#2c313c",
    toolSuccessBg: "#2f3a2f", toolErrorBg: "#3b2b2d", toolTitle: "#e5c07b", toolOutput: "#abb2bf",
    mdHeading: "#d19a66", mdLink: "#61afef", mdLinkUrl: "#5c6370", mdCode: "#98c379",
    mdCodeBlock: "#abb2bf", mdCodeBlockBorder: "#5c6370", mdQuote: "#5c6370", mdQuoteBorder: "#5c6370",
    mdHr: "#3e4452", mdListBullet: "#e5c07b", toolDiffAdded: "#8ca485", toolDiffRemoved: "#b87882",
    toolDiffContext: "#7f848e", syntaxComment: "#5c6370", syntaxKeyword: "#c678dd",
    syntaxFunction: "#61afef", syntaxVariable: "#e06c75", syntaxString: "#98c379",
    syntaxNumber: "#d19a66", syntaxType: "#e5c07b", syntaxOperator: "#56b6c2", syntaxPunctuation: "#abb2bf",
    thinkingOff: "#c678dd", thinkingMinimal: "#c678dd", thinkingLow: "#c678dd", thinkingMedium: "#c678dd",
    thinkingHigh: "#c678dd", thinkingXhigh: "#c678dd", thinkingMax: "#c678dd", bashMode: "#61afef",
    multitask: "#d17277",
  },
};

function createTheme(json: ThemeJson, mode: ColorMode, sourcePath?: string): Theme {
  const resolved = resolveThemeColors(withThemeColorFallbacks(json.colors), json.vars ?? {});
  const fgColors: Record<string, string | number> = {};
  const bgColors: Record<string, string | number> = {};
  for (const [key, value] of Object.entries(resolved)) {
    if (BG_KEYS.has(key)) bgColors[key] = value;
    else fgColors[key] = value;
  }
  return new Theme(fgColors as never, bgColors as never, mode, { name: json.name, sourcePath });
}

function userThemePath(name: string): string {
  return join(piAgentDir(), "themes", `${name}.json`);
}

export function loadTheme(name: string, mode?: ColorMode): Theme {
  const colorMode = mode ?? (getCapabilities().trueColor ? "truecolor" : "256color");
  try {
    const json = JSON.parse(readFileSync(userThemePath(name), "utf8")) as ThemeJson;
    return createTheme(json, colorMode, userThemePath(name));
  } catch {
    return createTheme(FALLBACK_ONEDARK, colorMode, undefined);
  }
}

let current: Theme | undefined;

/** Theme used when neither midas nor pi settings name one. */
export const DEFAULT_THEME_NAME = "onedark";

export function initTheme(name?: string): Theme {
  const themeName = name ?? loadPiSettings().theme ?? DEFAULT_THEME_NAME;
  current = loadTheme(typeof themeName === "string" ? themeName : DEFAULT_THEME_NAME);
  return current;
}

export function theme(): Theme {
  if (!current) current = initTheme();
  return current;
}

/**
 * Markdown theme for agent/user text. Driven by the loaded pi theme (onedark),
 * matching pi-agent's markdown colors.
 */
export function getMarkdownTheme(): MarkdownTheme {
  const t = theme();
  return {
    heading: (text) => t.bold(t.fg("mdHeading", text)),
    link: (text) => t.fg("mdLink", text),
    linkUrl: (text) => t.fg("mdLinkUrl", text),
    code: (text) => t.fg("mdCode", text),
    codeBlock: (text) => t.fg("mdCodeBlock", text),
    codeBlockBorder: (text) => t.fg("mdCodeBlockBorder", text),
    // Fenced code blocks get per-token colours from pi's syntax highlighter,
    // which maps highlight.js scopes onto the `syntax*` theme colours. Falls
    // back to the flat code-block colour when the language is unknown or pi's
    // theme singleton is not initialised yet.
    highlightCode: (code, lang) => {
      try {
        return highlightPiCode(code, lang);
      } catch {
        return code.split("\n").map((line) => t.fg("mdCodeBlock", line));
      }
    },
    quote: (text) => t.fg("mdQuote", text),
    quoteBorder: (text) => t.fg("mdQuoteBorder", text),
    hr: (text) => t.fg("mdHr", text),
    listBullet: (text) => t.fg("mdListBullet", text),
    bold: (text) => t.bold(t.fg("mdHeading", text)),
    italic: (text) => t.italic(text),
    underline: (text) => t.underline(text),
    strikethrough: (text) => t.strikethrough(text),
  };
}

export function getSelectListTheme(): SelectListTheme {
  const t = theme();
  return {
    selectedPrefix: (text) => t.fg("accent", text),
    selectedText: (text) => t.fg("accent", text),
    description: (text) => t.fg("muted", text),
    scrollInfo: (text) => t.fg("muted", text),
    noMatch: (text) => t.fg("muted", text),
  };
}

export function getEditorTheme(): EditorTheme {
  const t = theme();
  return {
    borderColor: (text) => t.fg("borderMuted", text),
    selectList: getSelectListTheme(),
  };
}

export interface SettingsListThemeLike {
  label: (text: string, selected: boolean) => string;
  value: (text: string, selected: boolean) => string;
  description: (text: string) => string;
  cursor: string;
  hint: (text: string) => string;
}

export function getSettingsListTheme(): SettingsListThemeLike {
  const t = theme();
  return {
    label: (text, selected) => (selected ? t.fg("accent", text) : text),
    value: (text, selected) => (selected ? t.fg("accent", text) : t.fg("muted", text)),
    description: (text) => t.fg("dim", text),
    cursor: t.fg("accent", "→ "),
    hint: (text) => t.fg("dim", text),
  };
}

export const USER_THEMES_DIR = join(homedir(), ".pi", "agent", "themes");
