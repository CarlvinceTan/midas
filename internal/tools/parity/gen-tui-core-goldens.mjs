#!/usr/bin/env node
/**
 * Generate differential goldens for the renderer-independent pi-tui core.
 *
 * Values come directly from the live vendored JavaScript build. In particular,
 * Box is imported from dist so the local OSC 777 content-marker patch is part
 * of the oracle.
 *
 * Usage: node tools/parity/gen-tui-core-goldens.mjs [--check]
 */
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { join, relative, resolve } from "node:path";
import { Box } from "../../third_party/pi-tui/dist/components/box.js";
import { HStack } from "../../third_party/pi-tui/dist/components/h-stack.js";
import { ScrollView } from "../../third_party/pi-tui/dist/components/scroll-view.js";
import { Spacer } from "../../third_party/pi-tui/dist/components/spacer.js";
import { allocateStackSizes } from "../../third_party/pi-tui/dist/components/stack.js";
import { VStack } from "../../third_party/pi-tui/dist/components/v-stack.js";
import {
  Container,
  CURSOR_MARKER,
  TuiBase,
  compositeTuiLine,
  dispatchMouseEvent,
} from "../../third_party/pi-tui/dist/tui.js";
import {
  getLayoutBoxesAt,
  getScrollbarGeometry,
  getScrollViewsAt,
  renderLayoutFrame,
} from "../../third_party/pi-tui/dist/layout.js";
import {
  getCellDimensions,
  registerKittyImageMetadata,
  setCapabilities,
} from "../../third_party/pi-tui/dist/terminal-image.js";
import { isKeyRelease, matchesKey } from "../../third_party/pi-tui/dist/keys.js";
import {
  isOsc11BackgroundColorResponse,
  parseOsc11BackgroundColor,
  parseTerminalColorSchemeReport,
} from "../../third_party/pi-tui/dist/terminal-colors.js";

const repoRoot = resolve(import.meta.dirname, "..", "..");
const outFile = join(repoRoot, "tui", "testdata", "core-reference.json");
const check = process.argv.includes("--check");

class Probe {
  constructor(id, renderFn, mouseFn) {
    this.id = id;
    this.renderFn = renderFn;
    this.mouseFn = mouseFn;
    this.renderWidths = [];
    this.mouseEvents = [];
    this.invalidations = 0;
  }
  render(width) {
    this.renderWidths.push(width);
    return this.renderFn(width);
  }
  invalidate() {
    this.invalidations += 1;
  }
  handleMouse(event) {
    this.mouseEvents.push({ ...event });
    return this.mouseFn?.(event);
  }
}

class InputContainer extends Container {
  constructor(id) {
    super();
    this.id = id;
  }
  handleInput() {}
}

const fixed = (id, lines, mouseFn) => new Probe(id, () => [...lines], mouseFn);
const widthProbe = (id, rows = 1) =>
  new Probe(id, (width) => Array.from({ length: rows }, (_, index) => `${id}:${width}:${index}`));

function baseEvent(overrides = {}) {
  return {
    type: "click",
    button: "left",
    x: 2,
    y: 1,
    screenX: 12,
    screenY: 7,
    width: 10,
    height: 4,
    shift: false,
    alt: false,
    ctrl: false,
    ...overrides,
  };
}

function serializeMouseResult(result) {
  if (!result) return { found: false };
  return {
    found: true,
    handled: !!result.handled,
    capture: !!result.capture,
    focus: !!result.focus,
    render: result.render ?? null,
    targetId: result.target?.component?.id ?? "",
    originX: result.target?.originX ?? 0,
    originY: result.target?.originY ?? 0,
    width: result.target?.width ?? 0,
    height: result.target?.height ?? 0,
    focusTargetId: result.focusTarget?.id ?? "",
  };
}

const mouse = [];
for (const [name, raw] of [
  ["undefined", undefined],
  ["render-only", { render: true }],
  ["handled", { handled: true, render: false }],
  ["capture-implies-handled", { capture: true }],
  ["focus-implies-handled", { focus: true }],
]) {
  const probe = fixed(`direct-${name}`, ["x"], () => raw);
  const event = baseEvent();
  mouse.push({ name, event, result: serializeMouseResult(dispatchMouseEvent(probe, event)), received: probe.mouseEvents[0] });
}

