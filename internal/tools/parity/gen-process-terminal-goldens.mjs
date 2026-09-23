#!/usr/bin/env node

import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { join, relative, resolve } from "node:path";
import {
  ProcessTerminal,
  normalizeNativeShiftEnterInput,
  parseKeyboardProtocolNegotiationSequence,
  resolveEscapeTimeoutMs,
} from "../../third_party/pi-tui/dist/terminal.js";

const root = resolve(import.meta.dirname, "..", "..");
const outFile = join(root, "tui", "testdata", "process-terminal-reference.json");
const check = process.argv.includes("--check");
const savedWriteLog = process.env.PI_TUI_WRITE_LOG;
delete process.env.PI_TUI_WRITE_LOG;

function terminalScenario(name, run) {
  let output = "";
  const originalWrite = process.stdout.write;
  process.stdout.write = (chunk) => { output += String(chunk); return true; };
  try {
    const terminal = new ProcessTerminal();
    const input = [];
    terminal.inputHandler = (sequence) => input.push(sequence);
    const feed = (sequence) => {
      const negotiation = terminal.readKeyboardProtocolNegotiationSequence(sequence);
      if (negotiation === "pending") {
        terminal.scheduleKeyboardProtocolNegotiationBufferFlush();
      } else if (!terminal.handleKeyboardProtocolNegotiationSequence(negotiation)) {
        terminal.forwardInputSequence(sequence);
      }
    };
    return Promise.resolve(run(terminal, feed)).then(() => ({ name, output, input, kitty: terminal.kittyProtocolActive, modifyOtherKeys: terminal.modifyOtherKeysActive }));
  } finally {
    process.stdout.write = originalWrite;
  }
}

const scenarios = [];
scenarios.push(await terminalScenario("output-methods", (terminal) => {
  terminal.moveBy(2); terminal.moveBy(-1); terminal.moveBy(0);
  terminal.hideCursor(); terminal.showCursor(); terminal.clearLine(); terminal.clearFromCursor(); terminal.clearScreen(); terminal.setTitle("title");
  terminal.setProgress(true); terminal.setProgress(true); terminal.setProgress(false); terminal.write("payload");
}));
scenarios.push(await terminalScenario("kitty-split", (_terminal, feed) => {
  feed("\x1b[?1;2c"); feed("a"); feed("\x1b["); feed("?7u");
}));
scenarios.push(await terminalScenario("kitty-zero", (_terminal, feed) => { feed("\x1b[?0u"); }));
scenarios.push(await terminalScenario("invalid-split", (_terminal, feed) => { feed("\x1b["); feed("x"); }));
scenarios.push(await terminalScenario("prefix-timeout", async (_terminal, feed) => {
  feed("\x1b[");
  await new Promise((resolvePromise) => setTimeout(resolvePromise, 200));
}));
scenarios.push(await terminalScenario("stop-inactive", (terminal) => { terminal.stop(); }));
scenarios.push(await terminalScenario("stop-progress", (terminal) => { terminal.setProgress(true); terminal.stop(); }));

const parseInputs = ["\x1b[?7u", "\x1b[?0u", "\x1b[?1;2c", "\x1b[?c", "\x1b[", "a"];
const parsedNegotiation = parseInputs.map((input) => ({ input, result: parseKeyboardProtocolNegotiationSequence(input) ?? null }));
const escapeTimeouts = [
  ["default", {}], ["configured", { PI_TUI_ESC_TIMEOUT: "25" }], ["fraction", { PI_TUI_ESC_TIMEOUT: "1.5" }],
  ["invalid", { PI_TUI_ESC_TIMEOUT: "wat" }], ["infinity", { PI_TUI_ESC_TIMEOUT: "Infinity" }],
  ["ssh", { SSH_CONNECTION: "host" }], ["ssh-override", { SSH_TTY: "tty", PI_TUI_ESC_TIMEOUT: "7" }],
].map(([name, env]) => ({ name, env, result: resolveEscapeTimeoutMs(env) }));
const normalizedShiftEnter = [
  ["plain", "\r", false, true], ["shift", "\r", true, true], ["not-pressed", "\r", true, false], ["other", "a", true, true],
].map(([name, input, detect, pressed]) => ({ name, input, detect, pressed, result: normalizeNativeShiftEnterInput(input, detect, pressed) }));

const output = JSON.stringify({ scenarios, parsedNegotiation, escapeTimeouts, normalizedShiftEnter }, null, 2) + "\n";
if (savedWriteLog !== undefined) process.env.PI_TUI_WRITE_LOG = savedWriteLog;
if (check) {
  const current = existsSync(outFile) ? readFileSync(outFile, "utf8") : "";
  if (current !== output) { console.error("gen-process-terminal-goldens: goldens are stale; re-run without --check"); process.exit(1); }
  console.log("process terminal goldens are current");
  process.exit(0);
}
mkdirSync(join(root, "tui", "testdata"), { recursive: true });
writeFileSync(outFile, output);
console.log(`wrote ${relative(root, outFile)}: ${scenarios.length} scenarios`);
