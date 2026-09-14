import assert from "node:assert/strict";
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";
import { readCachedModels, writeCachedModels } from "./model-cache.ts";

test("model catalogs round-trip through the startup cache", () => {
  const directory = mkdtempSync(join(tmpdir(), "midas-model-cache-"));
  try {
    const models = [{
      providerID: "openai",
      modelID: "gpt-5.6-sol",
      name: "GPT-5.6 Sol",
      providerName: "OpenAI",
      cost: { input: 1, output: 2, cacheRead: 0.5 },
      contextLimit: 200_000,
      reasoning: true,
    }];
    writeCachedModels(models, directory);
    assert.deepEqual(readCachedModels(directory), models);
    assert.equal(JSON.parse(readFileSync(join(directory, "model-catalog.json"), "utf8")).version, 1);
  } finally {
    rmSync(directory, { recursive: true, force: true });
  }
});

test("invalid, oversized and empty catalogs are ignored", () => {
  const directory = mkdtempSync(join(tmpdir(), "midas-model-cache-"));
  const path = join(directory, "model-catalog.json");
  try {
    writeFileSync(path, JSON.stringify({ version: 1, models: [{ providerID: "openai" }] }));
    assert.deepEqual(readCachedModels(directory), []);
    writeFileSync(path, "x".repeat(5 * 1024 * 1024 + 1));
    assert.deepEqual(readCachedModels(directory), []);
    writeCachedModels([], directory);
    assert.equal(readFileSync(path, "utf8").length, 5 * 1024 * 1024 + 1);
  } finally {
    rmSync(directory, { recursive: true, force: true });
  }
});