{
  const first = fixed("first", ["a", "b"]);
  const second = fixed("second", ["c"], (event) => ({ handled: true, focus: true, render: event.x >= 0 }));
  const container = new Container();
  container.id = "container";
  container.addChild(first);
  container.addChild(second);
  container.render(8);
  const event = baseEvent({ x: -2, y: 2, screenX: 20, screenY: 30, width: 8, height: 3 });
  mouse.push({
    name: "container-child-transform",
    event,
    result: serializeMouseResult(dispatchMouseEvent(container, event)),
    received: second.mouseEvents[0],
  });
}

{
  const child = fixed("focus-child", ["a"], () => ({ focus: true }));
  const container = new InputContainer("input-container");
  container.addChild(child);
  container.render(6);
  const event = baseEvent({ x: 1, y: 0, screenX: 4, screenY: 5, width: 6, height: 1 });
  mouse.push({
    name: "container-focus-delegation",
    event,
    result: serializeMouseResult(dispatchMouseEvent(container, event)),
    received: child.mouseEvents[0],
  });
}

{
  const child = fixed("box-child", ["a", "b"], () => ({ capture: true }));
  const box = new Box(2, 1);
  box.id = "box";
  box.addChild(child);
  box.render(10);
  const event = baseEvent({ x: 3, y: 2, screenX: 13, screenY: 12, width: 10, height: 4 });
  mouse.push({
    name: "box-content-transform",
    event,
    result: serializeMouseResult(dispatchMouseEvent(box, event)),
    received: child.mouseEvents[0],
  });
}

const containers = [];
{
  const container = new Container();
  container.addChild(fixed("a", ["a", "b"]));
  container.addChild(fixed("b", []));
  container.addChild(fixed("c", ["c"]));
  containers.push({ name: "concatenates-in-order", width: 7, out: container.render(7) });
}
{
  const duplicate = fixed("duplicate", ["d"]);
  const other = fixed("other", ["o"]);
  const container = new Container();
  container.addChild(duplicate);
  container.addChild(other);
  container.addChild(duplicate);
  container.removeChild(duplicate);
  containers.push({ name: "remove-first-identity-match", width: 5, out: container.render(5) });
}

const boxes = [];
function recordBox(name, box, width) {
  boxes.push({ name, width, out: box.render(width) });
}
recordBox("empty", new Box(), 10);
{
  const box = new Box(1, 2);
  box.addChild(fixed("empty-child", []));
  recordBox("empty-child", box, 10);
}
{
  const box = new Box(1, 1);
  box.addChild(fixed("plain", ["hi", "world"]));
  recordBox("patched-content-markers", box, 10);
}
{
  const box = new Box(0, 0);
  box.addChild(fixed("overwide", ["over-wide-value"]));
  recordBox("does-not-truncate", box, 5);
}
{
  const bg = (text) => `\x1b[44m${text}\x1b[49m`;
  const box = new Box(2, 1, bg);
  box.addChild(fixed("styled", ["\x1b[31mred\x1b[0m", "你"]));
  recordBox("background-and-wide-text", box, 12);
}
{
  let bgCalls = 0;
  const child = fixed("cache-child", ["cached"]);
  const box = new Box(1, 0, (text) => {
    bgCalls += 1;
    return `<${text}>`;
  });
  box.addChild(child);
  const first = box.render(9);
  const second = box.render(9);
  boxes.push({ name: "cache-sample", width: 9, out: second, first, childRenderCalls: child.renderWidths.length, bgCalls });
}

const spacer = [-2, 0, 1, 3].map((lines) => ({ lines, out: new Spacer(lines).render(20) }));

