#!/usr/bin/env node

import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { join, relative, resolve } from "node:path";
import { detectCapabilities } from "../../third_party/pi-tui/dist/terminal-image.js";

const root = resolve(import.meta.dirname, "..", "..");
const outFile = join(root, "tui", "testdata", "terminal-capabilities-reference.json");
const check = process.argv.includes("--check");
const keys = [
  "TERM_PROGRAM", "TERMINAL_EMULATOR", "TERM", "COLORTERM", "TMUX", "KITTY_WINDOW_ID",
  "GHOSTTY_RESOURCES_DIR", "WEZTERM_PANE", "WARP_SESSION_ID", "WARP_TERMINAL_SESSION_UUID",
  "ITERM_SESSION_ID", "WT_SESSION", "PI_IMAGE_PROTOCOL", "PI_TRUE_COLOR", "PI_HYPERLINKS",
];
const saved = Object.fromEntries(keys.map((key) => [key, process.env[key]]));
const definitions = [
  ["unknown", { COLORTERM: "truecolor" }, false],
  ["tmux-hyperlinks", { TMUX: "1", COLORTERM: "24bit" }, true],
  ["tmux-no-hyperlinks", { TERM: "tmux-256color" }, false],
  ["screen", { TERM: "screen-256color", COLORTERM: "truecolor" }, true],
  ["kitty", { KITTY_WINDOW_ID: "1" }, false],
  ["ghostty", { TERM_PROGRAM: "ghostty" }, false],
  ["wezterm", { WEZTERM_PANE: "1" }, false],
  ["warp", { WARP_SESSION_ID: "1" }, false],
  ["iterm", { TERM_PROGRAM: "iTerm.app" }, false],
  ["windows-terminal", { WT_SESSION: "1" }, false],
  ["alacritty", { TERM_PROGRAM: "Alacritty" }, false],
  ["jetbrains", { TERMINAL_EMULATOR: "JetBrains-JediTerm" }, false],
  ["overrides", { TERM_PROGRAM: "kitty", PI_IMAGE_PROTOCOL: "none", PI_TRUE_COLOR: "0", PI_HYPERLINKS: "0" }, true],
  ["force-images", { PI_IMAGE_PROTOCOL: "iterm2", PI_TRUE_COLOR: "1", PI_HYPERLINKS: "1" }, false],
];
const cases = definitions.map(([name, env, tmuxHyperlinks]) => {
  for (const key of keys) delete process.env[key];
  Object.assign(process.env, env);
  const result = detectCapabilities(() => tmuxHyperlinks);
  return { name, env, tmuxHyperlinks, result: { images: result.images ?? "", trueColor: result.trueColor, hyperlinks: result.hyperlinks } };
});
for (const key of keys) {
  if (saved[key] === undefined) delete process.env[key]; else process.env[key] = saved[key];
}
const output = JSON.stringify({ cases }, null, 2) + "\n";
if (check) {
  const current = existsSync(outFile) ? readFileSync(outFile, "utf8") : "";
  if (current !== output) { console.error("gen-terminal-capabilities-goldens: goldens are stale; re-run without --check"); process.exit(1); }
  console.log("terminal capability goldens are current");
  process.exit(0);
}
mkdirSync(join(root, "tui", "testdata"), { recursive: true });
writeFileSync(outFile, output);
console.log(`wrote ${relative(root, outFile)}: ${cases.length} differential cases`);
