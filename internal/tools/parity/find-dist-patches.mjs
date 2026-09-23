#!/usr/bin/env node
/**
 * Enumerate every local patch applied to the vendored pi-tui build.
 *
 * The vendored `dist` is compiled output, and its source maps embed the
 * *upstream* TypeScript — so the shipped JavaScript is upstream plus whatever
 * was changed locally afterwards. Guessing at those changes from comments or
 * export lists is unreliable: a patch can be behaviour-only and leave both
 * untouched.
 *
 * Two independent comparisons are made, and their combination is what makes the
 * result trustworthy:
 *
 *  1. CONTROL — the pristine published copy of the same pi-tui version that npm
 *     installed for pi-coding-agent. Comparing the vendored tree against it is a
 *     direct, compiler-free answer to "what was patched", because both trees
 *     were built by the vendor's own toolchain from the same upstream sources.
 *
 *  2. RECONSTRUCTION — compiling the recovered reference with the repo's
 *     TypeScript must reproduce the control byte for byte. That proves the
 *     recovered sources really are upstream and that the build settings are
 *     exact, which is what lets the 66 untouched modules be transliterated from
 *     the recovered source with confidence.
 *
 * If the control is unavailable the tool falls back to comparing the
 * reconstruction directly against the vendored tree, which is noisier: a
 * declaration-only difference can be a compiler artefact rather than a patch.
 * The report says which method was used.
 *
 * Usage:
 *   node tools/parity/find-dist-patches.mjs            # verify against baseline
 *   node tools/parity/find-dist-patches.mjs --update   # re-baseline
 *   node tools/parity/find-dist-patches.mjs --verbose  # print every diff
 */
import { execFileSync } from "node:child_process";
import { createHash } from "node:crypto";
import {
  cpSync,
  existsSync,
  mkdirSync,
  readFileSync,
  readdirSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { join, relative, resolve } from "node:path";

const repoRoot = resolve(import.meta.dirname, "..", "..");
const srcDir = join(repoRoot, "reference", "pi-tui", "src");
const buildDir = join(repoRoot, "reference", "pi-tui-build");
const vendoredDir = join(repoRoot, "third_party", "pi-tui");
const baselineFile = join(repoRoot, "tools", "parity", "dist-patches.json");
const diffDir = join(repoRoot, "reference", "pi-tui-patches");

/** Pristine published copies npm installed, in preference order. */
const CONTROL_CANDIDATES = [
  join(
    repoRoot,
    "node_modules",
    "@earendil-works",
    "pi-coding-agent",
    "node_modules",
    "@earendil-works",
    "pi-tui",
  ),
];

const update = process.argv.includes("--update");
const verbose = process.argv.includes("--verbose");

if (!existsSync(srcDir)) {
  process.stderr.write(
    "find-dist-patches: recovered sources missing; run tools/parity/extract-pi-tui-sources.mjs first\n",
  );
  process.exit(1);
}

function readVersion(dir) {
  try {
    return JSON.parse(readFileSync(join(dir, "package.json"), "utf8")).version;
  } catch {
    return undefined;
  }
}

const vendoredVersion = readVersion(vendoredDir);
const controlDir = CONTROL_CANDIDATES.find((dir) => {
  if (!existsSync(join(dir, "dist"))) return false;
  const version = readVersion(dir);
  if (version !== vendoredVersion) {
    process.stderr.write(
      `find-dist-patches: ignoring ${relative(repoRoot, dir)} (version ${version}, vendored is ${vendoredVersion})\n`,
    );
    return false;
  }
  return true;
});
const controlDist = controlDir ? join(controlDir, "dist") : undefined;

// ---------------------------------------------------------------------------
// Reconstruction: compile the recovered reference with the vendor's settings.
// Type errors are expected and irrelevant; only emit fidelity is under test.
// ---------------------------------------------------------------------------
const TSCONFIG = {
  compilerOptions: {
    target: "ES2022",
    module: "NodeNext",
    moduleResolution: "NodeNext",
    lib: ["ES2022"],
    strict: true,
    noUncheckedIndexedAccess: true,
    rewriteRelativeImportExtensions: true,
    verbatimModuleSyntax: true,
    skipLibCheck: true,
    declaration: true,
    sourceMap: false,
    outDir: "out",
    rootDir: "src",
  },
  include: ["src"],
};

rmSync(buildDir, { recursive: true, force: true });
mkdirSync(buildDir, { recursive: true });
cpSync(srcDir, join(buildDir, "src"), { recursive: true });
writeFileSync(join(buildDir, "tsconfig.json"), `${JSON.stringify(TSCONFIG, null, 2)}\n`);

const tsc = join(repoRoot, "node_modules", "typescript", "bin", "tsc");
if (!existsSync(tsc)) {
  process.stderr.write(`find-dist-patches: no TypeScript compiler at ${relative(repoRoot, tsc)}\n`);
  process.exit(1);
}
try {
  execFileSync(process.execPath, [tsc, "-p", join(buildDir, "tsconfig.json")], {
    stdio: ["ignore", "ignore", "pipe"],
  });
} catch (error) {
  // Type errors do not prevent emit; only a missing compiler or a crash matters.
  if (error.stderr) process.stderr.write(error.stderr.toString());
}
const outDir = join(buildDir, "out");

function walk(dir, suffix) {
  const out = [];
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const full = join(dir, entry.name);
    if (entry.isDirectory()) out.push(...walk(full, suffix));
    else if (entry.name.endsWith(suffix)) out.push(full);
  }
  return out;
}

