import { truncateToWidth, visibleWidth } from "@earendil-works/pi-tui";
import type { ToolView } from "../../state/transcript.ts";
import { theme } from "../../theme/theme.ts";
import { isAbsolute, relative, sep } from "node:path";

/**
 * Compact Codex-style tool rows, ported from ~/.pi/agent/extensions/compact-tools.ts.
 *
 *   ⠋ Running `npm test`   ->   ✓ Ran `npm test`
 *   ⠋ Reading package.json ->   ✓ Read 42 lines in package.json
 *   ⠋ Editing src/a.ts     ->   ✓ Edited src/a.ts + 3 -1
 */

export const SPINNER_FRAMES = ["⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"];
const SPINNER_INTERVAL_MS = 80;
const EXPANDED_PREVIEW_LINES = 8;

/**
 * Connected MCP server names, set once the catalog loads. Opencode names MCP
 * tools `<server>_<tool>`, so this lets rows show "Server MCP: Tool" instead of
 * the raw combined id, matching the pi-agent MCP renderer.
 */
let mcpServers: string[] = [];

export function setMcpServerNames(names: readonly string[]): void {
  // Longest first so "Supabase_Midas" wins over a hypothetical "Supabase".
  mcpServers = [...names].sort((a, b) => b.length - a.length);
}

/** `davinci-resolve` -> "Davinci Resolve", `web_search` -> "Web Search". */
export function formatMcpDisplayName(value: string): string {
  return value
    .trim()
    .replace(/([a-z0-9])([A-Z])/g, "$1 $2")
    .replace(/[_-]+/g, " ")
    .replace(/\s+/g, " ")
    .split(" ")
    .filter(Boolean)
    .map((word) => `${word.charAt(0).toUpperCase()}${word.slice(1).toLowerCase()}`)
    .join(" ");
}

/** Split an MCP tool id into its server and tool parts using known servers. */
function mcpSplit(toolName: string): { server: string; tool: string } | undefined {
  for (const server of mcpServers) {
    if (toolName === server) return { server, tool: "" };
    if (toolName.startsWith(`${server}_`)) return { server, tool: toolName.slice(server.length + 1) };
  }
  return undefined;
}

/** Display name of the MCP server that owns a tool, without the tool suffix. */
export function mcpServerDisplayName(toolName: string): string | undefined {
  const split = mcpSplit(toolName);
  return split ? formatMcpDisplayName(split.server) : undefined;
}

/** "Davinci Resolve MCP: Folder" for an MCP tool id, or undefined if not MCP. */
function mcpTitle(toolName: string): string | undefined {
  const split = mcpSplit(toolName);
  if (!split) return undefined;
  const scope = `${formatMcpDisplayName(split.server)} MCP`;
  return split.tool ? `${scope}: ${formatMcpDisplayName(split.tool)}` : scope;
}

function currentFrame(now = Date.now()): string {
  return SPINNER_FRAMES[Math.floor(now / SPINNER_INTERVAL_MS) % SPINNER_FRAMES.length]!;
}

/** First word in the tool title color, the remainder muted. */
export function styleVerb(message: string): string {
  const separator = message.indexOf(" ");
  if (separator === -1) return theme().fg("toolTitle", message);
  return theme().fg("toolTitle", message.slice(0, separator)) + theme().fg("muted", message.slice(separator));
}