const allocationSpecs = [
  { name: "intrinsic-unbounded", entries: [{}, {}, {}], intrinsic: [2, 4, 1], available: null, gap: 0 },
  { name: "fixed-basis", entries: [{ basis: 3 }, { basis: 1 }], intrinsic: [20, 20], available: null, gap: 0 },
  { name: "negative-clamps", entries: [{ basis: -2, minSize: -4, maxSize: -1 }], intrinsic: [9], available: 8, gap: 0 },
  { name: "min-greater-than-max", entries: [{ basis: 1, minSize: 5, maxSize: 2 }], intrinsic: [1], available: 20, gap: 0 },
  { name: "gap-subtraction", entries: [{ grow: 1 }, { grow: 1 }, { grow: 1 }], intrinsic: [1, 1, 1], available: 10, gap: 2 },
  { name: "grow-weighted", entries: [{ grow: 1 }, { grow: 2 }, { grow: 3 }], intrinsic: [1, 1, 1], available: 17, gap: 0 },
  { name: "grow-with-max", entries: [{ grow: 1, maxSize: 2 }, { grow: 1 }], intrinsic: [1, 1], available: 8, gap: 0 },
  { name: "no-grow-candidates", entries: [{}, {}], intrinsic: [1, 1], available: 8, gap: 0 },
  { name: "shrink-size-weighted", entries: [{ shrink: 1 }, { shrink: 1 }, { shrink: 2 }], intrinsic: [8, 4, 4], available: 9, gap: 0 },
  { name: "shrink-with-min", entries: [{ shrink: 1, minSize: 7 }, { shrink: 1 }], intrinsic: [8, 8], available: 9, gap: 0 },
  { name: "no-shrink-candidates", entries: [{ shrink: 0 }, { shrink: 0 }], intrinsic: [5, 5], available: 3, gap: 0 },
  { name: "ordered-remainder", entries: [{ grow: 1 }, { grow: 1 }, { grow: 1 }], intrinsic: [0, 0, 0], available: 5, gap: 0 },
];
const allocation = allocationSpecs.map((spec) => ({
  ...spec,
  out: allocateStackSizes(spec.entries, spec.intrinsic, spec.available ?? undefined, spec.gap),
}));

function renderVStackCase(name) {
  if (name === "basic-gap") {
    const stack = new VStack([fixed("a", ["a", "aa"]), fixed("b", ["b"])], { gap: 1 });
    return { width: 6, out: stack.render(6) };
  }
  if (name === "basis-crop-pad") {
    const stack = new VStack([
      { component: fixed("a", ["a0", "a1", "a2"]), basis: 1 },
      { component: fixed("b", ["b0"]), basis: 3 },
    ]);
    return { width: 8, out: stack.render(8) };
  }
  if (name === "visibility-root-width") {
    const stack = new VStack([
      { component: fixed("small", ["small"]), visible: ({ width }) => width < 10 },
      { component: fixed("large", ["large"]), visible: ({ width }) => width >= 10 },
    ]);
    return { width: 12, out: stack.render(12) };
  }
  throw new Error(`unknown VStack case: ${name}`);
}

const vstacks = ["basic-gap", "basis-crop-pad", "visibility-root-width"].map((name) => ({
  name,
  ...renderVStackCase(name),
}));

function renderHStackCase(name) {
  if (name === "basic-gap") {
    const a = widthProbe("a", 2);
    const b = widthProbe("b", 1);
    const stack = new HStack(
      [
        { component: a, grow: 1 },
        { component: b, grow: 1 },
      ],
      { gap: 1 },
    );
    return { width: 20, out: stack.render(20), renderWidths: { a: a.renderWidths, b: b.renderWidths } };
  }
  if (name === "center-align") {
    const stack = new HStack([fixed("a", ["a0", "a1", "a2"]), fixed("b", ["b0"])], { gap: 1, align: "center" });
    return { width: 8, out: stack.render(8) };
  }
  if (name === "end-align") {
    const stack = new HStack([fixed("a", ["a0", "a1", "a2"]), fixed("b", ["b0"])], { align: "end" });
    return { width: 8, out: stack.render(8) };
  }
  if (name === "zero-width-child") {
    const zero = widthProbe("zero", 1);
    const full = widthProbe("full", 1);
    const stack = new HStack([
      { component: zero, basis: 0, shrink: 0 },
      { component: full, basis: 6, shrink: 0 },
    ]);
    return { width: 6, out: stack.render(6), renderWidths: { zero: zero.renderWidths, full: full.renderWidths } };
  }
  if (name === "wide-and-ansi") {
    const stack = new HStack([
      { component: fixed("wide", ["你好"]), basis: 4, shrink: 0 },
      { component: fixed("ansi", ["\x1b[31mred\x1b[0m"]), basis: 4, shrink: 0 },
    ]);
    return { width: 8, out: stack.render(8) };
  }
  throw new Error(`unknown HStack case: ${name}`);
}

const hstacks = ["basic-gap", "center-align", "end-align", "zero-width-child", "wide-and-ansi"].map((name) => ({
  name,
  ...renderHStackCase(name),
}));

