import { Box, Container, Markdown, truncateToWidth, visibleWidth } from "@earendil-works/pi-tui";
import { getMarkdownTheme, theme } from "../../theme/theme.ts";

const OSC133_ZONE_START = "\x1b]133;A\x07";
const OSC133_ZONE_END = "\x1b]133;B\x07";
const OSC133_ZONE_FINAL = "\x1b]133;C\x07";

/**
 * macOS screenshot names contain a narrow no-break space (U+202F) before am/pm,
 * which renders at a width that disagrees with the measured width and shifts the
 * following glyphs. Display it as a normal space (content sent to the agent is
 * unaffected — this only affects the transcript rendering).
 */
const UNICODE_SPACE_REGEX = /[\u00A0\u1680\u2000-\u200A\u202F\u205F\u3000]/g;

/** Atomic image/file chips, e.g. `[Image: screenshot.png]` / `[File: report.pdf]`. */
const ATTACHMENT_MARKER_REGEX = /\[(?:Image|File): [^\]\n]*\]/g;

/** Yellow, matching the editor's `[Image: …]` / `[File: …]` chips. */
const IMAGE_MARKER_COLOR = "\x1b[33m";

/**
 * The machine-facing, labelled file-content sections the submit path appends
 * after the visible prompt. They are for the model, not the transcript, so the
 * card hides them and keeps the chip label as the user-facing summary.
 */
const UNTRUSTED_SECTION_REGEX =
  /----- BEGIN UNTRUSTED FILE CONTENT: [\s\S]*?----- END UNTRUSTED FILE CONTENT: [^\n]*-----/g;

/** Drop file-content sections and any blank gap they leave behind. */
function visiblePromptText(text: string): string {
  return text
    .replace(UNTRUSTED_SECTION_REGEX, "")
    .replace(/\n{3,}/g, "\n\n")
    .replace(/[ \t]+$/gm, "")
    .trim();
}

/**
 * User prompt card: markdown text inside a rounded, coloured border. Ported from
 * pi's user-message component (the published build renders a borderless
 * background box), so midas keeps the bordered prompt styling.
 */
export class UserPromptCard extends Container {
  constructor(
    private text: string,
    private outputPad: number,
    private borderColor: (content: string) => string,
  ) {
    super();
    this.rebuild();
  }

  private rebuild(): void {
    this.clear();
    const contentBox = new Box(this.outputPad, 0);
    const promptText = visiblePromptText(this.text).replace(UNICODE_SPACE_REGEX, " ");
    // Every `[Image: …]` / `[File: …]` marker renders as the yellow chip in the
    // TUI, wherever it came from (typed, pasted, re-edited queue, transcript
    // copy). The real file is attached from the prompt's file parts / labelled
    // content section when it is sent, so the agent still reads the actual file
    // rather than this label. The chip is wrapped as inline code so markdown
    // leaves it intact, then the code style paints it yellow and restores the
    // prompt text colour. Only the card's border carries the mode colour; the
    // body text stays the normal prompt colour.
    const promptTextColor = "userMessageText";
    const markdownTheme = {
      ...getMarkdownTheme(),
      code: (content: string) => `${IMAGE_MARKER_COLOR}${content}${theme().getFgAnsi(promptTextColor)}`,
    };
    contentBox.addChild(
      new Markdown(
        promptText.replace(ATTACHMENT_MARKER_REGEX, (marker) => `\`${marker}\``),
        0,
        0,
        markdownTheme,
        { color: (content: string) => theme().fg(promptTextColor, content) },
        { preserveOrderedListMarkers: true, preserveBackslashEscapes: true },
      ),
    );
    this.addChild(contentBox);
  }

  render(width: number): string[] {
    // Offset the prompt gutter by one border column, never below zero.
    const margin = Math.min(Math.max(0, this.outputPad - 1), Math.max(0, Math.floor((width - 3) / 2)));
    const cardWidth = Math.max(0, width - margin * 2);
    const innerWidth = Math.max(1, cardWidth - 2);
    const contentLines = super.render(innerWidth).map((line) => {
      const fitted = visibleWidth(line) > innerWidth ? truncateToWidth(line, innerWidth, "") : line;
      return fitted + " ".repeat(Math.max(0, innerWidth - visibleWidth(fitted)));
    });
    if (contentLines.length === 0) return contentLines;

    const horizontal = "─".repeat(innerWidth);
    const lines =
      cardWidth >= 3
        ? [
            "\x1b]777;pi-decoration\x07\x1b]777;pi-content-start\x07\x1b]777;pi-content-end\x07" +
              this.borderColor(`╭${horizontal}╮`),
            ...contentLines.map((line) => `${this.borderColor("│")}${line}${this.borderColor("│")}`),
            "\x1b]777;pi-decoration\x07\x1b]777;pi-content-start\x07\x1b]777;pi-content-end\x07" +
              this.borderColor(`╰${horizontal}╯`),
          ]
        : contentLines.map((line) => truncateToWidth(line, cardWidth, ""));

    for (let i = 0; i < lines.length; i++) {
      // Trim trailing padding: writing into the terminal's final column can make
      // it auto-wrap and overwrite the next row (text appears "squashed"). The
      // TUI clears the rest of each row anyway.
      lines[i] = (" ".repeat(margin) + lines[i]! + " ".repeat(margin)).replace(/ +$/, "");
    }
    lines[0] = OSC133_ZONE_START + lines[0];
    lines[lines.length - 1] = OSC133_ZONE_END + OSC133_ZONE_FINAL + lines[lines.length - 1]!;
    return lines;
  }
}