export function shortenMiddle(text: string, maxWidth: number): string {
  if (maxWidth <= 0) return "";
  if (visibleWidth(text) <= maxWidth) return text;
  const separator = text.includes("/") ? "/" : text.includes("\\") ? "\\" : "";
  const fallback = truncateToWidth("...", maxWidth, "");
  if (!separator) return fallback;
  const leading = text.match(separator === "/" ? /^\/+/ : /^\\+/)?.[0] ?? "";
  const trailing = text.endsWith(separator) ? separator : "";
  const bodyEnd = trailing ? text.length - trailing.length : text.length;
  const segments = text.slice(leading.length, bodyEnd).split(separator).filter((s) => s.length > 0);
  if (segments.length === 0) return fallback;
  let best = fallback;
  let bestScore = Number.POSITIVE_INFINITY;
  for (let head = 0; head <= segments.length; head++) {
    for (let tail = 0; head + tail < segments.length; tail++) {
      const collapsed = `${leading}${[...segments.slice(0, head), "...", ...(tail > 0 ? segments.slice(-tail) : [])].join(separator)}${trailing}`;
      if (visibleWidth(collapsed) > maxWidth) continue;
      const score = Math.abs(head - tail) * 1000 - (head + tail);
      if (score < bestScore) {
        bestScore = score;
        best = collapsed;
      }
    }
  }
  return best;
}

function displayPath(value: string | undefined, cwd: string, fallback = "(unknown path)"): string {
  if (!value) return fallback;
  const resolved = isAbsolute(value) ? value : `${cwd}${sep}${value}`;
  const rel = relative(cwd, resolved);
  const inside = rel === "" || (rel !== ".." && !rel.startsWith(`..${sep}`) && !isAbsolute(rel));
  return inside ? (rel === "" ? "." : rel) : value;
}

function text(value: unknown): string {
  if (typeof value === "string") return value;
  if (Array.isArray(value) && value.every((v) => typeof v === "string")) return value.join(" ");
  return "";
}

function normalizeCommand(command: string): string {
  return command.replace(/\s+/g, " ").trim() || "(empty command)";
}

function lineCount(value: string): number {
  const lines = value.replace(/\r\n/g, "\n").split("\n");
  while (lines.length > 0 && lines[lines.length - 1] === "") lines.pop();
  return lines.length;
}

function diffStats(source: unknown): { added: number; removed: number } | null {
  if (typeof source !== "string" || source.length === 0) return null;
  let added = 0;
  let removed = 0;
  for (const line of source.split("\n")) {
    if (line.startsWith("+") && !line.startsWith("+++")) added += 1;
    else if (line.startsWith("-") && !line.startsWith("---")) removed += 1;
  }
  return { added, removed };
}

function preview(value: string, maxLines = EXPANDED_PREVIEW_LINES): string[] {
  const lines = value.replace(/\r\n/g, "\n").replace(/\s+$/g, "").split("\n");
  if (lines.length === 1 && lines[0] === "") return [];
  const out = lines.slice(0, maxLines).map((line) => theme().fg("dim", line));
  if (lines.length > maxLines) out.push(theme().fg("muted", `... ${lines.length - maxLines} more lines`));
  return out;
}

function subagentName(tool: ToolView): string {
  return formatMcpDisplayName(text(tool.input.subagent_type) || "subagent");
}

function subagentTitle(tool: ToolView): string {
  const prompt = text(tool.input.prompt);
  return text(tool.input.description) || tool.title || prompt.split("\n").find((line) => line.trim())?.trim() || "Delegated task";
}

/** Strip OpenCode's transport wrapper before showing a completed child result. */
function subagentOutput(tool: ToolView): string {
  const output = tool.status === "error" ? tool.error ?? tool.output ?? "" : tool.output ?? "";
  const wrapped = output.match(/^\s*<task(?:\s[^>]*)?>\s*<task_result>\s*([\s\S]*?)\s*<\/task_result>\s*<\/task>\s*$/);
  return wrapped?.[1] ?? output;
}

/**
 * Adapt OpenCode's `task` tool to the compact Pi subagent presentation. A Task
 * is one child-agent invocation; OpenCode exposes its agent and short title in
 * the tool input rather than Pi's extension-specific result details.
 */
