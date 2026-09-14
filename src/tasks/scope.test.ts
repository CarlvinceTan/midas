import test from "node:test";
import assert from "node:assert/strict";
import { scopesOverlap } from "./board.ts";

/** scopesOverlap is symmetric: assert both argument orders in one place. */
function assertOverlap(a: string[], b: string[]): void {
  assert.equal(scopesOverlap(a, b), true, `expected overlap for ${JSON.stringify(a)} vs ${JSON.stringify(b)}`);
  assert.equal(scopesOverlap(b, a), true, `expected overlap for ${JSON.stringify(b)} vs ${JSON.stringify(a)} (reversed)`);
}

function assertDisjoint(a: string[], b: string[]): void {
  assert.equal(scopesOverlap(a, b), false, `expected disjoint for ${JSON.stringify(a)} vs ${JSON.stringify(b)}`);
  assert.equal(scopesOverlap(b, a), false, `expected disjoint for ${JSON.stringify(b)} vs ${JSON.stringify(a)} (reversed)`);
}

test("wildcard patterns that both match a shared concrete path overlap", () => {
  // Both match src/a/foo.ts: `src/*/foo.ts` via `*` = "a", `src/a/*.ts` via `*` = "foo".
  assertOverlap(["src/*/foo.ts"], ["src/a/*.ts"]);
  // Nested stars, still intersecting at src/a/b/foo.ts.
  assertOverlap(["src/*/*/foo.ts"], ["src/a/*/*.ts"]);
  // Interleaved literals: src/a/b/c.ts matches both orderings of the stars.
  assertOverlap(["src/*/b/*.ts"], ["src/a/*/c.ts"]);
});

test("a directory scope intersects a glob that reaches inside it", () => {
  assertOverlap(["src/voice/"], ["src/*/tts.ts"]);
  assertOverlap(["src/voice/"], ["src/voice/*.ts"]);
  assertOverlap(["src/a/b/"], ["src/a/*/c.ts"]);
});

test("nested directory and exact descendant scopes overlap", () => {
  assertOverlap(["src/a/b/"], ["src/a/b/c/d.ts"]);
  assertOverlap(["src/a/b/"], ["src/a/b/c/"]);
  assertOverlap(["src/a/b/c/d.ts"], ["src/a/b/"]);
});

test("exact disjoint paths and disjoint literal prefixes stay disjoint", () => {
  assertDisjoint(["src/a.ts"], ["src/b.ts"]);
  assertDisjoint(["src/a/"], ["src/b/"]);
  assertDisjoint(["src/a/*.ts"], ["src/b/*.ts"]);
  assertDisjoint(["src/a/b/"], ["src/a/c/"]);
  // A directory does not cover its sibling that merely shares a text prefix.
  assertDisjoint(["src/voice/"], ["src/voicebox/tts.ts"]);
});

test("missing or empty scopes overlap everything in either position", () => {
  assertOverlap([], ["src/a.ts"]);
  assertOverlap(["src/a.ts"], []);
  assertOverlap([], []);
  assertOverlap([], ["src/voice/"]);
});

test("literal regex metacharacters are matched literally, not as a pattern", () => {
  assertOverlap(["src/a+b.ts"], ["src/a+b.ts"]);
  assertDisjoint(["src/a+b.ts"], ["src/ab.ts"]);
  assertOverlap(["src/(x)/"], ["src/(x)/y.ts"]);
  assertDisjoint(["src/(x)/"], ["src/x/y.ts"]);
  assertOverlap(["src/a.b.ts"], ["src/a.b.ts"]);
  assertDisjoint(["src/a.b.ts"], ["src/axb.ts"]);
  assertOverlap(["src/a$b.ts"], ["src/a$b.ts"]);
  assertDisjoint(["src/a$b.ts"], ["src/a/b.ts"]);
  assertOverlap(["src/a[1]/"], ["src/a[1]/index.ts"]);
});

test("'*' keeps crossing-slash semantics and matches empty runs", () => {
  assertOverlap(["src/*.ts"], ["src/a/b/c.ts"]);
  assertOverlap(["src/*"], ["src/a/b/"]);
  assertOverlap(["src/a*"], ["src/a"]);
  assertOverlap(["src/a*"], ["src/abc"]);
});

test("a trailing-slash subtree does not cover the bare directory path", () => {
  assertDisjoint(["src/voice/"], ["src/voice"]);
});
