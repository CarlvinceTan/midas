#!/usr/bin/env node
/** Generate differential fixtures for key matching and keybinding resolution. */
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { join, relative, resolve } from "node:path";
import { isKeyRelease, matchesKey, setKittyProtocolActive } from "../../third_party/pi-tui/dist/keys.js";
import { KeybindingsManager } from "../../third_party/pi-tui/dist/keybindings.js";

const repoRoot = resolve(import.meta.dirname, "..", "..");
const outFile = join(repoRoot, "tui", "testdata", "key-reference.json");
const check = process.argv.includes("--check");

const candidates = [
  "escape", "enter", "shift+enter", "alt+enter", "tab", "shift+tab", "space", "ctrl+space", "alt+space",
  "backspace", "ctrl+backspace", "alt+backspace", "insert", "delete", "home", "end", "pageUp", "pageDown",
  "up", "down", "left", "right", "shift+up", "ctrl+up", "ctrl+shift+up", "alt+left", "ctrl+right",
  "f1", "f2", "f5", "f12", "a", "shift+a", "ctrl+a", "alt+a", "ctrl+alt+a", "super+a",
  "c", "ctrl+c", "ctrl+k", "ctrl+v", "ctrl+/", "ctrl+shift+d", "ctrl+-", "0", ".", "+",
];

const savedEnvironment = Object.fromEntries(["WT_SESSION", "SSH_CONNECTION", "SSH_CLIENT", "SSH_TTY"].map((key) => [key, process.env[key]]));

const specs = [
  ["escape", "\x1b"], ["enter-cr", "\r"], ["enter-lf-legacy", "\n"], ["shift-tab", "\x1b[Z"],
  ["legacy-up", "\x1b[A"], ["legacy-app-up", "\x1bOA"], ["legacy-home", "\x1b[7~"],
  ["legacy-page-up", "\x1b[[5~"], ["legacy-shift-up", "\x1b[a"], ["legacy-ctrl-up", "\x1bOa"],
  ["legacy-alt-left", "\x1bb"], ["legacy-ctrl-right", "\x1b[1;5C"],
  ["f1", "\x1bOP"], ["f5", "\x1b[[E"], ["f12", "\x1b[24~"],
  ["raw-a", "a"], ["raw-shift-a", "A"], ["raw-ctrl-a", "\x01"], ["raw-alt-a", "\x1ba"],
  ["raw-ctrl-alt-a", "\x1b\x01"], ["raw-ctrl-minus", "\x1f"],
  ["kitty-a", "\x1b[97u"], ["kitty-a-release", "\x1b[97;1:3u"], ["kitty-shift-a", "\x1b[65;2u"],
  ["kitty-ctrl-c-lock", "\x1b[99;69u"], ["kitty-modified-up", "\x1b[1;6A"],
  ["kitty-page-release", "\x1b[5;1:3~"], ["kitty-home", "\x1b[1;1H"],
  ["kitty-keypad-zero", "\x1b[57399u"], ["kitty-keypad-plus", "\x1b[57413u"],
  ["kitty-nonlatin-base", "\x1b[1089::99;5u"], ["kitty-latin-authoritative", "\x1b[107::118;5u"],
  ["kitty-symbol-authoritative", "\x1b[47::91;5u"],
  ["modify-ctrl-c", "\x1b[27;5;99~"], ["modify-shift-a", "\x1b[27;2;65~"],
  ["paste-release-lookalike", "\x1b[200~90:62:3F:A5\x1b[201~"],
];

const cases = [];
function record(name, data, kitty = false, environment = "default") {
  setKittyProtocolActive(kitty);
  delete process.env.WT_SESSION;
  delete process.env.SSH_CONNECTION;
  delete process.env.SSH_CLIENT;
  delete process.env.SSH_TTY;
  if (environment === "windows") process.env.WT_SESSION = "1";
  if (environment === "windows-ssh") {
    process.env.WT_SESSION = "1";
    process.env.SSH_CONNECTION = "remote";
  }
  cases.push({
    name,
    data,
    kitty,
    environment,
    release: isKeyRelease(data),
    matches: candidates.filter((candidate) => matchesKey(data, candidate)),
  });
}

for (const [name, data] of specs) record(name, data);
record("kitty-active-newline", "\n", true);
record("kitty-active-shift-enter-map", "\x1b\r", true);
record("legacy-alt-enter", "\x1b\r", false);
record("legacy-backspace-bs", "\x08", false);
record("windows-ctrl-backspace", "\x08", false, "windows");
record("windows-ssh-backspace", "\x08", false, "windows-ssh");
record("delete-char", "\x7f");

for (const [key, value] of Object.entries(savedEnvironment)) {
  if (value === undefined) delete process.env[key];
  else process.env[key] = value;
}
setKittyProtocolActive(false);

const definitions = {
  first: { defaultKeys: "a", description: "first" },
  second: { defaultKeys: ["b", "b"], description: "second" },
  third: { defaultKeys: [], description: "third" },
};

const manager = new KeybindingsManager(definitions, {
  first: [],
  second: ["x", "x"],
  third: "x",
  unknown: "z",
});

function normalizedConfig(config) {
  return Object.fromEntries(Object.entries(config)
    .filter(([, value]) => value !== undefined)
    .map(([id, value]) => [id, Array.isArray(value) ? value : [value]]));
}

function managerSnapshot(label) {
  return {
    label,
    first: manager.getKeys("first"),
    second: manager.getKeys("second"),
    third: manager.getKeys("third"),
    conflicts: manager.getConflicts(),
    user: normalizedConfig(manager.getUserBindings()),
    resolved: normalizedConfig(manager.getResolvedBindings()),
    matchesX: ["first", "second", "third"].filter((id) => manager.matches("x", id)),
  };
}

const registry = [managerSnapshot("initial")];
manager.setUserBindings({ first: ["ctrl+c", "ctrl+c"], second: undefined });
registry.push(managerSnapshot("updated"));

const golden = {
  generator: "tools/parity/gen-key-goldens.mjs",
  node: process.version,
  candidates,
  cases,
  registry,
};

const output = `${JSON.stringify(golden, null, 2)}\n`;
if (check) {
  let current;
  try { current = readFileSync(outFile, "utf8"); } catch { current = undefined; }
  if (current !== output) {
    process.stderr.write("gen-key-goldens: goldens are stale; re-run without --check\n");
    process.exit(1);
  }
  process.stdout.write("key goldens are current\n");
  process.exit(0);
}

mkdirSync(join(repoRoot, "tui", "testdata"), { recursive: true });
writeFileSync(outFile, output, "utf8");
process.stdout.write(`wrote ${relative(repoRoot, outFile)}: ${cases.length + registry.length} differential cases\n`);