export function renderSubagentTool(
  tool: ToolView,
  width: number,
  expanded: boolean,
  spinner = currentFrame(),
): string[] {
  const t = theme();
  const active = tool.status === "pending" || tool.status === "running";
  const failed = tool.status === "error";
  const glyph = active ? t.fg("accent", spinner) : failed ? t.fg("error", "✗") : t.fg("success", "✓");
  const title = `${active ? "Running " : ""}Subagents (1 task):`;

  if (!expanded) {
    const header = truncateToWidth(`${glyph} ${t.fg("toolTitle", title)}`, width);
    const prefix = `${glyph} ${t.fg("accent", `${subagentName(tool)}:`)} `;
    const remaining = Math.max(0, width - visibleWidth(prefix));
    const task = truncateToWidth(subagentTitle(tool), remaining, "...");
    return [header, truncateToWidth(prefix, width, "") + t.fg("dim", task)];
  }

  const lines = [truncateToWidth(`${glyph} ${t.fg("toolTitle", subagentName(tool))}`, width)];
  const prompt = text(tool.input.prompt);
  if (prompt) {
    lines.push("", t.fg("muted", "─── Task ───"), ...preview(prompt));
  }
  const output = subagentOutput(tool);
  if (output.trim()) {
    lines.push("", t.fg("muted", "─── Output ───"), ...preview(output));
  }
  return lines;
}

/** Keep only the hunk body: drop `Index:`, `===`, `---`/`+++`, `@@` and markers. */
function diffBody(diff: string): string[] {
  const out: string[] = [];
  for (const line of diff.replace(/\r\n/g, "\n").split("\n")) {
    const trimmed = line.trim();
    if (trimmed === "") {
      out.push(line);
      continue;
    }
    if (/^Index:/i.test(trimmed)) continue;
    if (/^=+$/.test(trimmed)) continue;
    if (line.startsWith("--- ") || line.startsWith("+++ ")) continue;
    if (line.startsWith("@@")) continue;
    if (line.startsWith("\\ No newline")) continue;
    out.push(line);
  }
  return out;
}

/** Colored diff preview: added lines green, removed red, context dim. */
function diffPreview(value: string, maxLines = EXPANDED_PREVIEW_LINES): string[] {
  const t = theme();
  const source = diffBody(value);
  while (source.length > 0 && source[source.length - 1]!.trim() === "") source.pop();
  if (source.length === 0) return [];
  // Show a small window around the first change rather than leading context.
  let start = 0;
  if (source.length > maxLines) {
    const firstChange = source.findIndex((line) => line.startsWith("+") || line.startsWith("-"));
    start = Math.max(0, Math.min(firstChange >= 0 ? firstChange - 2 : 0, source.length - maxLines));
  }
  const shown = source.slice(start, start + maxLines);
  const out = shown.map((line) => {
    if (line.startsWith("+")) return t.fg("toolDiffAdded", line);
    if (line.startsWith("-")) return t.fg("toolDiffRemoved", line);
    return t.fg("toolDiffContext", line);
  });
  const hidden = source.length - (start + shown.length);
  if (hidden > 0) out.push(t.fg("muted", `... ${hidden} more lines`));
  return out;
}

interface EditInfo {
  added: number;
  removed: number;
  diff?: string;
}

/**
 * Edits report their change through tool metadata (`diff`/`patch`/`filediff`);
 * older/other shapes may put it on the input. Read every shape defensively.
 */
function editInfo(tool: ToolView): EditInfo {
  const metadata = tool.metadata ?? {};
  const input = tool.input;
  const diffText =
    typeof metadata.diff === "string" ? metadata.diff : typeof input.diff === "string" ? input.diff : undefined;
  const patchText =
    typeof metadata.patch === "string" ? metadata.patch : typeof input.patch === "string" ? input.patch : undefined;
  const stats = diffStats(patchText) ?? diffStats(diffText) ?? null;
  const filediff = (metadata.filediff ?? metadata.fileDiff) as { additions?: number; deletions?: number } | undefined;
  const added = stats?.added ?? (typeof metadata.additions === "number" ? metadata.additions : filediff?.additions) ?? 0;
  const removed =
    stats?.removed ?? (typeof metadata.deletions === "number" ? metadata.deletions : filediff?.deletions) ?? 0;
  return { added, removed, diff: diffText ?? patchText };
}