/** Normalise a compiled file so only real content differences remain. */
function normalise(text) {
  return text
    .split("\n")
    .filter((line) => !line.includes("sourceMappingURL="))
    .map((line) => line.replace(/\s+$/, ""))
    .join("\n")
    .replace(/\n+$/, "");
}

/** Line-level diff, returning changed lines from each side. */
function diffLines(a, b) {
  const countLines = (lines) => {
    const counts = new Map();
    for (const line of lines) counts.set(line, (counts.get(line) ?? 0) + 1);
    return counts;
  };
  const left = a.split("\n");
  const right = b.split("\n");
  const rightCounts = countLines(right);
  const removed = [];
  for (const line of left) {
    const count = rightCounts.get(line) ?? 0;
    if (count > 0) rightCounts.set(line, count - 1);
    else removed.push(line);
  }
  const leftCounts = countLines(left);
  const added = [];
  for (const line of right) {
    const count = leftCounts.get(line) ?? 0;
    if (count > 0) leftCounts.set(line, count - 1);
    else added.push(line);
  }
  return { added, removed };
}

const modules = {};
const patchDetail = {};
rmSync(diffDir, { recursive: true, force: true });

for (const suffix of [".js", ".d.ts"]) {
  for (const built of walk(outDir, suffix)) {
    const rel = relative(outDir, built).split("\\").join("/");
    const emitted = normalise(readFileSync(built, "utf8"));
    const vendoredPath = join(vendoredDir, "dist", rel);
    const vendored = existsSync(vendoredPath)
      ? normalise(readFileSync(vendoredPath, "utf8"))
      : undefined;

    // Does the reconstruction agree with the pristine published build?
    let reconstruction = "no-control";
    if (controlDist) {
      const controlPath = join(controlDist, rel);
      const control = existsSync(controlPath)
        ? normalise(readFileSync(controlPath, "utf8"))
        : undefined;
      reconstruction = control === undefined ? "missing-in-control" : emitted === control ? "identical" : "differs";
    }

    // Is the vendored tree different from pristine? That is the patch.
    const patched = controlDist
      ? vendored !== undefined &&
        vendored !== normalise(readFileSync(join(controlDist, rel), "utf8"))
      : vendored !== undefined && emitted !== vendored;

    const hash = vendored === undefined ? "" : createHash("sha256").update(vendored).digest("hex").slice(0, 16);
    modules[rel] = {
      status: vendored === undefined ? "missing-from-vendored" : patched ? "patched" : "upstream",
      reconstruction,
      hash,
    };

    if (patched) {
      const { added, removed } = diffLines(
        controlDist ? normalise(readFileSync(join(controlDist, rel), "utf8")) : emitted,
        vendored,
      );
      patchDetail[rel] = { added: added.length, removed: removed.length };
      modules[rel].added = added.length;
      modules[rel].removed = removed.length;
      mkdirSync(diffDir, { recursive: true });
      writeFileSync(
        join(diffDir, rel.replace(/[\\/]/g, "__") + ".diff"),
        `--- pristine published (upstream)\n+++ vendored dist (patched)\n` +
          removed.map((l) => `- ${l}`).join("\n") +
          "\n" +
          added.map((l) => `+ ${l}`).join("\n") +
          "\n",
        "utf8",
      );
    }
  }
}

