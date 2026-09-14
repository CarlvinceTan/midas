import { BashExecutionComponent } from "@earendil-works/pi-coding-agent";
import type { Component, TUI } from "@earendil-works/pi-tui";
import type { BashView } from "../../state/transcript.ts";
import { markRenderedLines, stripBold } from "../../lib/ansi.ts";
import { RoundedDialogFrame } from "../rounded-frame.ts";

/**
 * Wraps pi's real BashExecutionComponent so `!` shell output keeps its
 * bashMode-bordered content (`$ command`, muted output, loader while running).
 *
 * pi draws only bare top/bottom `─` rules; midas boxes it in the same rounded
 * rectangle as the rest of its frames (corners, side borders, inset content).
 */
export class BashBox implements Component {
  private impl: BashExecutionComponent;
  private frame: RoundedDialogFrame;
  private lastOutputLength = 0;
  private lastStatus = "running";

  constructor(private part: BashView, ui: TUI, expanded: boolean) {
    this.impl = new BashExecutionComponent(part.command, ui, part.exclude);
    this.impl.setExpanded(expanded);
    // pi already insets its header/output text by one column, which is the single
    // space the prompt card uses inside its border. Giving the frame a pad of 0
    // keeps the shell box aligned with the cards above it instead of double-inseting.
    this.frame = new RoundedDialogFrame(() => 0);
    this.frame.addChild(this.impl);
  }

  invalidate(): void {
    this.impl.invalidate();
  }

  render(width: number): string[] {
    this.sync();
    // The frame converts pi's plain rules in place, so only the rendered lines
    // need the selection bounds that pi's component does not emit itself. pi
    // bolds the `$ command` header; the transcript keeps it at normal weight.
    return markRenderedLines(this.frame.render(width)).map((line) => stripBold(line));
  }

  private sync(): void {
    if (this.part.output.length > this.lastOutputLength) {
      this.impl.appendOutput(this.part.output.slice(this.lastOutputLength));
      this.lastOutputLength = this.part.output.length;
    }
    if (this.part.status !== "running" && this.part.status !== this.lastStatus) {
      this.impl.setComplete(this.part.exitCode, this.part.status === "cancelled");
      this.lastStatus = this.part.status;
    }
  }
}
