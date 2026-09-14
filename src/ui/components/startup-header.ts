import { truncateToWidth, visibleWidth, type Component } from "@earendil-works/pi-tui";
import { theme } from "../../theme/theme.ts";
import { markContent, markDecoration } from "../../lib/ansi.ts";
import { alignSides } from "./footer.ts";

export interface SessionHeaderInfo {
  title?: string;
  status?: string;
  /** Right-aligned current path shown on the title row. */
  path?: string;
  /** Git branch for the primary worktree, shown in parentheses after the path. */
  branch?: string;
  /** Right-aligned "N skills • M mcps" summary shown on the status row. */
  resources?: string;
  /** Hide the whole pinned header (the terminal-title setting is off). */
  hidden?: boolean;
}

/**
 * Title/status bar pinned above the transcript (not part of the scrolling
 * output), so the session title and live status stay visible.
 */
export class SessionHeader implements Component {
  constructor(
    private getInfo: () => SessionHeaderInfo,
    private getPad: () => number = () => 1,
  ) {}

  invalidate(): void {}

  render(width: number): string[] {
    const t = theme();
    const data = this.getInfo();
    if (data.hidden) return [];
    const pad = this.getPad();
    const inset = " ".repeat(pad);
    const inner = Math.max(1, width - pad * 2);
    const ellipsis = t.fg("dim", "…");
    const title = t.bold(t.fg("text", data.title || "New Session"));
    const status = t.fg("muted", data.status || "Idle");
    // Path and skills/mcps summary sit on the right of the two header rows.
    const location = data.path && data.branch ? `${data.path} (${data.branch})` : data.path;
    const line1 = location ? alignSides(title, t.fg("dim", location), inner, ellipsis) : title;
    const resources = data.resources ? t.fg("dim", data.resources) : "";
    const line2 = resources ? alignSides(status, resources, inner, ellipsis) : status;
    // A rule under the title/status makes the pinned header read as a header.
    // It is decoration: inset by the row padding and excluded from copy.
    const divider = t.fg("borderMuted", "─".repeat(inner));
    return [
      inset + markContent(line1),
      inset + markContent(line2),
      // Empty content bounds keep the rule unselectable while still decoration.
      inset + markDecoration(markContent("") + divider),
    ];
  }
}

export interface StartupResources {
  contextPaths: string[];
  /** Agents grouped for display; a blank row separates each group. */
  agentGroups: string[][];
  skills: string[];
  mcpNames: string[];
  version: string;
}

/**
 * Compact startup summary, matching the patched pi interactive-mode header:
 *
 *   [Context]
 *   ~/.pi/agent/AGENTS.md
 *
 *   [Agents]
 *   main
 *   orchestrator
 *
 *   advisor
 *   explore
 *   task
 *
 *   compaction
 *   summary
 *   title
 *
 *   [Skills]
 *   None
 *
 *   [MCPs]
 *   None
 */
export class StartupHeader implements Component {
  constructor(
    private getResources: () => StartupResources,
    private getPad: () => number = () => 1,
  ) {}

  invalidate(): void {}

  render(width: number): string[] {
    const t = theme();
    const data = this.getResources();
    const lines: string[] = [];

    // Context / Agents / Skills / MCPs render side by side as left-aligned columns.
    // Agent groups are flattened with a blank row between them, so the columns
    // stay row-aligned while the grouping reads as spacing.
    const agentItems =
      data.agentGroups.length > 0
        ? data.agentGroups.flatMap((group, index) => (index > 0 ? ["", ...group] : group))
        : ["None"];
    const columns: Array<{ title: string; items: string[] }> = [
      { title: "[Context]", items: data.contextPaths.length > 0 ? data.contextPaths : ["None"] },
      { title: "[Agents]", items: agentItems },
      { title: "[Skills]", items: data.skills.length > 0 ? data.skills : ["None"] },
      { title: "[MCPs]", items: data.mcpNames.length > 0 ? data.mcpNames : ["None"] },
    ];
    const padCount = this.getPad();
    const inner = Math.max(0, width - padCount * 2);
    const gap = 2;
    // Split the available width evenly across the columns (recomputed on every
    // render, so it follows terminal resizes). Any remainder goes to the left.
    const totalGap = gap * (columns.length - 1);
    const base = Math.max(1, Math.floor((inner - totalGap) / columns.length));
    const remainder = Math.max(0, inner - totalGap - base * columns.length);
    const widths = columns.map((_, index) => base + (index < remainder ? 1 : 0));
    const cell = (raw: string, index: number, style: (text: string) => string): string => {
      const columnWidth = widths[index]!;
      if (!raw) return " ".repeat(columnWidth);
      // Style the ellipsis too, otherwise truncation drops back to the default color.
      const clipped = truncateToWidth(style(raw), columnWidth, style("…"));
      return clipped + " ".repeat(Math.max(0, columnWidth - visibleWidth(clipped)));
    };
    lines.push(
      columns
        .map((column, index) => cell(column.title, index, (text) => t.fg("startupHeading", text)))
        .join(" ".repeat(gap))
        .trimEnd(),
    );
    const rowCount = Math.max(...columns.map((column) => column.items.length));
    for (let row = 0; row < rowCount; row++) {
      lines.push(
        columns
          .map((column, index) => cell(column.items[row] ?? "", index, (text) => t.fg("dim", text)))
          .join(" ".repeat(gap))
          .trimEnd(),
      );
    }

    const pad = " ".repeat(padCount);
    return lines.map((line) => (line.length > 0 ? pad + markContent(line) : line));
  }
}
