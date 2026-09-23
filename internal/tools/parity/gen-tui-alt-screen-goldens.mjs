#!/usr/bin/env node
/**
 * Generate terminal-write and interaction traces for the alternate-screen TUI.
 *
 * Usage: node tools/parity/gen-tui-alt-screen-goldens.mjs [--check]
 */
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { join, relative, resolve } from "node:path";
import { CURSOR_MARKER } from "../../third_party/pi-tui/dist/tui.js";
import { TuiAltScreen } from "../../third_party/pi-tui/dist/tui-alt-screen.js";
import { KeybindingsManager, TUI_KEYBINDINGS, setKeybindings } from "../../third_party/pi-tui/dist/keybindings.js";
import {
  getCapabilities,
  registerKittyImageMetadata,
  setCapabilities,
} from "../../third_party/pi-tui/dist/terminal-image.js";

const repoRoot = resolve(import.meta.dirname, "..", "..");
const outFile = join(repoRoot, "tui", "testdata", "alt-screen-reference.json");
const check = process.argv.includes("--check");

class FakeTerminal {
  constructor(columns = 8, rows = 3) {
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
  constructor(lines = []) {
    this.lines = [...lines];
    this.invalidations = 0;
    this.inputs = [];
    this.mouseEvents = [];
  }
  render() { return [...this.lines]; }
  invalidate() { this.invalidations += 1; }
  handleInput(data) { this.inputs.push(data); }
}

class MouseComponent extends MutableComponent {
  handleMouse(event) {
    this.mouseEvents.push({
      type: event.type, button: event.button, x: event.x, y: event.y,
      screenX: event.screenX, screenY: event.screenY, width: event.width, height: event.height,
      shift: event.shift, alt: event.alt, ctrl: event.ctrl,
      ...(event.wheelDelta === undefined ? {} : { wheelDelta: event.wheelDelta }),
      ...(event.clickCount === undefined ? {} : { clickCount: event.clickCount }),
    });
    return { handled: true, focus: event.type === "press", capture: event.type === "press" };
  }
}

class CapabilityComponent extends MutableComponent {
  render() { return [getCapabilities().images ? "image" : "fallback"]; }
}

function bool(value) { return value; }

function rig(lines, {
  columns = 8,
  rows = 3,
  showHardwareCursor = false,
  mouse = false,
  component,
  tuiOptions = {},
} = {}) {
  const terminal = new FakeTerminal(columns, rows);
  const document = component ?? new MutableComponent(lines);
  const tui = new TuiAltScreen(terminal, showHardwareCursor, undefined, { mouse, ...tuiOptions });
  tui.addChild(document);
  return { terminal, component: document, tui };
}

function rendererState(context) {
  const capabilities = getCapabilities();
  const selection = context.tui.getSelectionBounds();
  const selectionPoint = (point) => point ? {
    row: point.row, col: point.col, boundary: point.boundary === true,
    scroll: point.scrollView === context.tui.implicitScrollView,
  } : null;
  return {
    previousScreen: [...context.tui.previousScreen],
    lastDocument: [...context.tui.lastDocument],
    previousScreenWidth: context.tui.previousScreenWidth,
    previousScreenHeight: context.tui.previousScreenHeight,
    viewportTop: context.tui.viewportTop,
    followingOutput: context.tui.isFollowingOutput,
    fullRedraws: context.tui.fullRedraws,
    altScreenActive: context.tui.altScreenActive,
    hasCurrentLayout: context.tui.currentLayout !== undefined,
    uploadedKittyImageIds: [...context.tui.uploadedKittyImages.keys()],
    imageProtocol: context.tui.imageProtocol ?? "",
    capabilityImages: capabilities.images ?? "",
    invalidations: context.component.invalidations,
    inputs: [...context.component.inputs],
    overlayInputs: [...(context.overlay?.inputs ?? [])],
    mouseEvents: [...(context.component.mouseEvents ?? [])],
    primaryScrollbarActive: context.tui.implicitScrollView.scrollbarActive,
    hasScrollbarDrag: context.tui.scrollbarDrag !== undefined,
    hasScrollbarHover: context.tui.scrollbarHover !== undefined,
    selection: selection ? { start: selectionPoint(selection.start), end: selectionPoint(selection.end) } : null,
    selectionText: context.tui.getActiveSelectionText() ?? null,
    selectionGranularity: context.tui.selectionGranularity,
    selectionPressActive: context.tui.selectionPressActive,
    selectionDragged: context.tui.selectionDragged,
    flashLines: context.tui.flashes.render(context.terminal.columns),
    openedUrls: [...(context.openedUrls ?? [])],
    copiedTexts: [...(context.copiedTexts ?? [])],
    search: context.tui.activeSearch ? {
      query: context.tui.activeSearch.query,
      matches: context.tui.activeSearch.matches,
      selectedIndex: context.tui.activeSearch.selectedIndex,
      selectedKey: context.tui.activeSearch.selectedKey ?? null,
      anchorRow: context.tui.activeSearch.anchorRow,
      selectionMode: context.tui.activeSearch.selectionMode,
      resultIndex: context.tui.activeSearch.component.resultIndex,
      resultCount: context.tui.activeSearch.component.resultCount,
      overlayFocused: context.tui.activeSearch.overlay?.isFocused() ?? false,
      overlayBounds: context.tui.activeSearch.overlay?.getBounds() ?? null,
    } : null,
  };
}

function step(label, context) {
  return { label, log: context.terminal.takeLog(), state: rendererState(context) };
}

function capabilities(images) {
  setCapabilities({ images, trueColor: true, hyperlinks: true });
}

function kittyLine(id, payload = "AAAA") {
  return `\x1b_Ga=T,f=100,q=2,C=1,c=2,r=1,i=${id};${payload}\x1b\\`;
}

const scenarios = [];

{
  capabilities(null);
  const context = rig(["one", "two"]);
  const steps = [];
  context.tui.start();
  steps.push(step("start", context));
  context.tui.renderNow();
  steps.push(step("first-frame", context));
  context.tui.stop({ preserveScreen: true });
  steps.push(step("stop-preserve", context));
  scenarios.push({ name: "lifecycle-preserve", steps });
}

{
  capabilities(null);
  const context = rig([`\x1b]133;A\x07abcdef${CURSOR_MARKER}`, "xy"], { columns: 4, rows: 2 });
  const steps = [];
  context.tui.start();
  context.terminal.takeLog();
  context.tui.renderNow();
  steps.push(step("frame", context));
  context.tui.stop({ preserveScreen: false });
  steps.push(step("stop-with-document", context));
  scenarios.push({ name: "lifecycle-final-document", steps });
}

{
  capabilities(null);
  const context = rig(["one", "two"], { columns: 8, rows: 3, showHardwareCursor: true });
  const steps = [];
  context.tui.start();
  context.terminal.takeLog();
  context.tui.renderNow();
  steps.push(step("first", context));
  context.tui.renderNow();
  steps.push(step("unchanged", context));
  context.component.lines = ["ONE", `two${CURSOR_MARKER}`];
  context.tui.renderNow();
  steps.push(step("changed-with-cursor", context));
  context.terminal.columns = 4;
  context.tui.renderNow();
  steps.push(step("width-change", context));
  context.terminal.rows = 2;
  context.tui.renderNow();
  steps.push(step("height-change", context));
  context.tui.stop({ preserveScreen: true });
  context.terminal.takeLog();
  scenarios.push({ name: "differential-and-resize", steps });
}

{
  capabilities(null);
  const context = rig(["zero", "one", "two", "three", "four", "five"], { rows: 3 });
  const steps = [];
  context.tui.start();
  context.terminal.takeLog();
  context.tui.renderNow();
  steps.push(step("follow-end", context));
  context.tui.scrollBy(-2);
  context.tui.renderNow();
  steps.push(step("scroll-up", context));
  context.component.lines.push("six");
  context.tui.renderNow();
  steps.push(step("growth-while-detached", context));
  context.tui.scrollToBottom();
  context.tui.renderNow();
  steps.push(step("return-to-bottom", context));
  context.tui.stop({ preserveScreen: true });
  context.terminal.takeLog();
  scenarios.push({ name: "implicit-follow-end", steps });
}

{
  capabilities(null);
  const context = rig(["implicit"]);
  const explicit = new MutableComponent(["explicit", `cursor${CURSOR_MARKER}`]);
  const steps = [];
  context.tui.start();
  context.terminal.takeLog();
  context.tui.renderNow();
  steps.push(step("implicit", context));
  context.tui.setLayoutRoot(explicit);
  context.tui.renderNow();
  steps.push(step("explicit", context));
  context.tui.setLayoutRoot(undefined);
  context.tui.renderNow();
  steps.push(step("implicit-again", context));
  context.tui.stop({ preserveScreen: true });
  context.terminal.takeLog();
  scenarios.push({ name: "explicit-layout-root", steps });
}

{
  capabilities("kitty");
  registerKittyImageMetadata({ imageId: 101, columns: 2, rows: 1, widthPx: 20, heightPx: 10 });
  const context = rig([kittyLine(101)]);
  const steps = [];
  context.tui.start();
  steps.push(step("start", context));
  context.tui.renderNow();
  steps.push(step("first-transmission", context));
  context.tui.renderNow(true);
  steps.push(step("placement-reuse", context));
  registerKittyImageMetadata({ imageId: 101, columns: 2, rows: 1, widthPx: 20, heightPx: 10 });
  context.tui.renderNow(true);
  steps.push(step("new-generation", context));
  context.component.lines = ["text"];
  context.tui.renderNow();
  steps.push(step("image-disappears", context));
  context.tui.stop({ preserveScreen: true });
  steps.push(step("stop", context));
  scenarios.push({ name: "kitty-reuse", steps });
}

{
  capabilities("kitty");
  const context = rig([],{ rows: 1 });
  const steps = [];
  context.tui.start();
  context.terminal.takeLog();
  for (let id = 200; id < 218; id++) {
    registerKittyImageMetadata({ imageId: id, columns: 2, rows: 1, widthPx: 20, heightPx: 10 });
    context.component.lines = [kittyLine(id)];
    context.tui.renderNow();
    steps.push(step(`image-${id}`, context));
  }
  context.tui.stop({ preserveScreen: true });
  context.terminal.takeLog();
  scenarios.push({ name: "kitty-lru-count", steps });
}

{
  capabilities("iterm2");
  const component = new CapabilityComponent();
  const context = rig([], { component });
  const steps = [];
  context.tui.start();
  steps.push(step("start-suppressed", context));
  context.tui.renderNow();
  steps.push(step("fallback-frame", context));
  context.tui.stop({ preserveScreen: true });
  steps.push(step("stop-restored", context));
  scenarios.push({ name: "iterm2-suppression", steps });
}

const savedEnvironment = Object.fromEntries(["TMUX", "ZELLIJ", "STY", "TERM"].map((key) => [key, process.env[key]]));
for (const multiplexed of [false, true]) {
  capabilities(null);
  delete process.env.TMUX;
  delete process.env.ZELLIJ;
  delete process.env.STY;
  process.env.TERM = "xterm-256color";
  if (multiplexed) process.env.TMUX = "1";
  const context = rig(["mouse"], { mouse: bool(true) });
  const steps = [];
  context.tui.start();
  steps.push(step("start", context));
  context.tui.stop({ preserveScreen: true });
  steps.push(step("stop", context));
  scenarios.push({ name: multiplexed ? "mouse-multiplexer" : "mouse-all-motion", steps });
}
for (const [key, value] of Object.entries(savedEnvironment)) {
  if (value === undefined) delete process.env[key];
  else process.env[key] = value;
}

{
  capabilities(null);
  setKeybindings(new KeybindingsManager(TUI_KEYBINDINGS, {
    "tui.altScreen.halfPageUp": "u",
    "tui.altScreen.lineDown": "d",
    "tui.altScreen.pageUp": ["pageUp", "x"],
    "tui.altScreen.top": ["home", "x"],
  }));
  const bel = "\x1b]133;A\x07";
  const st = "\x1b]133;A\x1b\\";
  const context = rig([
    `${bel}prompt-0`, "one", "two", `${st}prompt-3`, "four", "five",
    "six", "seven", `${bel}prompt-8`, "nine", "ten", "eleven",
  ], { rows: 6 });
  const steps = [];
  context.tui.start();
  context.terminal.takeLog();
  context.tui.setFocus(context.component);
  context.tui.renderNow();
  steps.push(step("initial", context));
  context.terminal.onInput("\x1b[5~");
  steps.push(step("page-up", context));
  context.terminal.onInput("\x1b[5;1:3~");
  steps.push(step("release-consumed", context));
  context.terminal.onInput("\x1b[5;1:2~");
  steps.push(step("repeat-page-up", context));
  context.terminal.onInput("u");
  steps.push(step("custom-half-page-up", context));
  context.terminal.onInput("d");
  steps.push(step("custom-line-down", context));
  context.terminal.onInput("\x1b[1;5A");
  steps.push(step("previous-prompt", context));
  context.terminal.onInput("\x1b[1;5B");
  steps.push(step("next-prompt", context));
  context.terminal.onInput("x");
  steps.push(step("conflict-page-before-top", context));
  context.terminal.onInput("\x1b[F");
  steps.push(step("bottom", context));
  context.terminal.onInput("z");
  steps.push(step("unmatched-forwarded", context));
  const overlay = new MutableComponent(["overlay"]);
  context.overlay = overlay;
  context.tui.showOverlay(overlay);
  context.terminal.onInput("\x1b[5~");
  steps.push(step("overlay-defers", context));
  context.tui.stop({ preserveScreen: true });
  context.terminal.takeLog();
  scenarios.push({ name: "keyboard-navigation", steps });
  setKeybindings(new KeybindingsManager(TUI_KEYBINDINGS));
}

{
  capabilities(null);
  const context = rig([
    "zero", "foo one", "two", "three", "four", "FOO five",
    "six", "seven", "eight", "foo nine",
  ], { columns: 40, rows: 6 });
  const steps = [];
  context.tui.start();
  context.terminal.takeLog();
  context.tui.renderNow();
  steps.push(step("initial", context));
  context.terminal.onInput("\x1b[102;6u");
  context.tui.renderNow();
  steps.push(step("open", context));
  context.terminal.onInput("foo");
  context.tui.renderNow();
  steps.push(step("query", context));
  context.terminal.onInput("\r");
  context.tui.renderNow();
  steps.push(step("next", context));
  context.terminal.onInput("\r");
  context.tui.renderNow();
  steps.push(step("next-wrap", context));
  context.terminal.onInput("\x1b[13;2u");
  context.tui.renderNow();
  steps.push(step("previous-wrap", context));
  context.terminal.onInput("\x1b[5~");
  steps.push(step("page-up-while-focused", context));
  context.terminal.onInput("\x03");
  steps.push(step("ctrl-c-does-not-close", context));
  context.terminal.onInput("\x1b");
  context.tui.renderNow();
  steps.push(step("close", context));
  context.terminal.onInput("\x1b[102;6u");
  context.tui.renderNow();
  const overlay = new MutableComponent(["other"]);
  context.overlay = overlay;
  context.tui.showOverlay(overlay);
  context.terminal.onInput("\r");
  steps.push(step("other-overlay-defers", context));
  context.terminal.onInput("\x1b[102;6u");
  context.tui.renderNow();
  steps.push(step("toggle-global-close", context));
  context.tui.stop({ preserveScreen: true });
  context.terminal.takeLog();
  scenarios.push({ name: "search-keyboard", steps });
}

{
  capabilities(null);
  const component = new MouseComponent(["zero", "one", "two"]);
  const context = rig([], { columns: 12, rows: 3, component });
  const steps = [];
  context.tui.start();
  context.terminal.takeLog();
  context.tui.renderNow();
  steps.push(step("initial", context));
  context.terminal.onInput("\x1b[<0;3;2M");
  steps.push(step("press-capture", context));
  context.terminal.onInput("\x1b[<32;5;2M");
  context.terminal.onInput("\x1b[<0;5;2m");
  steps.push(step("drag-release-no-click", context));
  for (let count = 1; count <= 4; count++) {
    context.terminal.onInput("\x1b[<0;3;2M");
    context.terminal.onInput("\x1b[<0;3;2m");
    steps.push(step(`click-${count}`, context));
  }
  context.terminal.onInput("\x1b[O");
  steps.push(step("focus-out", context));
  context.tui.stop({ preserveScreen: true });
  context.terminal.takeLog();
  scenarios.push({ name: "mouse-component-dispatch", steps });
}

{
  capabilities(null);
  const context = rig(["0", "1", "2", "3", "4", "5", "6", "7", "8", "9"], { columns: 12, rows: 4 });
  const steps = [];
  context.tui.start();
  context.terminal.takeLog();
  context.tui.renderNow();
  steps.push(step("initial", context));
  context.terminal.onInput("\x1b[<64;2;2M");
  steps.push(step("sgr-up", context));
  context.terminal.onInput("\x1b[<72;2;2M");
  steps.push(step("alt-up-five", context));
  context.terminal.onInput("\x1b[M" + String.fromCharCode(97, 34, 34));
  steps.push(step("x10-down", context));
  context.terminal.onInput("\x1b[<66;2;2M");
  steps.push(step("invalid-wheel-consumed", context));
  context.tui.stop({ preserveScreen: true });
  context.terminal.takeLog();
  scenarios.push({ name: "mouse-wheel", steps });
}

{
  capabilities(null);
  const context = rig(["0", "1", "2", "3", "4", "5", "6", "7", "8", "9"], { columns: 12, rows: 4 });
  context.tui.implicitScrollView.setScrollbar("always");
  const steps = [];
  context.tui.start();
  context.terminal.takeLog();
  context.tui.renderNow();
  steps.push(step("initial", context));
  context.terminal.onInput("\x1b[<35;12;1M");
  steps.push(step("hover", context));
  context.terminal.onInput("\x1b[<0;12;1M");
  steps.push(step("track-press", context));
  context.terminal.onInput("\x1b[<32;12;4M");
  steps.push(step("drag-bottom", context));
  context.terminal.onInput("\x1b[<0;12;4m");
  steps.push(step("release", context));
  context.terminal.onInput("\x1b[<35;1;1M");
  steps.push(step("hover-leave", context));
  context.tui.stop({ preserveScreen: true });
  context.terminal.takeLog();
  scenarios.push({ name: "mouse-scrollbar", steps });
}

{
  capabilities(null);
  const context = rig([
    "zero", "foo one", "two", "three", "four", "foo five",
    "six", "seven", "eight", "foo nine",
  ], { columns: 40, rows: 6 });
  const steps = [];
  context.tui.start();
  context.terminal.takeLog();
  context.tui.renderNow();
  context.terminal.onInput("\x1b[102;6u");
  context.terminal.onInput("foo");
  context.tui.renderNow();
  steps.push(step("query", context));
  let bounds = context.tui.activeSearch.overlay.getBounds();
  let x = bounds.col + context.tui.activeSearch.component.previousButtonStart;
  let y = bounds.row + 2;
  context.terminal.onInput(`\x1b[<35;${x + 1};${y + 1}M`);
  context.tui.renderNow();
  steps.push(step("hover-previous", context));
  context.terminal.onInput(`\x1b[<0;${x + 1};${y + 1}M`);
  context.tui.renderNow();
  steps.push(step("press-previous", context));
  bounds = context.tui.activeSearch.overlay.getBounds();
  x = bounds.col + context.tui.activeSearch.component.nextButtonStart;
  y = bounds.row + 2;
  context.terminal.onInput(`\x1b[<1;${x + 1};${y + 1}M`);
  context.tui.renderNow();
  steps.push(step("middle-does-not-navigate", context));
  context.terminal.onInput(`\x1b[<0;${x + 1};${y + 1}M`);
  context.tui.renderNow();
  steps.push(step("press-next", context));
  context.terminal.onInput("\x1b[<35;1;6M");
  context.tui.renderNow();
  steps.push(step("hover-leave", context));
  context.tui.stop({ preserveScreen: true });
  context.terminal.takeLog();
  scenarios.push({ name: "search-mouse-navigation", steps });
}

{
  capabilities(null);
  const start = "\x1b]777;pi-content-start\x07";
  const end = "\x1b]777;pi-content-end\x07";
  const decoration = "\x1b]777;pi-decoration\x07";
  const context = rig([
    `pad${start}  alpha  ${end} tail`,
    `${decoration}frame`,
    "\x1b[31mbeta界\x1b[0m",
  ], { columns: 20, rows: 3, tuiOptions: { copyOnSelect: false } });
  const steps = [];
  context.tui.start();
  context.terminal.takeLog();
  context.tui.renderNow();
  context.terminal.onInput("\x1b[<0;4;1M");
  context.tui.renderNow();
  steps.push(step("press", context));
  context.terminal.onInput("\x1b[<32;6;3M");
  context.tui.renderNow();
  steps.push(step("drag", context));
  context.terminal.onInput("\x1b[<0;6;3m");
  context.tui.renderNow();
  steps.push(step("release", context));
  context.tui.stop({ preserveScreen: true });
  context.terminal.takeLog();
  scenarios.push({ name: "selection-markers-and-wide", steps });
}

{
  capabilities(null);
  const context = rig(["path/to-kebab other"], { columns: 24, rows: 2, tuiOptions: { copyOnSelect: false } });
  const steps = [];
  context.tui.start();
  context.terminal.takeLog();
  context.tui.renderNow();
  for (let count = 1; count <= 4; count++) {
    context.terminal.onInput("\x1b[<0;7;1M");
    context.terminal.onInput("\x1b[<0;7;1m");
    context.tui.renderNow();
    steps.push(step(`click-${count}`, context));
  }
  context.tui.stop({ preserveScreen: true });
  context.terminal.takeLog();
  scenarios.push({ name: "selection-click-granularity", steps });
}

{
  capabilities(null);
  const context = rig(["copy me"], { columns: 20, rows: 2 });
  const steps = [];
  context.tui.start();
  context.terminal.takeLog();
  context.tui.renderNow();
  context.terminal.onInput("\x1b[<0;1;1M");
  context.terminal.onInput("\x1b[<32;7;1M");
  context.terminal.onInput("\x1b[<0;7;1m");
  context.tui.renderNow();
  steps.push(step("osc52-and-success-flash", context));
  context.tui.stop({ preserveScreen: true });
  context.terminal.takeLog();
  scenarios.push({ name: "selection-copy-fallback", steps });
}

{
  capabilities(null);
  const copiedTexts = [];
  const context = rig(["copy"], {
    columns: 20, rows: 2,
    tuiOptions: { copySelection: async (text) => { copiedTexts.push(text); return false; } },
  });
  context.copiedTexts = copiedTexts;
  const steps = [];
  context.tui.start();
  context.terminal.takeLog();
  context.tui.renderNow();
  await context.tui.copyTextToClipboard("Ω failed");
  context.tui.renderNow();
  steps.push(step("injected-failure", context));
  context.tui.stop({ preserveScreen: true });
  context.terminal.takeLog();
  scenarios.push({ name: "selection-copy-injected", steps });
}

{
  capabilities(null);
  const openedUrls = [];
  const link = "\x1b]8;;https://example.com\x07link\x1b]8;;\x07 plain";
  const context = rig([link], { columns: 24, rows: 2, tuiOptions: { openUrl: (url) => openedUrls.push(url), copyOnSelect: false } });
  context.openedUrls = openedUrls;
  const steps = [];
  context.tui.start();
  context.terminal.takeLog();
  context.tui.renderNow();
  context.terminal.onInput("\x1b[<0;2;1M");
  context.terminal.onInput("\x1b[<0;2;1m");
  context.tui.renderNow();
  steps.push(step("open-link", context));
  context.tui.stop({ preserveScreen: true });
  context.terminal.takeLog();
  scenarios.push({ name: "selection-link", steps });
}

{
  capabilities(null);
  const context = rig(["under"], { columns: 12, rows: 3 });
  const steps = [];
  context.tui.start();
  context.terminal.takeLog();
  context.tui.renderNow();
  context.tui.flash("one", 10_000, "\x1b[45m");
  context.tui.flash("message-too-long", 10_000, "\x1b[46m");
  context.tui.renderNow();
  steps.push(step("stacked", context));
  context.tui.stop({ preserveScreen: true });
  context.terminal.takeLog();
  scenarios.push({ name: "flash-stacking", steps });
}

capabilities(null);

const golden = {
  generator: "tools/parity/gen-tui-alt-screen-goldens.mjs",
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
    process.stderr.write("gen-tui-alt-screen-goldens: goldens are stale; re-run without --check\n");
    process.exit(1);
  }
  process.stdout.write("tui alt-screen goldens are current\n");
  process.exit(0);
}

mkdirSync(join(repoRoot, "tui", "testdata"), { recursive: true });
writeFileSync(outFile, output, "utf8");
const count = scenarios.reduce((sum, scenario) => sum + scenario.steps.length, 0);
process.stdout.write(`wrote ${relative(repoRoot, outFile)}: ${count} differential cases\n`);
