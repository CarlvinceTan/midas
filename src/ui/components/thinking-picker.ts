import {
  fuzzyFilter,
  getKeybindings,
  matchesKey,
  truncateToWidth,
  visibleWidth,
  type Component,
} from "@earendil-works/pi-tui";
import { theme } from "../../theme/theme.ts";

const LEVEL_DESCRIPTIONS: Record<string, string> = {
  off: "No reasoning",
  minimal: "Very brief reasoning (~1k tokens)",
  low: "Light reasoning (~2k tokens)",
  medium: "Moderate reasoning (~8k tokens)",
  high: "Deep reasoning (~16k tokens)",
  xhigh: "Extra-high reasoning (~32k tokens)",
  max: "Maximum reasoning",
};

interface ThinkingItem {
  level: string;
  description: string;
}

/**
 * Compact thinking-level picker styled like the model picker: a bordered list
 * with a `>` search row, a `✓` on the active level and no extra hint rows.
 */
export class ThinkingPicker implements Component {
  private filter = "";
  private selected = 0;
  private readonly items: ThinkingItem[];
  private filtered: ThinkingItem[];

  constructor(
    levels: string[],
    private current: string,
    private defaultLevel: string | undefined,
    private onSelect: (level: string) => void,
    private onSetDefault: (level: string) => void,
    private onCancel: () => void,
    private title = "Thinking",
  ) {
    this.items = levels.map((level) => {
      const full = LEVEL_DESCRIPTIONS[level] ?? "";
      // Show only the token-budget hint, e.g. "(~1k tokens)"; levels without one
      // (off/max) keep their short label.
      const hint = full.match(/\([^)]*\)/)?.[0] ?? full;
      return { level, description: hint };
    });
    this.filtered = this.items;
    const index = this.items.findIndex((item) => item.level === current);
    if (index >= 0) this.selected = index;
  }

  invalidate(): void {}

  private refilter(): void {
    this.filtered = this.filter.trim()
      ? fuzzyFilter(this.items, this.filter, (item) => `${item.level} ${item.description}`)
      : this.items;
    this.selected = Math.min(this.selected, Math.max(0, this.filtered.length - 1));
  }

  handleInput(data: string): void {
    const kb = getKeybindings();
    if (matchesKey(data, "escape") || matchesKey(data, "ctrl+c")) return this.onCancel();
    if (kb.matches(data, "app.thinking.save")) {
      const item = this.filtered[this.selected];
      if (item) this.onSetDefault(item.level);
      return;
    }
    if (matchesKey(data, "enter")) {
      const item = this.filtered[this.selected];
      if (item) this.onSelect(item.level);
      return;
    }
    if (matchesKey(data, "up")) {
      this.selected = this.filtered.length === 0 ? 0 : (this.selected + this.filtered.length - 1) % this.filtered.length;
      return;
    }
    if (matchesKey(data, "down")) {
      this.selected = this.filtered.length === 0 ? 0 : (this.selected + 1) % this.filtered.length;
      return;
    }
    if (matchesKey(data, "backspace")) {
      this.filter = this.filter.slice(0, -1);
      this.refilter();
      return;
    }
    if (data.length === 1 && data >= " " && data !== "\x7f") {
      this.filter += data;
      this.refilter();
    }
  }

  render(width: number): string[] {
    const t = theme();
    const total = Math.max(3, width);
    const inner = total - 2;
    const border = (text: string) => t.fg("borderAccent", text);
    const titleLabel = ` ${this.title} `;
    const title = total >= titleLabel.length + 6 ? titleLabel : "";
    const top =
      border("╭─") + t.fg("borderAccent", title) + border("─".repeat(Math.max(0, total - 3 - title.length)) + "╮");
    const rows: string[] = [];

    // ">" mirrors the selection arrow in the same column, like the model picker.
    const filterLine = "> " + t.fg("text", this.filter) + t.fg("accent", "█");
    rows.push(border("│") + " " + pad(truncateToWidth(filterLine, inner - 1, "…"), inner - 1) + border("│"));

    const visibleCount = 10;
    const count = Math.min(visibleCount, Math.max(1, this.filtered.length));
    const start =
      this.filtered.length <= visibleCount
        ? 0
        : Math.max(0, Math.min(this.selected - Math.floor(visibleCount / 2), this.filtered.length - visibleCount));
    for (let i = 0; i < count; i++) {
      const index = Math.max(0, start) + i;
      const item = this.filtered[index];
      if (!item) {
        rows.push(border("│") + " " + " ".repeat(inner - 1) + border("│"));
        continue;
      }
      const isSelected = index === this.selected;
      const isCurrent = item.level === this.current;
      const marker = isSelected ? t.fg("accent", "→ ") : "  ";
      // The active level is always green; the focused row's name is blue only
      // when it is not the active one.
      const name = isCurrent ? t.fg("success", item.level) : isSelected ? t.fg("accent", item.level) : t.fg("text", item.level);
      const suffix = item.level === this.defaultLevel ? `${item.description} · default` : item.description;
      const line = marker + name + t.fg("muted", `  ${suffix}`);
      rows.push(border("│") + " " + pad(truncateToWidth(line, inner - 1, "…"), inner - 1) + border("│"));
    }

    const bottom = border("╰" + "─".repeat(inner) + "╯");
    return [top, ...rows, bottom];
  }
}

function pad(text: string, width: number): string {
  return text + " ".repeat(Math.max(0, width - visibleWidth(text)));
}
