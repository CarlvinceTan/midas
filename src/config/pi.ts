import { existsSync, mkdirSync, readFileSync, readdirSync, writeFileSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";
import { midasAgents } from "../agents/definitions.ts";

/** Global pi config directory (respects PI_CONFIG_DIR like pi does). */
export function piConfigDir(): string {
  return process.env.PI_CONFIG_DIR ?? join(homedir(), ".pi");
}

export function piAgentDir(): string {
  return join(piConfigDir(), "agent");
}

/** midas's own global config directory (`~/.midas`, override with MIDAS_CONFIG_DIR). */
export function midasConfigDir(): string {
  return process.env.MIDAS_CONFIG_DIR ?? join(homedir(), ".midas");
}

/** midas's project config directory (`<cwd>/.midas`). */
export function midasProjectDir(cwd: string): string {
  return join(cwd, ".midas");
}

/** `.agents` directories (global and project) used for skills/commands/context. */
export function agentsGlobalDir(): string {
  return join(homedir(), ".agents");
}

export function agentsProjectDir(cwd: string): string {
  return join(cwd, ".agents");
}

/**
 * The opencode config midas hands to `opencode serve`.
 *
 * Sources are scoped to midas's own directories so skills, MCP servers and
 * context come only from `~/.midas`, `<cwd>/.midas` and `<cwd>/.agents`:
 *   - `agent`: midas's own agent definitions (see `src/agents/definitions.ts`),
 *     so `~/.config/opencode/agents/*.md` is never required.
 *   - `skills`: the `skills/` dir under each root that exists.
 *   - `instructions`: the `AGENTS.md` under each root that exists.
 *   - every other key (notably `mcp`): merged from `midas.jsonc`/`midas.json`
 *     in each root, with `MIDAS_CONFIG_FILE` (if set) applied last.
 *
 * opencode's own discovery is turned off separately in `startServer`.
 */
export function midasOpencodeConfig(
  cwd: string,
  options: { disabledSkills?: ReadonlySet<string> } = {},
): Record<string, unknown> {
  // Midas owns its agents; a `midas.jsonc` in any root can still override them.
  const config: Record<string, unknown> = { agent: midasAgents() };
  const files = [
    join(midasConfigDir(), "midas.jsonc"),
    join(midasConfigDir(), "midas.json"),
    join(midasProjectDir(cwd), "midas.jsonc"),
    join(midasProjectDir(cwd), "midas.json"),
    join(agentsProjectDir(cwd), "midas.jsonc"),
    join(agentsProjectDir(cwd), "midas.json"),
    process.env.MIDAS_CONFIG_FILE,
  ].filter((value): value is string => Boolean(value));
  for (const file of files) {
    const parsed = readJsonc(file);
    if (parsed) mergeDeep(config, parsed);
  }

  const roots = [midasConfigDir(), midasProjectDir(cwd), agentsProjectDir(cwd)];
  // opencode reads `skills` (as `skills.paths`) only at server start, so a
  // session toggle takes a restart. With nothing disabled we keep passing the
  // roots (nested skills stay discoverable); once something is disabled we pass
  // the individual enabled skill folders instead.
  const disabled = options.disabledSkills;
  const skills =
    disabled && disabled.size > 0
      ? listSkills(cwd).filter((skill) => !disabled.has(skill.name)).map((skill) => skill.path)
      : roots.map((root) => join(root, "skills")).filter((dir) => existsSync(dir));
  if (skills.length > 0) config.skills = skills;
  const instructions = roots.map((root) => join(root, "AGENTS.md")).filter((file) => existsSync(file));
  if (instructions.length > 0) config.instructions = instructions;
  return config;
}

function readJson<T>(path: string): T | undefined {
  try {
    return JSON.parse(readFileSync(path, "utf8")) as T;
  } catch {
    return undefined;
  }
}

function isPlainObject(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

/** Recursively merge `patch` into `target`; arrays and scalars replace. */
function mergeDeep(target: Record<string, unknown>, patch: Record<string, unknown>): void {
  for (const [key, value] of Object.entries(patch)) {
    const current = target[key];
    if (isPlainObject(current) && isPlainObject(value)) mergeDeep(current, value);
    else target[key] = value;
  }
}

/** Read a `.json`/`.jsonc` file, tolerating comments and trailing commas. */
function readJsonc(path: string): Record<string, unknown> | undefined {
  try {
    const parsed = parseJsonc(readFileSync(path, "utf8"));
    return isPlainObject(parsed) ? parsed : undefined;
  } catch {
    return undefined;
  }
}

/**
 * Parse JSON-with-comments: strips `//` and block comments plus trailing commas
 * outside of strings. Hand-rolled so midas need not add a JSONC dependency just
 * to read its own `midas.jsonc`.
 */
export function parseJsonc(text: string): unknown {
  let out = "";
  let inString = false;
  let inLineComment = false;
  let inBlockComment = false;
  for (let i = 0; i < text.length; i++) {
    const ch = text[i]!;
    const next = text[i + 1];
    if (inLineComment) {
      if (ch === "\n") {
        inLineComment = false;
        out += ch;
      }
      continue;
    }
    if (inBlockComment) {
      if (ch === "*" && next === "/") {
        inBlockComment = false;
        i++;
      }
      continue;
    }
    if (inString) {
      out += ch;
      if (ch === "\\") {
        out += next ?? "";
        i++;
      } else if (ch === '"') {
        inString = false;
      }
      continue;
    }
    if (ch === '"') {
      inString = true;
      out += ch;
    } else if (ch === "/" && next === "/") {
      inLineComment = true;
      i++;
    } else if (ch === "/" && next === "*") {
      inBlockComment = true;
      i++;
    } else {
      out += ch;
    }
  }

  // Drop trailing commas: a comma followed by only whitespace then `}` or `]`.
  let clean = "";
  inString = false;
  for (let i = 0; i < out.length; i++) {
    const ch = out[i]!;
    if (inString) {
      clean += ch;
      if (ch === "\\") {
        clean += out[i + 1] ?? "";
        i++;
      } else if (ch === '"') {
        inString = false;
      }
      continue;
    }
    if (ch === '"') {
      inString = true;
      clean += ch;
      continue;
    }
    if (ch === ",") {
      let j = i + 1;
      while (j < out.length && /\s/.test(out[j]!)) j++;
      const following = out[j];
      if (following === "}" || following === "]") continue;
    }
    clean += ch;
  }
  return JSON.parse(clean);
}

/** Transcript/UI row padding is fixed at 1 (not configurable). */
export function rowPad(_cwd?: string): number {
  return 1;
}

export interface PiSettings {
  theme?: string;
  defaultThinkingLevel?: string;
  modelThinkingLevels?: Record<string, string>;
  hideThinkingBlock?: boolean;
  /** Command whose stdout streams JSONL STT events for `/voice`. */
  voiceSttCommand?: string;
  /** Warm the speech model at startup so `/voice` starts instantly (default true). */
  voicePreload?: boolean;
  /** Per-agent "Last Used" model ref (`provider/model`), used when no specific override is set. */
  agentLastUsed?: Record<string, string>;
  /** Per-agent reasoning level chosen alongside its specific model. */
  agentThinkingLevels?: Record<string, string>;
  [key: string]: unknown;
}

/**
 * Settings resolution order (later wins):
 * pi global -> pi project -> midas global (~/.midas) -> midas project (.midas)
 */
export function loadPiSettings(cwd?: string): PiSettings {
  const piGlobal = readJson<PiSettings>(join(piAgentDir(), "settings.json")) ?? {};
  const piProject = (cwd ? readJson<PiSettings>(join(cwd, ".pi", "settings.json")) : undefined) ?? {};
  const midasGlobal = readJson<PiSettings>(join(midasConfigDir(), "settings.json")) ?? {};
  const midasProject = (cwd ? readJson<PiSettings>(join(midasProjectDir(cwd), "settings.json")) : undefined) ?? {};
  return { ...piGlobal, ...piProject, ...midasGlobal, ...midasProject };
}

export function homePath(path: string): string {
  const home = homedir();
  return path.startsWith(home) ? "~" + path.slice(home.length) : path;
}

interface PiModel {
  id?: string;
  provider?: string;
  name?: string;
}

export interface LastSelectedModel {
  providerID: string;
  modelID: string;
  name?: string;
}

/** midas's own last-selected model (`~/.midas/last-selected-model.json`). */
function midasLastSelectedModelPath(): string {
  return join(midasConfigDir(), "last-selected-model.json");
}

/**
 * The last model used, preferring midas's own store and falling back to pi's last
 * interactively selected model so a shared install still resumes sensibly.
 */
export function readLastSelectedModel(): LastSelectedModel | undefined {
  const own = readJson<{ providerID?: string; provider?: string; modelID?: string; id?: string; name?: string }>(
    midasLastSelectedModelPath(),
  );
  const ownProvider = own?.providerID ?? own?.provider;
  const ownModel = own?.modelID ?? own?.id;
  if (ownProvider && ownModel) {
    return { providerID: ownProvider, modelID: ownModel, ...(own?.name ? { name: own.name } : {}) };
  }
  const pi = readJson<PiModel>(join(piAgentDir(), "last-selected-model.json"));
  if (pi?.provider && pi.id) {
    return { providerID: pi.provider, modelID: pi.id, ...(pi.name ? { name: pi.name } : {}) };
  }
  return undefined;
}

/** Remember the model chosen in midas so the next launch resumes with it. */
export function writeLastSelectedModel(model: LastSelectedModel): void {
  try {
    mkdirSync(midasConfigDir(), { recursive: true });
    writeFileSync(midasLastSelectedModelPath(), `${JSON.stringify(model, null, 2)}\n`);
  } catch {
    // Best-effort: selection still applies for the current session.
  }
}

/** Per-model thinking level override from pi settings. */
export function thinkingLevelFor(settings: PiSettings, providerID: string, modelID: string): string {
  const map = settings.modelThinkingLevels as Record<string, string> | undefined;
  const value = map?.[`${providerID}/${modelID}`];
  if (typeof value === "string" && value) return value;
  return typeof settings.defaultThinkingLevel === "string" ? settings.defaultThinkingLevel : "medium";
}

/** Merge one key into the global pi settings file. */
export function updateGlobalSetting(key: string, value: unknown): void {
  const path = join(midasConfigDir(), "settings.json");
  let settings: Record<string, unknown> = {};
  try {
    settings = JSON.parse(readFileSync(path, "utf8")) as Record<string, unknown>;
  } catch {
    settings = {};
  }
  settings[key] = value;
  writeFileSync(path, `${JSON.stringify(settings, null, 2)}\n`);
}

/** Persist a per-model thinking level into midas's global settings file. */
export function updateModelThinkingLevel(providerID: string, modelID: string, level: string): void {
  const path = join(midasConfigDir(), "settings.json");
  let settings: Record<string, unknown> = {};
  try {
    settings = JSON.parse(readFileSync(path, "utf8")) as Record<string, unknown>;
  } catch {
    settings = {};
  }
  const map = { ...((settings.modelThinkingLevels as Record<string, string>) ?? {}) };
  map[`${providerID}/${modelID}`] = level;
  settings.modelThinkingLevels = map;
  writeFileSync(path, `${JSON.stringify(settings, null, 2)}\n`);
}

/** Provider ids with stored opencode credentials. */
export function readAuthedProviders(): string[] {
  try {
    return Object.keys(JSON.parse(readFileSync(opencodeAuthPath(), "utf8")) as Record<string, unknown>);
  } catch {
    return [];
  }
}

function opencodeAuthPath(): string {
  const dataDir = process.env.XDG_DATA_HOME ?? join(homedir(), ".local", "share");
  return join(dataDir, "opencode", "auth.json");
}

/** Remove a provider's stored credentials from opencode's auth store. */
export function removeAuthedProvider(providerID: string): void {
  try {
    const store = JSON.parse(readFileSync(opencodeAuthPath(), "utf8")) as Record<string, unknown>;
    delete store[providerID];
    writeFileSync(opencodeAuthPath(), `${JSON.stringify(store, null, 2)}\n`);
  } catch {
    // Nothing stored or unreadable.
  }
}

export interface SkillEntry {
  name: string;
  path: string;
  /** Where the skill came from: user/global dirs vs the current project. */
  scope: "global" | "local";
}

/** The `name:` from a `SKILL.md`'s frontmatter, falling back to the folder name. */
function skillName(skillFile: string, fallback: string): string {
  try {
    const match = readFileSync(skillFile, "utf8").match(/^---\r?\n([\s\S]*?)\r?\n---/);
    if (match) {
      const line = match[1]!.split(/\r?\n/).find((entry) => entry.trimStart().toLowerCase().startsWith("name:"));
      if (line) {
        const value = line.slice(line.indexOf(":") + 1).trim().replace(/^["']|["']$/g, "");
        if (value) return value;
      }
    }
  } catch {
    // Unreadable; fall back to the folder name.
  }
  return fallback;
}

/**
 * Skills visible to midas: global `~/.midas/skills`, project `.midas/skills`,
 * and project `.agents/skills`. The global `~/.agents/skills` dir is not scanned.
 *
 * Only folders holding a `SKILL.md` count, named by its frontmatter, matching
 * opencode's own discovery so the list reflects what the agent can invoke.
 */
export function listSkills(cwd: string): SkillEntry[] {
  const dirs: Array<{ dir: string; scope: SkillEntry["scope"] }> = [
    { dir: join(midasProjectDir(cwd), "skills"), scope: "local" },
    { dir: join(midasConfigDir(), "skills"), scope: "global" },
    { dir: join(agentsProjectDir(cwd), "skills"), scope: "local" },
  ];
  const byName = new Map<string, SkillEntry>();
  for (const { dir, scope } of dirs) {
    let entries: import("node:fs").Dirent[] = [];
    try {
      entries = readdirSync(dir, { withFileTypes: true });
    } catch {
      continue;
    }
    for (const entry of entries) {
      if (entry.name.startsWith(".") || !entry.isDirectory()) continue;
      const skillFile = join(dir, entry.name, "SKILL.md");
      if (!existsSync(skillFile)) continue;
      const name = skillName(skillFile, entry.name);
      if (!byName.has(name)) byName.set(name, { name, path: join(dir, entry.name), scope });
    }
  }
  return [...byName.values()].sort((a, b) => a.name.localeCompare(b.name));
}
