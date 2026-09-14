import { randomUUID } from "node:crypto";
import { mkdirSync, readFileSync, renameSync, rmSync, statSync, writeFileSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";
import type { ModelChoice } from "../opencode/session.ts";

const CACHE_VERSION = 1;
const CACHE_FILE = "model-catalog.json";
const MAX_CACHE_BYTES = 5 * 1024 * 1024;

export function modelCacheDir(): string {
  return process.env.MIDAS_CACHE_DIR ?? join(process.env.XDG_CACHE_HOME ?? join(homedir(), ".cache"), "midas");
}

function cachedModel(value: unknown): ModelChoice | undefined {
  if (!value || typeof value !== "object" || Array.isArray(value)) return undefined;
  const item = value as Record<string, unknown>;
  if (
    typeof item.providerID !== "string" || !item.providerID ||
    typeof item.modelID !== "string" || !item.modelID ||
    typeof item.name !== "string" || !item.name ||
    typeof item.providerName !== "string" || !item.providerName
  ) return undefined;

  const model: ModelChoice = {
    providerID: item.providerID,
    modelID: item.modelID,
    name: item.name,
    providerName: item.providerName,
  };
  const cost = item.cost;
  if (cost && typeof cost === "object" && !Array.isArray(cost)) {
    const prices = cost as Record<string, unknown>;
    if (typeof prices.input === "number" && Number.isFinite(prices.input) && typeof prices.output === "number" && Number.isFinite(prices.output)) {
      model.cost = {
        input: prices.input,
        output: prices.output,
        ...(typeof prices.cacheRead === "number" && Number.isFinite(prices.cacheRead) ? { cacheRead: prices.cacheRead } : {}),
        ...(typeof prices.cacheWrite === "number" && Number.isFinite(prices.cacheWrite) ? { cacheWrite: prices.cacheWrite } : {}),
      };
    }
  }
  if (typeof item.contextLimit === "number" && Number.isFinite(item.contextLimit)) model.contextLimit = item.contextLimit;
  if (typeof item.reasoning === "boolean") model.reasoning = item.reasoning;
  return model;
}

/** Last successful OpenCode model catalog, used only to make startup rendering instant. */
export function readCachedModels(directory = modelCacheDir()): ModelChoice[] {
  const path = join(directory, CACHE_FILE);
  try {
    if (statSync(path).size > MAX_CACHE_BYTES) return [];
    const parsed = JSON.parse(readFileSync(path, "utf8")) as { version?: unknown; models?: unknown };
    if (parsed.version !== CACHE_VERSION || !Array.isArray(parsed.models)) return [];
    return parsed.models.map(cachedModel).filter((model): model is ModelChoice => Boolean(model));
  } catch {
    return [];
  }
}

/** Atomically replace the cache so concurrent Midas launches never read a partial file. */
export function writeCachedModels(models: ModelChoice[], directory = modelCacheDir()): void {
  if (models.length === 0) return;
  const path = join(directory, CACHE_FILE);
  const temporary = `${path}.${process.pid}.${randomUUID()}.tmp`;
  try {
    mkdirSync(directory, { recursive: true, mode: 0o700 });
    writeFileSync(temporary, `${JSON.stringify({ version: CACHE_VERSION, models })}\n`, { mode: 0o600 });
    renameSync(temporary, path);
  } catch {
    try { rmSync(temporary, { force: true }); } catch { /* Best-effort cache cleanup. */ }
  }
}
