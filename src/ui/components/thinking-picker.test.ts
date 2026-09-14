import assert from "node:assert/strict";
import { test } from "node:test";
import { stripTerminalSequences } from "@earendil-works/pi-tui";
import { ThinkingPicker } from "./thinking-picker.ts";

function picker(title?: string): ThinkingPicker {
  return new ThinkingPicker(["low", "medium", "high"], "medium", "medium", () => {}, () => {}, () => {}, title);
}

test("thinking picker uses its standalone title by default", () => {
  const rendered = picker().render(50).map(stripTerminalSequences).join("\n");
  assert.match(rendered, /Thinking/);
  assert.doesNotMatch(rendered, /Model > Thinking/);
});

test("model flow identifies thinking as its second step", () => {
  const rendered = picker("Model > Thinking").render(50).map(stripTerminalSequences).join("\n");
  assert.match(rendered, /Model > Thinking/);
});
