import {
  Container,
  stripTerminalSequences,
  truncateToWidth,
  visibleWidth,
  type Component,
  type TuiMouseEvent,
  type TuiMouseEventResult,
} from "@earendil-works/pi-tui";
import { theme } from "../theme/theme.ts";
import { CONTENT_END, CONTENT_START, DECORATION, markContent } from "../lib/ansi.ts";
import { capitalize } from "../lib/text.ts";

/**
 * Rounded-dialog framing, ported from pi's interactive-mode (RoundedDialogFrame
 * / FramedEditorDock). Turns a component's plain `─` rules into rounded corners
 * and insets content by the configured output padding.
 */

/** A line that is itself a rounded frame edge, e.g. `╭─ Title ───╮`. */
const isRoundedEdge = (line: string): boolean => {
  const text = stripTerminalSequences(line).trim();
  return /^╭─.*╮$/u.test(text) || /^╰─.*╯$/u.test(text);
};

/** A plain `─` rule, or a scroll rule like `──── ↑ 17 more ────`. */
function isDialogRulePlain(plain: string): boolean {
  const text = plain.trim();
  if (text.length === 0) return false;
  if (/^─+$/u.test(text)) return true;
  return /^─+\s*[↑↓]\s*\d+\s*more\s*─*$/u.test(text);
}

function isDialogRule(line: string): boolean {
  return isDialogRulePlain(stripTerminalSequences(line));
}

/** The `↑ N more` label embedded in a scroll rule, if any. */
function dialogRuleLabel(plain: string): string | undefined {
  return plain.trim().match(/[↑↓]\s*\d+\s*more/)?.[0];
}

export function roundedFrameRow(middle: string, edge: "top" | "bottom" | "body", color: (text: string) => string): string {
  if (edge === "body") return color("│") + middle + "\x1b[0m" + color("│");
  const corners = edge === "top" ? ["╭", "╮"] : ["╰", "╯"];
  return (
    DECORATION +
    CONTENT_START +
    CONTENT_END +
    color(corners[0]!) +
    middle +
    color(corners[1]!)
  );
}

/** `─ Title ─────` sized to `total` visible columns, title in the border color. */
function titledRule(title: string, total: number, color: (text: string) => string): string {
  const label = ` ${title} `;
  const labelWidth = visibleWidth(label);
  if (total <= labelWidth + 1) return color("─".repeat(Math.max(0, total)));
  return color("─") + color(label) + color("─".repeat(total - labelWidth - 1));
}

/** Like `titledRule` but with the label centred (editor scroll indicators). */
function centeredRule(label: string, total: number, color: (text: string) => string): string {
  const text = ` ${label} `;
  const textWidth = visibleWidth(text);
  if (total <= textWidth) return color("─".repeat(Math.max(0, total)));
  const remaining = total - textWidth;
  const left = Math.floor(remaining / 2);
  return color("─".repeat(left)) + color(text) + color("─".repeat(remaining - left));
}

export class RoundedDialogFrame extends Container {
  private framed = false;
  private pad = 0;
  private innerWidth = 0;
  private innerHeight = 0;

  constructor(
    private getPad: () => number = () => 1,
    private borderColor?: (text: string) => string,
    /** Optional label drawn into the top rule, e.g. `╭─ Multitask ───╮`. */
    private title?: string,
    /** Centered labels overlaid on the top/bottom rules (e.g. history counts). */
    private labels?: { top?: () => string; bottom?: () => string },
  ) {
    super();
  }

  render(width: number): string[] {
    const outer = super.render(width);
    this.framed = false;
    if (width < 3) return outer;
    // Only the child's outer rules mean it already draws a rounded frame. Scanning
    // the whole body would misfire on content that merely contains box-drawing
    // characters (e.g. pasting a diagram into the editor).
    const firstNonEmpty = outer.find((line) => stripTerminalSequences(line).trim().length > 0);
    const lastNonEmpty = [...outer].reverse().find((line) => stripTerminalSequences(line).trim().length > 0);
    if (isRoundedEdge(firstNonEmpty ?? "") || isRoundedEdge(lastNonEmpty ?? "")) return outer;
    if (!outer.some(isDialogRule)) return outer;

    const requested = Math.max(0, Math.floor(this.getPad()));
    const pad = width < 5 ? 0 : Math.min(requested, Math.floor((width - 2) / 2));
    const innerWidth = Math.max(1, width - 2 - pad * 2);
    const inner = super.render(innerWidth);
    const plain = inner.map((line) => stripTerminalSequences(line));
    const top = plain.findIndex(isDialogRulePlain);
    let bottom = -1;
    for (let i = plain.length - 1; i > top; i--) {
      if (isDialogRulePlain(plain[i]!)) {
        bottom = i;
        break;
      }
    }
    if (top < 0 || bottom < 0) return outer;

    const edge = inner[top]!;
    const dashStart = edge.indexOf("─");
    const color =
      this.borderColor ??
      ((text: string): string => edge.slice(0, dashStart) + text + edge.slice(edge.lastIndexOf("─") + 1));
    // A title sits left on the rule (`╭─ Title ───╮`); a scroll rule's
    // `↑ N more` label stays centred like the editor drew it.
    const plainRule = color("─".repeat(width - 2));
    const topScroll = dialogRuleLabel(plain[top]!);
    const bottomScroll = top === bottom ? undefined : dialogRuleLabel(plain[bottom]!);
    const topLabel = this.labels?.top?.() || undefined;
    const bottomLabel = this.labels?.bottom?.() || undefined;
    const topRule = topLabel
      ? centeredRule(topLabel, width - 2, color)
      : topScroll
        ? centeredRule(topScroll, width - 2, color)
        : this.title
          ? titledRule(this.title, width - 2, color)
          : plainRule;
    const bottomRule = bottomLabel
      ? centeredRule(bottomLabel, width - 2, color)
      : bottomScroll
        ? centeredRule(bottomScroll, width - 2, color)
        : plainRule;
    const fit = (line: string): string => {
      const fitted = visibleWidth(line) > width ? truncateToWidth(line, width, "") : line;
      return fitted + " ".repeat(Math.max(0, width - visibleWidth(fitted)));
    };

    const lines: string[] = [];
    for (let i = 0; i < inner.length; i++) {
      if (i > bottom) {
        // Rows below the box are the editor's autocomplete menu. They carry
        // their own padding plus the `→ `/`  ` pointer prefix, so inset them by
        // the frame's gutter to line each command up under the first character
        // typed after the slash.
        lines.push(fit(" ".repeat(pad) + inner[i]!));
        continue;
      }
      if (i < top) {
        lines.push(fit(inner[i]!));
        continue;
      }
      if (i === top) {
        lines.push(roundedFrameRow(topRule, "top", color));
        continue;
      }
      if (i === bottom) {
        lines.push(roundedFrameRow(bottomRule, "bottom", color));
        continue;
      }
      // Mark the real content (and only it) so drag-selection skips the frame
      // padding: an empty body row emits adjacent markers and selects nothing.
      const content = truncateToWidth(inner[i]!, innerWidth, "").trimEnd();
      const tail = " ".repeat(Math.max(0, width - 2 - pad - visibleWidth(content)));
      const middle = " ".repeat(pad) + markContent(content) + tail;
      lines.push(roundedFrameRow(middle, "body", color));
    }
    this.pad = pad;
    this.innerWidth = innerWidth;
    this.innerHeight = inner.length;
    this.framed = true;
    return lines;
  }