const compositeInputs = [
  ["", "x", 0, 1, 5],
  ["abcde", "X", 2, 1, 5],
  ["abc", "XY", 5, 2, 8],
  ["\x1b[31mabcdef\x1b[0m", "XY", 2, 2, 6],
  ["\x1b]8;;https://example.com\x07abcdef\x1b]8;;\x07", "Z", 3, 1, 6],
  ["a你b好c", "XY", 2, 2, 7],
  ["abcdef", "你好", 1, 3, 6],
  ["abcdef", "over-wide", 1, 3, 6],
  ["\x1b_Ga=T,f=100;AAAA\x1b\\", "x", 0, 1, 5],
];
const composite = compositeInputs.map(([base, overlay, startCol, overlayWidth, totalWidth]) => ({
  base,
  overlay,
  startCol,
  overlayWidth,
  totalWidth,
  out: compositeTuiLine(base, overlay, startCol, overlayWidth, totalWidth),
}));

function scrollState(label, scrollView, renderRequests, extra = {}) {
  return {
    label,
    scrollTop: scrollView.scrollTop,
    followingEnd: scrollView.isFollowingEnd,
    viewportHeight: scrollView.viewportHeight,
    scrollbar: scrollView.scrollbar,
    scrollbarVisible: scrollView.isScrollbarVisible,
    scrollbarActive: scrollView.isScrollbarActive,
    contentWidth1: scrollView.getContentWidth(1),
    contentWidth5: scrollView.getContentWidth(5),
    renderRequests,
    ...extra,
  };
}

const scrollTraces = [];
{
  let renders = 0;
  const view = new ScrollView(fixed("follow-child", []), { follow: "end" });
  const states = [scrollState("initial", view, renders)];
  view.updateLayout(10, 3, () => { renders += 1; });
  states.push(scrollState("layout-10-3", view, renders));
  const unconsumed = view.scrollBy(-2);
  states.push(scrollState("scroll-up-2", view, renders, { unconsumed }));
  view.updateLayout(12, 3, () => { renders += 1; });
  states.push(scrollState("grow-12-3", view, renders));
  view.scrollToEnd();
  states.push(scrollState("to-end", view, renders));
  view.updateLayout(15, 3, () => { renders += 1; });
  states.push(scrollState("grow-15-3", view, renders));
  scrollTraces.push({ name: "follow-end", states });
}
{
  let renders = 0;
  const view = new ScrollView(fixed("suppressed-child", []), { follow: "end" });
  view.updateLayout(10, 3, () => { renders += 1; });
  const states = [scrollState("at-end", view, renders)];
  view.scrollTo(7, { disableFollow: true });
  states.push(scrollState("suppress-at-end", view, renders));
  view.updateLayout(12, 3, () => { renders += 1; });
  states.push(scrollState("growth-stays-put", view, renders));
  view.scrollTo(9);
  states.push(scrollState("explicit-end-restores-follow", view, renders));
  scrollTraces.push({ name: "suppressed-follow", states });
}
{
  let renders = 0;
  const view = new ScrollView(fixed("bounds-child", []));
  view.updateLayout(5, 2, () => { renders += 1; });
  const states = [scrollState("initial", view, renders)];
  let unconsumed = view.scrollBy(-4);
  states.push(scrollState("past-start", view, renders, { unconsumed }));
  unconsumed = view.scrollBy(10);
  states.push(scrollState("past-end", view, renders, { unconsumed }));
  view.updateLayout(1, 2, () => { renders += 1; });
  states.push(scrollState("content-shrinks", view, renders));
  scrollTraces.push({ name: "bounds-and-shrink", states });
}
{
  let renders = 0;
  const view = new ScrollView(fixed("auto-child", []), { scrollbar: "auto", scrollbarHideDelayMs: 60000 });
  view.updateLayout(8, 3, () => { renders += 1; });
  const states = [scrollState("initial", view, renders)];
  view.scrollBy(1);
  states.push(scrollState("activity", view, renders));
  view.setScrollbarActive(true);
  states.push(scrollState("active", view, renders));
  view.setScrollbar("always");
  states.push(scrollState("always", view, renders));
  view.setScrollbar("hidden");
  states.push(scrollState("hidden", view, renders));
  scrollTraces.push({ name: "scrollbar-modes", states });
}

function serializeLayoutBox(box) {
  return {
    id: box.component.id ?? "",
    rect: box.rect,
    clip: box.clip,
    lineOffset: box.lineOffset ?? 0,
    lines: box.lines ?? null,
    scrollViewId: box.scrollView?.id ?? "",
    layer: box.layer,
    children: box.children.map(serializeLayoutBox),
  };
}

