import test from "node:test";
import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { existsSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { git } from "./board.ts";
import {
  SETTINGS_BASE,
  SETTINGS_COMBINED,
  SETTINGS_FILE,
  RESOLUTION_TOKENS,
  SMOKE_ENV,
  cleanupFixture,
  createFixture,
  runDeterministicSmoke,
} from "../../scripts/merge-agent-smoke.ts";

const REPO_ROOT = fileURLToPath(new URL("../../", import.meta.url));

/**
 * These tests exercise the fixture, cleanup and failure propagation with an
 * injected deterministic resolver. They never make a model call; the real merge
 * agent is reached only by the opt-in CLI in `scripts/merge-agent-smoke.ts`.
 */

test("fixture is an independently initialized clean repository", (t) => {
  const fixture = createFixture();
  t.after(() => cleanupFixture(fixture));

  assert.equal(git(fixture.root, "rev-parse", "--is-inside-work-tree"), "true");
  assert.equal(git(fixture.root, "symbolic-ref", "--short", "HEAD"), "main");
  assert.equal(readFileSync(join(fixture.root, SETTINGS_FILE), "utf8"), SETTINGS_BASE);
  assert.equal(git(fixture.root, "status", "--porcelain"), "");
  assert.notEqual(git(fixture.root, "rev-parse", "HEAD"), git(REPO_ROOT, "rev-parse", "HEAD"),
    "the fixture must not share history with the invoking repository");
});

test("deterministic resolver verifies the real conflict, combined content and ancestry", async () => {
  const evidence = await runDeterministicSmoke(async (_task, cwd) => {
    writeFileSync(join(cwd, SETTINGS_FILE), SETTINGS_COMBINED);
  });

  assert.equal(evidence.ok, true, evidence.error);
  assert.equal(evidence.mode, "deterministic-resolver");
  assert.equal(evidence.resolver.defaultResolver, false);
  assert.deepEqual(evidence.conflict?.conflicted, [SETTINGS_FILE]);
  assert.match(evidence.conflict?.markerSnippet ?? "", /<<<<<<</);
  assert.equal(evidence.conflict?.rootUnchangedByProbe, true);
  assert.equal(evidence.mergeStatus, "merged");
  assert.equal(evidence.resolution?.markersAbsent, true);
  for (const token of RESOLUTION_TOKENS) assert.equal(evidence.resolution?.tokensPresent[token], true, token);
  assert.equal(evidence.ancestry?.ok, true);
  assert.deepEqual(evidence.ancestry?.parents, [evidence.ancestry?.base, evidence.ancestry?.result]);
  assert.equal(evidence.root?.clean, true);
  assert.equal(evidence.root?.advanced, true);
  assert.equal(evidence.source.unchanged, true);
  assert.equal(existsSync(evidence.fixtureRoot), false, "fixture must be cleaned up");
});

test("a resolver that drops the target setting fails integration validation", async () => {
  const evidence = await runDeterministicSmoke(async (_task, cwd) => {
    writeFileSync(join(cwd, SETTINGS_FILE), "profile=default retries=5 timeout=30\n");
  });

  assert.equal(evidence.ok, false);
  assert.equal(evidence.mergeStatus, "failed");
  assert.match(evidence.error ?? "", /Check failed/);
  assert.equal(existsSync(evidence.fixtureRoot), false, "fixture must be cleaned up after a failure");
});

test("a resolver that leaves conflict markers is rejected", async () => {
  const evidence = await runDeterministicSmoke(async (_task, cwd) => {
    writeFileSync(join(cwd, SETTINGS_FILE), "<<<<<<< HEAD\nprofile=prod retries=3 timeout=30\n=======\nprofile=default retries=5 timeout=30\n>>>>>>> task\n");
  });

  assert.equal(evidence.ok, false);
  assert.equal(evidence.mergeStatus, "failed");
  assert.match(evidence.error ?? "", /conflict markers|Check failed/);
});

test("a thrown resolver failure marks the merge failed and preserves the target", async () => {
  const evidence = await runDeterministicSmoke(async () => {
    throw new Error("deterministic resolver refused");
  });

  assert.equal(evidence.ok, false);
  assert.match(evidence.error ?? "", /deterministic resolver refused|Merge conflict unresolved/);
  assert.equal(evidence.mergeStatus, "failed");
  assert.equal(evidence.root?.headAfterMerge, evidence.ancestry?.base, "the target branch must not advance");
  assert.equal(evidence.root?.clean, true);
  assert.equal(existsSync(evidence.fixtureRoot), false, "fixture must be cleaned up after a failure");
});

test("the real merge-agent smoke is opt-in and reports a nonzero unavailable result", () => {
  const script = fileURLToPath(new URL("../../scripts/merge-agent-smoke.ts", import.meta.url));
  const env = { ...process.env };
  delete env[SMOKE_ENV];
  const result = spawnSync(process.execPath, ["--import", "tsx", script], {
    cwd: REPO_ROOT,
    env,
    encoding: "utf8",
    timeout: 60_000,
  });

  assert.notEqual(result.status, 0, "without the opt-in flag the CLI must exit nonzero");
  assert.match(`${result.stdout}${result.stderr}`, /unavailable/);
  assert.match(`${result.stdout}${result.stderr}`, new RegExp(SMOKE_ENV.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")));
});