function done(message: string): string {
  return `${theme().fg("success", "✓")} ${styleVerb(message)}`;
}

function running(message: string, now: number): string {
  return `${theme().fg("accent", currentFrame(now))} ${styleVerb(message)}`;
}

/** Input is still streaming: `~ Writing command` / `~ Preparing edit`. */
function preparing(message: string): string {
  return `${theme().fg("accent", "~")} ${styleVerb(message)}`;
}

function errorRow(row: ToolRow, detail: string | undefined, expanded: boolean): string[] {
  // The ✗ carries the result; keep the tool's own label rather than raw error text.
  const head = `${theme().fg("error", "✗")} ${styleVerb(row.done)}`;
  const value = detail ?? "";
  return expanded && value.trim() ? [head, ...preview(value)] : [head];
}

interface ToolRow {
  /** Shown while the tool input is still streaming (status "pending"). */
  preparing: string;
  running: string;
  done: string;
  previewText?: string;
  /** Fully styled done row, overriding `done` (used for colored edit stats). */
  doneStyled?: string;
  /** Raw diff shown colored when the row is expanded. */
  diff?: string;
}

/** Middle-shorten a path so it fits the space left by `prefix`/`suffix`. */
function fitPath(path: string, width: number, prefix: string, suffix = ""): string {
  const budget = Math.max(8, width - visibleWidth(prefix) - visibleWidth(suffix) - 1);
  return visibleWidth(path) <= budget ? path : shortenMiddle(path, budget);
}