function serializeLayoutCase(name, frame, extras = {}) {
  return {
    name,
    width: frame.width,
    height: frame.height,
    lines: frame.lines,
    primaryScrollViewId: frame.primaryScrollView?.id ?? "",
    root: serializeLayoutBox(frame.root),
    ...extras,
  };
}

const layouts = [];
{
  const leaf = fixed("cursor-leaf", ["zero", "one", "two", `three${CURSOR_MARKER}`]);
  const frame = renderLayoutFrame(leaf, 5, 2, () => {});
  layouts.push(serializeLayoutCase("leaf-cursor-clipping", frame, {
    hits: getLayoutBoxesAt(frame, 0, 1).map((box) => box.component.id ?? ""),
  }));
}
{
  const one = 1;
  const a = fixed("v-a", ["a0", "a1", "a2"]);
  const b = fixed("v-b", ["b0"]);
  const root = new VStack([
    { component: a, basis: one },
    { component: b, grow: 1 },
  ], { gap: 1 });
  root.id = "v-root";
  const frame = renderLayoutFrame(root, 6, 5, () => {});
  layouts.push(serializeLayoutCase("vstack-allocation", frame, {
    hits: getLayoutBoxesAt(frame, 0, 2).map((box) => box.component.id ?? ""),
  }));
}
{
  const a = fixed("h-a", ["a0", "a1", "a2"]);
  const b = fixed("h-b", ["b0"]);
  const root = new HStack([
    { component: a, basis: 3, shrink: 0 },
    { component: b, grow: 1 },
  ], { gap: 1, align: "center" });
  root.id = "h-root";
  const frame = renderLayoutFrame(root, 8, 4, () => {});
  layouts.push(serializeLayoutCase("hstack-alignment", frame, {
    hits: getLayoutBoxesAt(frame, 4, 1).map((box) => box.component.id ?? ""),
  }));
}
{
  const child = fixed("scroll-child", ["row0", "row1", "row2", "row3", "row4", "row5"]);
  const view = new ScrollView(child, { scrollbar: "always", primary: true });
  view.id = "scroll-always";
  const frame = renderLayoutFrame(view, 8, 3, () => {});
  const box = frame.root;
  layouts.push(serializeLayoutCase("scrollbar-always", frame, {
    geometry: getScrollbarGeometry(box) ?? null,
    scrollViewsAt: getScrollViewsAt(frame, 1, 1).map((scroll) => scroll.id ?? ""),
  }));
}
{
  const child = fixed("offset-child", ["row0", "row1", "row2", "row3", "row4"]);
  const view = new ScrollView(child);
  view.id = "scroll-offset";
  renderLayoutFrame(view, 6, 2, () => {});
  view.scrollBy(2);
  const frame = renderLayoutFrame(view, 6, 2, () => {});
  layouts.push(serializeLayoutCase("scroll-offset", frame, {
    scrollTop: view.scrollTop,
    hits: getLayoutBoxesAt(frame, 0, 0).map((box) => box.component.id ?? ""),
  }));
}
{
  const leaf = fixed("zone-leaf", ["\x1b]133;A\x07\x1b]133;B\x1b\\visible"]);
  const frame = renderLayoutFrame(leaf, 10, 1, () => {});
  layouts.push(serializeLayoutCase("strip-leading-osc133", frame));
}
{
  const shared = fixed("shared", ["same"]);
  const root = new VStack([shared, shared]);
  root.id = "cache-root";
  const frame = renderLayoutFrame(root, 5, 2, () => {});
  layouts.push(serializeLayoutCase("render-cache", frame, { renderWidths: shared.renderWidths }));
}
{
  const leaf = fixed("clamp-leaf", ["over"]);
  const frame = renderLayoutFrame(leaf, -2, 0, () => {});
  layouts.push(serializeLayoutCase("viewport-clamp", frame));
}
{
  registerKittyImageMetadata({ imageId: 42, columns: 4, rows: 3, widthPx: 40, heightPx: 30 });
  const image = "\x1b_Ga=T,i=42,c=4,r=3;AAAA\x1b\\";
  const leaf = fixed("image-leaf", [image]);
  const frame = renderLayoutFrame(leaf, 4, 1, () => {});
  layouts.push(serializeLayoutCase("kitty-image-crop", frame));
}