const patched = Object.entries(modules)
  .filter(([, m]) => m.status === "patched")
  .map(([name]) => name)
  .sort();
const reconstructionDrift = Object.entries(modules)
  .filter(([, m]) => m.reconstruction === "differs" || m.reconstruction === "missing-in-control")
  .map(([name, m]) => `${name} (${m.reconstruction})`)
  .sort();

const method = controlDist ? "pristine-control" : "reconstruction-only";

const current = {
  generator: "tools/parity/find-dist-patches.mjs",
  method,
  control: controlDist ? relative(repoRoot, controlDir) : null,
  note: "Files whose vendored dist differs from the pristine published build. For every entry here the dist is authoritative for the Go port; the other files can be transliterated from reference/pi-tui/src.",
  modules,
};

if (update || !existsSync(baselineFile)) {
  writeFileSync(baselineFile, `${JSON.stringify(current, null, 2)}\n`, "utf8");
  process.stdout.write(
    `find-dist-patches: recorded baseline via ${method} — ${patched.length} patched of ${Object.keys(modules).length} files\n` +
      patched.map((p) => `  ${p}\n`).join(""),
  );
  if (reconstructionDrift.length > 0) {
    process.stderr.write(
      `  note: reconstruction differs from pristine for ${reconstructionDrift.length} file(s):\n` +
        reconstructionDrift.slice(0, 10).map((l) => `    ${l}\n`).join(""),
    );
  }
  process.exit(0);
}

const baseline = JSON.parse(readFileSync(baselineFile, "utf8"));
const drift = [];
for (const [name, entry] of Object.entries(modules)) {
  const before = baseline.modules[name];
  if (!before) drift.push(`new file: ${name} (${entry.status})`);
  else if (before.status !== entry.status) drift.push(`${name}: ${before.status} -> ${entry.status}`);
  else if (before.hash !== entry.hash) drift.push(`${name}: content changed while still ${entry.status}`);
}
for (const name of Object.keys(baseline.modules)) {
  if (!modules[name]) drift.push(`removed file: ${name}`);
}

if (drift.length > 0) {
  process.stderr.write(
    "find-dist-patches: the patch set changed since the recorded baseline:\n" +
      drift.map((line) => `  ${line}\n`).join("") +
      "Review the diffs under reference/pi-tui-patches/, then re-baseline with --update if intended.\n",
  );
  process.exit(1);
}

process.stdout.write(
  `find-dist-patches: patch set matches baseline via ${method} — ${patched.length} patched of ${Object.keys(modules).length} files\n`,
);
if (reconstructionDrift.length > 0) {
  process.stderr.write(
    `find-dist-patches: warning — reconstruction differs from pristine for ${reconstructionDrift.length} file(s); the recovered sources or build settings may be inexact:\n` +
      reconstructionDrift.map((l) => `  ${l}\n`).join(""),
  );
}

if (verbose) {
  for (const name of patched) {
    const { added, removed } = patchDetail[name];
    process.stdout.write(`\n########## ${name}  (+${added}/-${removed}) ##########\n`);
    process.stdout.write(readFileSync(join(diffDir, name.replace(/[\\/]/g, "__") + ".diff"), "utf8"));
  }
}
