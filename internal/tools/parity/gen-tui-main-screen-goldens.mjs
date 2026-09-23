#!/usr/bin/env node
/**
 * Generate exact terminal-write traces for the regular-screen renderer.
 *
 * Usage: node tools/parity/gen-tui-main-screen-goldens.mjs [--check]
 */
import { createHash } from "node:crypto";
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { join, relative, resolve } from "node:path";
import { CURSOR_MARKER } from "../../third_party/pi-tui/dist/tui.js";
import { TuiMainScreen } from "../../third_party/pi-tui/dist/tui-main-screen.js";

const repoRoot = resolve(import.meta.dirname, "..", "..");
const outFile = join(repoRoot, "tui", "testdata", "main-screen-reference.json");
const check = process.argv.includes("--check");

class FakeTerminal {
  constructor(columns = 20, rows = 5) {
    this.columns = columns;
    this.rows = rows;
    this.kittyProtocolActive = false;
    this.log = [];
  }
  start(onInput, onResize) { this.onInput = onInput; this.onResize = onResize; this.log.push("start"); }
  stop() { this.log.push("stop"); }
  async drainInput() {}
  write(data) { this.log.push(`write:${data}`); }
  moveBy(lines) { this.log.push(`move:${lines}`); }
  hideCursor() { this.log.push("hideCursor"); }
  showCursor() { this.log.push("showCursor"); }
  clearLine() { this.log.push("clearLine"); }
  clearFromCursor() { this.log.push("clearFromCursor"); }
  clearScreen() { this.log.push("clearScreen"); }
  setTitle(title) { this.log.push(`title:${title}`); }
  setProgress(active) { this.log.push(`progress:${active}`); }
  takeLog() { return this.log.splice(0); }
}

class MutableComponent {
  constructor(lines = []) { this.lines = [...lines]; }
  render() { return [...this.lines]; }
  invalidate() {}
}

function rig(lines, { columns = 20, rows = 5, showHardwareCursor = false } = {}) {
  const terminal = new FakeTerminal(columns, rows);
  const component = new MutableComponent(lines);
  const tui = new TuiMainScreen(terminal, showHardwareCursor);
  tui.addChild(component);
  return { terminal, component, tui };
}

function state(tui) {
  return tui.captureRenderState();
}

function step(label, context, includeState = true) {
  const result = {
    label,
    log: context.terminal.takeLog(),
    fullRedraws: context.tui.fullRedraws,
  };
  if (includeState) result.state = state(context.tui);
  return result;
}

const scenarios = [];

{
  const context = rig(["one", "two"]);
  const steps = [];
  context.tui.renderNow();
  steps.push(step("first", context));
  context.tui.renderNow();
  steps.push(step("unchanged", context));
  context.component.lines = ["one", "TWO"];
  context.tui.renderNow();
  steps.push(step("replace-last", context));
  context.component.lines = ["one", "TWO", "three"];
  context.tui.renderNow();
  steps.push(step("append", context));
  context.component.lines = ["one"];
  context.tui.renderNow();
  steps.push(step("delete-suffix", context));
  scenarios.push({ name: "basic-diff", steps });
}

{
  const context = rig([`left${CURSOR_MARKER}`, "right"], { showHardwareCursor: true });
  const steps = [];
  context.tui.renderNow();
  steps.push(step("cursor-first", context));
  context.component.lines = ["left", `wide界${CURSOR_MARKER}`];
  context.tui.renderNow();
  steps.push(step("cursor-second", context));
  context.component.lines = ["left", "right"];
  context.tui.renderNow();
  steps.push(step("cursor-hidden", context));
  scenarios.push({ name: "hardware-cursor", steps });
}

{
  const context = rig(["width-sensitive"]);
  const steps = [];
  context.tui.renderNow();
  steps.push(step("initial", context));
  context.terminal.columns = 24;
  context.tui.renderNow();
  steps.push(step("width-change", context));
  context.terminal.rows = 7;
  context.tui.renderNow();
  steps.push(step("height-change", context));
  scenarios.push({ name: "resize", steps });
}

{
  const previous = process.env.TERMUX_VERSION;
  process.env.TERMUX_VERSION = "test";
  const context = rig(["zero", "one", "two", "three", "four", "five"], { rows: 3 });
  const steps = [];
  context.tui.renderNow();
  steps.push(step("initial", context));
  context.terminal.rows = 5;
  context.component.lines[5] = "FIVE";
  context.tui.renderNow();
  steps.push(step("termux-height-change", context));
  if (previous === undefined) delete process.env.TERMUX_VERSION;
  else process.env.TERMUX_VERSION = previous;
  scenarios.push({ name: "termux-resize", steps });
}

{
  const context = rig(["zero", "one", "two", "three"]);
  context.tui.setClearOnShrink(true);
  const steps = [];
  context.tui.renderNow();
  steps.push(step("initial", context));
  context.component.lines = ["zero", "one"];
  context.tui.renderNow();
  steps.push(step("clear-on-shrink", context));
  scenarios.push({ name: "clear-on-shrink", steps });
}

{
  const context = rig(["zero", "one", "two", "three"]);
  context.tui.setClearOnShrink(true);
  const overlay = new MutableComponent(["hidden"]);
  const steps = [];
  context.tui.renderNow();
  steps.push(step("initial", context));
  const handle = context.tui.showOverlay(overlay);
  handle.setHidden(true);
  context.component.lines = ["zero", "one"];
  context.tui.renderNow();
  steps.push(step("hidden-overlay-entry", context));
  scenarios.push({ name: "shrink-with-overlay-entry", steps });
}