const terminalColorInputs = [
  "",
  "\x1b]11;#112233\x07",
  "\x1b]11; #abcdef \x1b\\",
  "\x1b]11;#111122223333\x07",
  "\x1b]11;rgb:f/00/8000\x07",
  "\x1b]11;rgba:ffff/8000/0000/ffff\x07",
  "\x1b]11;#bad\x07",
  "\x1b]11;rgb:zz/00/00\x07",
  "\x1b]11;#112233",
  "\x1b[?997;1n",
  "\x1b[?997;2n",
  "\x1b[?997;1n\x1b[?997;2n",
  "prefix\x1b[?997;1n",
].map((data) => {
  const color = parseOsc11BackgroundColor(data);
  const scheme = parseTerminalColorSchemeReport(data);
  return {
    data,
    isOsc11: isOsc11BackgroundColorResponse(data),
    colorFound: color !== undefined,
    color: color ?? { r: 0, g: 0, b: 0 },
    schemeFound: scheme !== undefined,
    scheme: scheme ?? "",
  };
});

const keyInputs = [
  "plain",
  "\x1b[100;6u",
  "\x1b[68;6u",
  "\x1b[100;6:3u",
  "\x1b[27;6;100~",
  "\x1b[100;5u",
  "\x1b[200~90:62:3F:A5\x1b[201~",
  "\x1b[65;2:3u",
  "\x1b[1;2:3A",
].map((data) => ({ data, release: isKeyRelease(data), debug: matchesKey(data, "shift+ctrl+d") }));

