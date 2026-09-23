#!/usr/bin/env node
/**
 * Recover the original TypeScript sources of the vendored pi-tui build.
 *
 * The published `third_party/pi-tui/dist` ships `.js.map` files whose
 * `sourcesContent` embeds the complete original TypeScript for every module.
 * That makes the Go port a transliteration with an exact, checkable reference
 * instead of a guess based on compiled output.
 *
 * Caveat: the vendored dist has local patches that postdate the source maps
 * (markdown heading handling, `roundedFrameRow`, editor image/file chips).
 * Where the two disagree, `dist/` is the source of truth. This script writes a
 * generated tree only; it never modifies the vendored package.
 *
 * Usage: node tools/parity/extract-pi-tui-sources.mjs [--check]
 *   --check  exit non-zero if the generated tree is missing or stale
 */
import { mkdirSync, readFileSync, readdirSync, rmSync, statSync, writeFileSync } from "node:fs";
// statSync is used by both the generator and --check mode.
import { dirname, join, normalize, relative, resolve } from "node:path";

const repoRoot = resolve(import.meta.dirname, "..", "..");
const distDir = join(repoRoot, "third_party", "pi-tui", "dist");
const outDir = join(repoRoot, "reference", "pi-tui");
const check = process.argv.includes("--check");

/** Every `.js.map` under the vendored dist, depth-first. */
function findMaps(dir) {
  const out = [];
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const full = join(dir, entry.name);
    if (entry.isDirectory()) out.push(...findMaps(full));
    else if (entry.name.endsWith(".js.map")) out.push(full);
  }
  return out;
}

function fail(message) {
  process.stderr.write(`extract-pi-tui-sources: ${message}\n`);
  process.exit(1);
}

if (!statSync(distDir, { throwIfNoEntry: false })?.isDirectory()) {
  fail(`no vendored dist at ${relative(repoRoot, distDir)}`);
}

const maps = findMaps(distDir);
if (maps.length === 0) fail("no .js.map files found");

/** @type {Map<string, string>} relative source path -> contents */
const sources = new Map();
for (const mapPath of maps) {
  const map = JSON.parse(readFileSync(mapPath, "utf8"));
  if (!Array.isArray(map.sourcesContent) || map.sourcesContent.length !== map.sources.length) {
    fail(`${relative(repoRoot, mapPath)} has no complete sourcesContent`);
  }
  for (let i = 0; i < map.sources.length; i += 1) {
    const content = map.sourcesContent[i];
    if (content === null || content === undefined) continue;
    // Sources are written relative to the map's own directory (`../src/x.ts`
    // or `../../src/components/x.ts`). Normalise the `.../src/` anchor so the
    // tree reconstructs as `src/x.ts`.
    const anchor = map.sources[i].indexOf("src/");
    if (anchor < 0) fail(`${relative(repoRoot, mapPath)} names a source outside src/: ${map.sources[i]}`);
    const rel = normalize(map.sources[i].slice(anchor));
    const previous = sources.get(rel);
    if (previous !== undefined && previous !== content) {
      fail(`conflicting contents for ${rel}`);
    }
    sources.set(rel, content);
  }
}

const files = [...sources.entries()].sort(([a], [b]) => a.localeCompare(b));

if (check) {
  if (!statSync(outDir, { throwIfNoEntry: false })?.isDirectory()) {
    fail(`${relative(repoRoot, outDir)} has not been generated yet; run without --check`);
  }
  let stale = 0;
  let missing = 0;
  for (const [rel, content] of files) {
    const target = join(outDir, rel);
    let current;
    try {
      current = readFileSync(target, "utf8");
    } catch {
      current = undefined;
    }
    if (current === undefined) {
      missing += 1;
    } else if (current !== content) {
      process.stderr.write(`stale: ${rel}\n`);
      stale += 1;
    }
  }
  if (stale > 0 || missing > 0) {
    fail(
      `${stale} generated file(s) out of date, ${missing} missing; re-run without --check`,
    );
  }
  process.stdout.write(`pi-tui reference is current (${files.length} files)\n`);
  process.exit(0);
}

rmSync(outDir, { recursive: true, force: true });
for (const [rel, content] of files) {
  const target = join(outDir, rel);
  mkdirSync(dirname(target), { recursive: true });
  writeFileSync(target, content, "utf8");
}

const totalLines = files.reduce((sum, [, content]) => sum + content.split("\n").length, 0);
process.stdout.write(
  `recovered ${files.length} modules (${totalLines} lines) from ${maps.length} source maps -> ${relative(repoRoot, outDir)}/src\n`,
);