  /**
   * Route clicks into the framed child with the border/pad offset removed.
   * The frame replaces the child's own rule rows in place, so rows line up
   * one-to-one; only the horizontal offset changes.
   */
  handleMouse(event: TuiMouseEvent): ReturnType<Container["handleMouse"]> {
    const child = this.children[0];
    if (!child) return undefined;
    if (!this.framed) return super.handleMouse(event);
    const x = event.x - 1 - this.pad;
    if (x < 0 || event.y < 0) return undefined;
    const result = child.handleMouse?.({ ...event, x, width: this.innerWidth, height: this.innerHeight });
    if (!result) return undefined;
    return {
      ...result,
      handled: true,
      target: { component: child, originX: 1 + this.pad, originY: 0, width: this.innerWidth, height: this.innerHeight },
    };
  }
}

/** Editor dock: every child gets the shared rounded frame. */
export class FramedEditorDock extends Container {
  constructor(private getPad: () => number = () => 1) {
    super();
  }

  addChild(component: Component): void {
    if (component instanceof RoundedDialogFrame) {
      super.addChild(component);
      return;
    }
    const frame = new RoundedDialogFrame(this.getPad);
    frame.addChild(component);
    super.addChild(frame);
  }
}

/**
 * Overlay wrapper that rounds a component's own rule rows (e.g. the thinking
 * selector) while forwarding input to it.
 */
export class RoundedOverlay implements Component {
  private frame: RoundedDialogFrame;

  constructor(
    private child: Component,
    borderColor?: (text: string) => string,
    title?: string,
  ) {
    this.frame = new RoundedDialogFrame(() => 1, borderColor, title);
    this.frame.addChild(child);
  }

  invalidate(): void {
    this.child.invalidate?.();
  }

  handleInput(data: string): void {
    this.child.handleInput?.(data);
  }

  render(width: number): string[] {
    return this.frame.render(width);
  }
}

/** Overlay wrapper that draws a titled rounded panel around arbitrary content. */
export class PanelOverlay implements Component {
  constructor(
    private title: string | (() => string),
    private child: Component,
  ) {}

  invalidate(): void {
    this.child.invalidate?.();
  }

  handleInput(data: string): void {
    this.child.handleInput?.(data);
  }

  handleMouse(event: TuiMouseEvent): TuiMouseEventResult | undefined {
    // Content is inset by the border row/column; translate before forwarding.
    const x = event.x - 1;
    const y = event.y - 1;
    if (x < 0 || y < 0) return undefined;
    return this.child.handleMouse?.({ ...event, x, y });
  }

  render(width: number): string[] {
    const t = theme();
    const total = Math.max(3, width);
    const inner = total - 2;
    const border = (text: string) => t.fg("borderAccent", text);
    const raw = typeof this.title === "function" ? this.title() : this.title;
    const title = total >= 12 ? ` ${capitalize(raw)} ` : "";
    // Empty content bounds mark the border rows as decoration, so a drag-select
    // never highlights the frame itself.
    const edge = (text: string): string => DECORATION + CONTENT_START + CONTENT_END + text;
    const top = edge(border("╭─") + t.fg("borderAccent", title) + border("─".repeat(Math.max(0, total - 3 - title.length)) + "╮"));
    // Inset content from the side borders so text never hugs the frame.
    const pad = total >= 9 ? 1 : 0;
    const contentWidth = Math.max(1, inner - pad * 2);
    const gutter = " ".repeat(pad);
    const rows = this.child.render(contentWidth).map((line) => {
      const fitted = visibleWidth(line) > contentWidth ? truncateToWidth(line, contentWidth, "…") : line;
      const fill = " ".repeat(Math.max(0, contentWidth - visibleWidth(fitted)));
      // Only the text is selectable; the gutter, fill and borders are not.
      return border("│") + gutter + markContent(fitted) + fill + gutter + border("│");
    });
    const bottom = edge(border("╰" + "─".repeat(inner) + "╯"));
    return [top, ...rows, bottom];
  }
}
