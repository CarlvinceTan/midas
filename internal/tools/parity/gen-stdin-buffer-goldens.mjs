#!/usr/bin/env node

import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { join, relative, resolve } from "node:path";
import { StdinBuffer } from "../../third_party/pi-tui/dist/stdin-buffer.js";

const root = resolve(import.meta.dirname, "..", "..");
const outFile = join(root, "tui", "testdata", "stdin-buffer-reference.json");
const check = process.argv.includes("--check");
const sleep = (ms) => new Promise((resolvePromise) => setTimeout(resolvePromise, ms));

async function run(name, chunks, options = {}, waitMs = 0) {
  const events = [];
  const buffer = new StdinBuffer(options);
  buffer.on("data", (data) => events.push({ type: "data", data }));
  buffer.on("paste", (data) => events.push({ type: "paste", data }));
  for (const chunk of chunks) buffer.process(chunk);
  if (waitMs > 0) await sleep(waitMs);
  const remainder = buffer.getBuffer();
  buffer.destroy();
  return { name, chunks, options, waitMs, events, remainder };
}

const cases = [
  await run("plain-unicode", ["ab界"]),
  await run("fragmented-csi-mouse", ["\x1b", "[<35;20", ";5m"]),
  await run("osc-dcs-apc-ss3", ["\x1b]11;rgb:aa/bb/cc\x07\x1bP>|v1\x1b\\\x1b_Gi=1;OK\x1b\\\x1bOA"]),
  await run("legacy-x10", ["\x1b[M", String.fromCharCode(96, 40, 41)]),
  await run("paste-and-remaining", ["x\x1b[200~hello", "\nworld\x1b[201~y"]),
  await run("partial-before-paste", ["\x1b\x1b[200~hello\x1b[201~"]),
  await run("wezterm-double-escape", ["\x1b\x1b[27;1:3u"]),
  await run("kitty-duplicate-printable", ["\x1b[97u", "a", "b"]),
  await run("escape-timeout", ["\x1b"], { escapeTimeout: 5, timeout: 5 }, 20),
  await run("sequence-timeout", ["\x1b[<35;2"], { escapeTimeout: 5, timeout: 5 }, 20),
];

const output = JSON.stringify({ cases }, null, 2) + "\n";
if (check) {
  const current = existsSync(outFile) ? readFileSync(outFile, "utf8") : "";
  if (current !== output) {
    process.stderr.write("gen-stdin-buffer-goldens: goldens are stale; re-run without --check\n");
    process.exit(1);
  }
  process.stdout.write("stdin buffer goldens are current\n");
  process.exit(0);
}
mkdirSync(join(root, "tui", "testdata"), { recursive: true });
writeFileSync(outFile, output);
process.stdout.write(`wrote ${relative(root, outFile)}: ${cases.length} differential cases\n`);