class FakeTerminal {
  constructor(columns = 20, rows = 10) {
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
}

class TestTui extends TuiBase {
  mode = "regular";
  renders = 0;
  doRender() { this.renders += 1; }
  compose(lines, width, height) { return this.compositeOverlays(lines, width, height); }
  cursor(lines, height) { return this.extractCursorPosition(lines, height); }
  resets(lines) { return this.applyLineResets(lines); }
  input(data) { return this.handleTerminalInput(data); }
  focusId() { return this.getFocusedComponent()?.id ?? ""; }
}

class FocusProbe extends Probe {
  constructor(id, lines, mouseFn) {
    super(id, () => [...lines], mouseFn);
    this.focused = false;
    this.inputs = [];
  }
  handleInput(data) { this.inputs.push(data); }
}

function overlayOptions(spec) {
  return { ...spec };
}

const overlays = [];
function recordOverlay(name, { termWidth = 20, termHeight = 10, baseLines = ["base"], overlayLines = ["overlay"], options = {}, hidden = false } = {}) {
  const terminal = new FakeTerminal(termWidth, termHeight);
  const tui = new TestTui(terminal);
  const overlay = new FocusProbe(`${name}-overlay`, overlayLines);
  const handle = tui.showOverlay(overlay, overlayOptions(options));
  if (hidden) handle.setHidden(true);
  const out = tui.compose(baseLines, termWidth, termHeight);
  overlays.push({
    name,
    out,
    boundsFound: handle.getBounds() !== undefined,
    bounds: handle.getBounds() ?? { row: 0, col: 0, width: 0, height: 0 },
    focusId: tui.focusId(),
    focused: overlay.focused,
    renderWidths: overlay.renderWidths,
  });
}

recordOverlay("default-center", { overlayLines: ["one", "two"] });
recordOverlay("top-left-margin", {
  overlayLines: ["x"],
  options: { width: 8, anchor: "top-left", margin: { top: 1, left: 2, right: 3, bottom: 1 } },
});
recordOverlay("percent-and-max-height", {
  termWidth: 30,
  termHeight: 10,
  overlayLines: ["0", "1", "2", "3", "4", "5", "6", "7"],
  options: { width: "50%", maxHeight: "50%", row: "25%", col: "100%" },
});
recordOverlay("invalid-percent-centers", {
  overlayLines: ["x", "y"],
  options: { width: "bad", row: "bad", col: "bad" },
});
recordOverlay("offsets-clamp", {
  overlayLines: ["x", "y", "z"],
  options: { width: 6, anchor: "bottom-right", offsetX: 50, offsetY: 50, margin: -3 },
});
recordOverlay("empty", { overlayLines: [] });
recordOverlay("long-base-bottom", {
  termWidth: 12,
  termHeight: 5,
  baseLines: Array.from({ length: 12 }, (_, index) => `base${index}`),
  overlayLines: ["bottom"],
  options: { width: 8, anchor: "bottom-left" },
});
recordOverlay("defensive-truncate", {
  termWidth: 10,
  termHeight: 4,
  overlayLines: ["over-wide-overlay-line"],
  options: { width: 5, anchor: "top-left" },
});
recordOverlay("hidden", { hidden: true, overlayLines: ["hidden"] });
recordOverlay("dynamic-invisible", {
  overlayLines: ["hidden"],
  options: { visible: (width) => width < 10 },
});

function focusSnapshot(label, tui, probes, handles = {}) {
  return {
    label,
    focusId: tui.focusId(),
    focused: Object.fromEntries(Object.entries(probes).map(([id, probe]) => [id, probe.focused])),
    hasOverlay: tui.hasOverlay(),
    hidden: Object.fromEntries(Object.entries(handles).map(([id, handle]) => [id, handle.isHidden()])),
  };
}

const focusTraces = [];
{
  const tui = new TestTui(new FakeTerminal());
  const base = new FocusProbe("base", ["base"]);
  const one = new FocusProbe("one", ["one"]);
  const two = new FocusProbe("two", ["two"]);
  tui.addChild(base);
  tui.setFocus(base);
  const states = [focusSnapshot("base", tui, { base, one, two })];
  const oneHandle = tui.showOverlay(one);
  states.push(focusSnapshot("show-one", tui, { base, one, two }, { one: oneHandle }));
  const twoHandle = tui.showOverlay(two, { nonCapturing: true });
  states.push(focusSnapshot("show-noncapturing-two", tui, { base, one, two }, { one: oneHandle, two: twoHandle }));
  twoHandle.focus();
  states.push(focusSnapshot("explicit-focus-two", tui, { base, one, two }, { one: oneHandle, two: twoHandle }));
  twoHandle.hide();
  states.push(focusSnapshot("hide-two", tui, { base, one, two }, { one: oneHandle }));
  tui.hideOverlay();
  states.push(focusSnapshot("hide-last", tui, { base, one, two }));
  focusTraces.push({ name: "stack-and-noncapturing", states });
}
{
  const tui = new TestTui(new FakeTerminal());
  const base = new FocusProbe("base", ["base"]);
  const blocker = new FocusProbe("blocker", ["blocker"]);
  const overlay = new FocusProbe("overlay", ["overlay"]);
  tui.addChild(base);
  tui.addChild(blocker);
  tui.setFocus(base);
  const handle = tui.showOverlay(overlay);
  const states = [focusSnapshot("overlay", tui, { base, blocker, overlay })];
  tui.setFocus(blocker);
  states.push(focusSnapshot("blocked", tui, { base, blocker, overlay }));
  tui.setFocus(null);
  states.push(focusSnapshot("nil-restores", tui, { base, blocker, overlay }));
  tui.setFocus(blocker);
  handle.unfocus({ target: base });
  states.push(focusSnapshot("explicit-target-pending", tui, { base, blocker, overlay }));
  tui.setFocus(null);
  states.push(focusSnapshot("explicit-target-resolves", tui, { base, blocker, overlay }));
  focusTraces.push({ name: "blocked-restore", states });
}
{
  const terminal = new FakeTerminal(20, 10);
  let visible = true;
  const tui = new TestTui(terminal);
  const base = new FocusProbe("base", ["base"]);
  const overlay = new FocusProbe("overlay", ["overlay"]);
  tui.addChild(base);
  tui.setFocus(base);
  const handle = tui.showOverlay(overlay, { visible: () => visible });
  const states = [focusSnapshot("visible", tui, { base, overlay }, { overlay: handle })];
  visible = false;
  tui.input("x");
  states.push(focusSnapshot("input-repairs-hidden", tui, { base, overlay }, { overlay: handle }));
  visible = true;
  tui.input("y");
  states.push(focusSnapshot("input-restores-visible", tui, { base, overlay }, { overlay: handle }));
  focusTraces.push({ name: "dynamic-visibility", states });
}

const cursorAndResets = [];
{
  const tui = new TestTui(new FakeTerminal());
  const lines = ["old" + CURSOR_MARKER, "middle", "\x1b[31mnew" + CURSOR_MARKER + "\x1b[0m"];
  const cursor = tui.cursor(lines, 2);
  cursorAndResets.push({
    name: "bottom-visible-cursor",
    cursorFound: cursor !== null,
    cursor: cursor ?? { row: 0, col: 0 },
    lines,
    resetLines: tui.resets(["a\tb", "\x1b_Ga=T;AAAA\x1b\\"]),
  });
}

const baseInputTraces = [];
{
  const terminal = new FakeTerminal();
  const tui = new TestTui(terminal);
  const focused = new FocusProbe("focused", ["focused"]);
  tui.addChild(focused);
  tui.setFocus(focused);
  const listenerLog = [];
  tui.addInputListener((data) => {
    listenerLog.push(`one:${data}`);
    if (data === "empty") return { data: "" };
    return { data: `${data}-one` };
  });
  tui.addInputListener((data) => {
    listenerLog.push(`two:${data}`);
    if (data.startsWith("consume")) return { consume: true };
    return { data: `${data}-two` };
  });
  let debugCount = 0;
  tui.onDebug = () => { debugCount += 1; };
  const schemes = [];
  tui.onTerminalColorSchemeChange((scheme) => schemes.push(scheme));

  tui.input("x");
  tui.input("empty");
  tui.input("consume");
  tui.input("\x1b[97;1:3u");
  focused.wantsKeyRelease = true;
  tui.input("\x1b[97;1:3u");
  tui.input("\x1b[100;6u");
  tui.input("\x1b[?997;2n");
  tui.input("\x1b[6;18;9t");

  const backgroundPromise = tui.queryTerminalBackgroundColor({ timeoutMs: 1000 });
  tui.input("\x1b]11;rgb:ffff/0000/8000\x07");
  const background = await backgroundPromise;
  const schemePromise = tui.queryTerminalColorScheme({ timeoutMs: 1000 });
  tui.input("\x1b[?997;1n");
  const queriedScheme = await schemePromise;

  baseInputTraces.push({
    name: "priority-and-transforms",
    listenerLog,
    inputs: focused.inputs,
    invalidations: focused.invalidations,
    debugCount,
    schemes,
    cellDimensions: getCellDimensions(),
    backgroundFound: background !== undefined,
    background: background ?? { r: 0, g: 0, b: 0 },
    queriedSchemeFound: queriedScheme !== undefined,
    queriedScheme: queriedScheme ?? "",
    terminalLog: terminal.log,
  });
}

class LifecycleTui extends TestTui {
  beforeTerminalStart() { this.terminal.log.push("hook:beforeStart"); }
  afterTerminalStart() { this.terminal.log.push("hook:afterStart"); }
  beforeTerminalStop(options) { this.terminal.log.push(`hook:beforeStop:${!!options.preserveScreen}`); }
  afterTerminalStop(options) { this.terminal.log.push(`hook:afterStop:${!!options.preserveScreen}`); }
  resetRenderState() { this.terminal.log.push("hook:reset"); }
  doRender() { this.terminal.log.push("hook:render"); this.renders += 1; }
}

const lifecycle = [];
{
  setCapabilities({ images: "kitty", trueColor: true, hyperlinks: true });
  const terminal = new FakeTerminal();
  const tui = new LifecycleTui(terminal, false);
  tui.setTerminalColorSchemeNotifications(true);
  tui.start();
  tui.renderNow(true);
  tui.stop({ preserveScreen: true });
  lifecycle.push({ name: "start-render-stop", log: terminal.log, renders: tui.renders });
  setCapabilities({ images: null, trueColor: false, hyperlinks: false });
}

const golden = {
  generator: "tools/parity/gen-tui-core-goldens.mjs",
  node: process.version,
  mouse,
  containers,
  boxes,
  spacer,
  allocation,
  vstacks,
  hstacks,
  composite,
  scrollTraces,
  layouts,
  terminalColorInputs,
  keyInputs,
  overlays,
  focusTraces,
  cursorAndResets,
  baseInputTraces,
  lifecycle,
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
    process.stderr.write("gen-tui-core-goldens: goldens are stale; re-run without --check\n");
    process.exit(1);
  }
  process.stdout.write("tui core goldens are current\n");
  process.exit(0);
}

mkdirSync(join(repoRoot, "tui", "testdata"), { recursive: true });
writeFileSync(outFile, output, "utf8");
const count = Object.values(golden).reduce((sum, value) => sum + (Array.isArray(value) ? value.length : 0), 0);
process.stdout.write(`wrote ${relative(repoRoot, outFile)}: ${count} differential cases\n`);