{
  const context = rig(Array.from({ length: 8 }, (_, index) => `line-${index}`), { rows: 3 });
  const steps = [];
  context.tui.renderNow();
  steps.push(step("initial", context));
  context.component.lines[0] = "changed-above-viewport";
  context.tui.renderNow();
  steps.push(step("change-above-viewport", context));
  scenarios.push({ name: "viewport-fallback", steps });
}

{
  const context = rig([]);
  const steps = [];
  context.tui.renderNow();
  steps.push(step("first-empty", context));
  context.tui.renderNow();
  steps.push(step("second-empty", context));
  scenarios.push({ name: "empty-renders", steps });
}

{
  const context = rig(["zero", "one", "two", "three", "four"], { rows: 3 });
  const steps = [];
  context.tui.renderNow();
  steps.push(step("initial", context));
  context.component.lines.push("five", "six", "seven");
  context.tui.renderNow();
  steps.push(step("append-beyond-viewport", context));
  scenarios.push({ name: "append-beyond-viewport", steps });
}

{
  const image = "\x1b_Ga=T,i=17,r=3;AAAA\x1b\\";
  const context = rig(["zero", "one", "two", "three", "four"], { rows: 3 });
  const steps = [];
  context.tui.renderNow();
  steps.push(step("initial", context));
  context.component.lines = ["zero", "one", "two", "three", image, "", ""];
  context.tui.renderNow();
  steps.push(step("partial-image-fallback", context));
  scenarios.push({ name: "kitty-partial-viewport", steps });
}

{
  const image = (id) => `\x1b_Ga=T,i=${id},r=3;AAAA\x1b\\`;
  const context = rig([image(7), "", "", "tail"], { rows: 5 });
  const steps = [];
  context.tui.renderNow();
  steps.push(step("initial-image", context));
  context.component.lines = [image(8), "", "", "tail"];
  context.tui.renderNow();
  steps.push(step("replace-image", context));
  context.terminal.columns = 21;
  context.tui.renderNow();
  steps.push(step("clear-image-on-width-change", context));
  scenarios.push({ name: "kitty-reserved-rows", steps });
}

{
  const complex = "\x1b_Ga=T,i=0x10,i=4294967295,i=0,i=1.5,r=2;AAAA\x1b\\";
  const context = rig([complex, ""], { rows: 3 });
  const steps = [];
  context.tui.renderNow();
  steps.push(step("parse-ids", context));
  context.terminal.columns = 21;
  context.tui.renderNow();
  steps.push(step("delete-valid-ids", context));
  scenarios.push({ name: "kitty-header-numbers", steps });
}

{
  const malformed = "\x1b_Ga=T,i=7,i=7,i=no,i=-1,i=4294967296,r=wat;AAAA\x1b\\";
  const context = rig([malformed]);
  const steps = [];
  context.tui.renderNow();
  steps.push(step("parse-malformed", context));
  context.terminal.columns = 21;
  context.tui.renderNow();
  steps.push(step("delete-once", context));
  scenarios.push({ name: "kitty-header-malformed", steps });
}

{
  const image = "\x1b_Ga=T,i=9,r=2;AAAA\x1b\\";
  const context = rig([image, "", "tail"]);
  const steps = [];
  context.tui.renderNow();
  steps.push(step("initial", context));
  const captured = context.tui.captureRenderState();
  context.component.lines = ["temporary"];
  context.tui.renderNow();
  steps.push(step("temporary", context));
  context.tui.restoreRenderState(captured);
  steps.push(step("restored", context));
  context.component.lines = [image, "", "tail"];
  context.tui.renderNow();
  steps.push(step("render-after-restore", context));
  scenarios.push({ name: "capture-restore", steps });
}

{
  const context = rig(["force"]);
  const steps = [];
  context.tui.renderNow();
  steps.push(step("initial", context));
  context.tui.renderNow(true);
  steps.push(step("forced", context));
  scenarios.push({ name: "forced-reset", steps });
}

for (const preserveScreen of [true, false]) {
  const context = rig(["one", "two"]);
  const steps = [];
  context.tui.renderNow();
  steps.push(step("render", context));
  context.tui.stop({ preserveScreen });
  steps.push(step("stop", context));
  scenarios.push({ name: preserveScreen ? "stop-preserve" : "stop-clean", steps });
}

function summarizeLog(log) {
  return log.map((entry) => {
    if (!entry.startsWith("write:")) return { event: entry };
    const data = entry.slice("write:".length);
    return {
      utf16Length: data.length,
      byteLength: Buffer.byteLength(data),
      sha256: createHash("sha256").update(data).digest("hex"),
      prefix: data.slice(0, 16),
      suffix: data.slice(-16),
    };
  });
}

{
  const prefixLength = 1024 * 1024 - 9;
  const context = rig([`${"a".repeat(prefixLength)}😀tail`], { columns: prefixLength + 20 });
  context.tui.renderNow();
  scenarios.push({
    name: "bounded-writes",
    steps: [{
      label: "astral-boundary",
      writeSummary: summarizeLog(context.terminal.takeLog()),
      fullRedraws: context.tui.fullRedraws,
    }],
  });
}

const golden = {
  generator: "tools/parity/gen-tui-main-screen-goldens.mjs",
  node: process.version,
  scenarios,
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
    process.stderr.write("gen-tui-main-screen-goldens: goldens are stale; re-run without --check\n");
    process.exit(1);
  }
  process.stdout.write("tui main-screen goldens are current\n");
  process.exit(0);
}

mkdirSync(join(repoRoot, "tui", "testdata"), { recursive: true });
writeFileSync(outFile, output, "utf8");
const count = scenarios.reduce((sum, scenario) => sum + scenario.steps.length, 0);
process.stdout.write(`wrote ${relative(repoRoot, outFile)}: ${count} differential cases\n`);