function rowFor(tool: ToolView, cwd: string, width = 120): ToolRow {
  const input = tool.input;
  const output = tool.output ?? "";
  const path = displayPath(text(input.filePath ?? input.file_path ?? input.path) || undefined, cwd);

  // MCP tools get the pi-agent style "Server MCP: Tool" title.
  const mcp = mcpTitle(tool.tool);
  if (mcp) {
    return {
      preparing: mcp,
      running: mcp,
      done: mcp,
      previewText: output,
    };
  }

  switch (tool.tool) {
    case "read": {
      const lines = lineCount(output);
      const prefix = `Read ${lines} line${lines === 1 ? "" : "s"} in `;
      return {
        preparing: "Preparing read",
        running: `Reading ${fitPath(path, width, "Reading ")}`,
        done: `${prefix}${fitPath(path, width, prefix)}`,
        previewText: output,
      };
    }
    case "bash":
    case "shell": {
      const command = normalizeCommand(text(input.command));
      return { preparing: "Writing command", running: `Running \`${command}\``, done: `Ran \`${command}\``, previewText: output };
    }
    case "edit":
    case "multiedit": {
      const info = editInfo(tool);
      const suffix = info.added === 0 && info.removed === 0 ? "" : ` +${info.added} -${info.removed}`;
      const shown = fitPath(path, width, "Edited ", suffix);
      const statParts: string[] = [];
      if (info.added > 0) statParts.push(theme().fg("success", `+${info.added}`));
      if (info.removed > 0) statParts.push(theme().fg("error", `-${info.removed}`));
      return {
        preparing: "Preparing edit",
        running: `Editing ${fitPath(path, width, "Editing ")}`,
        done: `Edited ${shown}${suffix}`,
        doneStyled: `${theme().fg("success", "✓")} ${styleVerb(`Edited ${shown}`)}${statParts.length ? ` ${statParts.join(" ")}` : ""}`,
        previewText: text(input.diff) || output,
        ...(info.diff ? { diff: info.diff } : {}),
      };
    }
    case "write": {
      const lines = lineCount(text(input.content));
      const prefix = `Wrote ${lines} line${lines === 1 ? "" : "s"} to `;
      return {
        preparing: "Preparing write",
        running: `Writing ${fitPath(path, width, "Writing ")}`,
        done: `${prefix}${fitPath(path, width, prefix)}`,
        previewText: output,
      };
    }
    case "grep": {
      const pattern = text(input.pattern);
      const matches = output.trim().length === 0 ? 0 : lineCount(output);
      const target = displayPath(text(input.path) || ".", cwd, ".");
      const prefix = `Found ${matches} match${matches === 1 ? "" : "es"} for /${pattern}/ in `;
      return {
        preparing: "Preparing search",
        running: `Searching /${pattern}/ in ${fitPath(target, width, `Searching /${pattern}/ in `)}`,
        done: `${prefix}${fitPath(target, width, prefix)}`,
        previewText: output,
      };
    }
    case "glob":
    case "find": {
      const pattern = text(input.pattern);
      const target = displayPath(text(input.path) || ".", cwd, ".");
      const files = output.trim().length === 0 ? 0 : lineCount(output);
      const prefix = `Found ${files} file${files === 1 ? "" : "s"} matching ${pattern} in `;
      return {
        preparing: "Preparing search",
        running: `Finding ${pattern} in ${fitPath(target, width, `Finding ${pattern} in `)}`,
        done: `${prefix}${fitPath(target, width, prefix)}`,
        previewText: output,
      };
    }
    case "list":
    case "ls": {
      const target = displayPath(text(input.path) || ".", cwd, ".");
      const entries = output.trim().length === 0 ? 0 : lineCount(output);
      const prefix = `Listed ${entries} entr${entries === 1 ? "y" : "ies"} in `;
      return {
        preparing: "Preparing list",
        running: `Listing ${fitPath(target, width, "Listing ")}`,
        done: `${prefix}${fitPath(target, width, prefix)}`,
        previewText: output,
      };
    }
    case "webfetch":
    case "fetch": {
      const url = text(input.url);
      return {
        preparing: "Preparing fetch",
        running: `Fetching ${fitPath(url, width, "Fetching ")}`,
        done: `Fetched ${fitPath(url, width, "Fetched ")}`,
        previewText: output,
      };
    }
    case "question": {
      return {
        preparing: "Preparing question",
        running: "Asking question",
        done: "Asked question",
        previewText: output,
      };
    }
    case "skill": {
      const name = formatMcpDisplayName(text(input.name ?? input.skill ?? input.skill_name) || "skill");
      return {
        preparing: `Loading ${name} skill`,
        running: `Using ${name} skill`,
        done: `Used ${name} skill`,
        previewText: output,
      };
    }
    case "task": {
      return {
        preparing: "Running Subagents (1 task)",
        running: "Running Subagents (1 task)",
        done: "Subagents (1 task)",
      };
    }
    default: {
      const summary = text(input.description) || text(input.command) || text(input.query) || tool.title || "";
      const label = tool.tool;
      return {
        preparing: `Preparing ${label}`,
        running: summary ? `Running ${label}: ${summary}` : `Running ${label}`,
        done: summary ? `Ran ${label}: ${summary}` : `Ran ${label}`,
        previewText: output,
      };
    }
  }
}

/** Live action text for a tool: "Writing command" while pending, "Running …" while running. */
export function toolLiveText(tool: ToolView, cwd: string, width = 100): string {
  const row = rowFor(tool, cwd, width);
  return tool.status === "pending" ? row.preparing : row.running;
}

export function renderTool(tool: ToolView, width: number, expanded: boolean, cwd: string, now = Date.now()): string[] {
  if (tool.tool === "task") return renderSubagentTool(tool, width, expanded, currentFrame(now));
  const row = rowFor(tool, cwd, width);
  if (tool.status === "error") return errorRow(row, tool.error ?? tool.output, expanded);

  if (tool.status === "completed") {
    const lines = [row.doneStyled ?? done(row.done)];
    if (expanded) {
      if (row.diff?.trim()) lines.push(...diffPreview(row.diff));
      else if (row.previewText?.trim()) lines.push(...preview(row.previewText));
    }
    return lines;
  }

  // The arguments are still streaming: show what is being written, not executed.
  if (tool.status === "pending") return [preparing(row.preparing)];
  return [running(row.running, now)];
}
