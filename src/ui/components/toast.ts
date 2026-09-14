import { truncateToWidth, visibleWidth, type Component } from "@earendil-works/pi-tui";

/** Semantic toast levels; every toast is one of these three. */
export type ToastLevel = "success" | "warning" | "error";

/**
 * Fixed styling per level. Success is green/black, warning yellow/black, and
 * error red/bright-white, so the background colour always matches the meaning.
 */
export const TOAST_STYLES: Record<ToastLevel, string> = {
  success: "\x1b[42m\x1b[30m",
  warning: "\x1b[43m\x1b[30m",
  error: "\x1b[41m\x1b[97m",
};

/** Fit the overlay to its message with one column of padding on each side. */
export function toastWidth(text: string, maxWidth: number): number {
  return Math.min(visibleWidth(text) + 2, Math.max(1, maxWidth));
}

/** One-line transient message, rendered as a top-right overlay. */
export class Toast implements Component {
  constructor(
    private text: string,
    private level: ToastLevel,
  ) {}

  invalidate(): void {}

  render(width: number): string[] {
    const inner = truncateToWidth(this.text, Math.max(0, width - 2), "…");
    const fill = " ".repeat(Math.max(0, width - 2 - visibleWidth(inner)));
    return [`${TOAST_STYLES[this.level]} ${inner}${fill} \x1b[0m`];
  }
}
