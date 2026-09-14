import {
  matchesKey,
  truncateToWidth,
  visibleWidth,
  type Component,
  type TuiMouseEvent,
  type TuiMouseEventResult,
} from "@earendil-works/pi-tui";
import type { QuestionOption, QuestionView } from "../../state/transcript.ts";
import { CONTENT_END, CONTENT_START, DECORATION, markContent } from "../../lib/ansi.ts";
import { theme } from "../../theme/theme.ts";

interface Entry extends QuestionOption {
  isOther?: boolean;
}

/** Per-question cursor/answer state, kept so ←/→ can revisit answered questions. */
interface QState {
  selected: number;
  picked: Set<string>;
  custom: string;
  customCursor: number;
  customScroll: number;
  editing: boolean;
}

const OTHER_LABEL = "Other";
const PASTE_START = "\x1b[200~";
const PASTE_END = "\x1b[201~";
/** The free-text answer wraps and scrolls within this many visible rows. */
const OTHER_MAX_LINES = 3;
/** Width of the `[x] ` checkbox prefix, so the answer lines up under the label. */
const OTHER_INDENT = 4;
/** Extra inset for the option rows and their descriptions: one column each side. */
const OPTION_INDENT = 1;
/** Column the option label / description / typed answer share. */
const LABEL_INDENT = OPTION_INDENT + OTHER_INDENT;

/** One grapheme in the wrapped free-text answer, with its index in the source. */
interface InputCell {
  index: number;
  text: string;
  width: number;
}

/** One wrapped visual line of the answer. `start`/`end` index into the source. */
interface InputRow {
  cells: InputCell[];
  start: number;
  end: number;
}

interface InputHit {
  /** Screen column of the first character of the answer. */
  textX: number;
  /** Screen row of the first visible answer line. */
  firstY: number;
  /** Number of visible answer lines. */
  count: number;
  rows: InputRow[];
  scroll: number;
  /** Wrap width used to lay the rows out. */
  width: number;
}

const graphemes = (text: string): Array<{ segment: string; index: number }> => {
  const result: Array<{ segment: string; index: number }> = [];
  const segmenter = new Intl.Segmenter(undefined, { granularity: "grapheme" });
  for (const part of segmenter.segment(text)) result.push({ segment: part.segment, index: part.index });
  return result;
};

/** A run of graphemes that wraps as a unit: a word, a whitespace gap, or a newline. */
interface InputToken {
  kind: "word" | "space" | "break";
  cells: InputCell[];
  width: number;
  start: number;
}

/** Split the answer into word/whitespace/newline runs, preserving source indices. */
function tokenizeInput(text: string): InputToken[] {
  const tokens: InputToken[] = [];
  for (const { segment, index } of graphemes(text)) {
    const kind: InputToken["kind"] = segment === "\n" ? "break" : /\s/.test(segment) ? "space" : "word";
    const width = kind === "break" ? 0 : Math.max(0, visibleWidth(segment));
    const last = tokens[tokens.length - 1];
    if (last && last.kind === kind && kind !== "break") {
      last.cells.push({ index, text: segment, width });
      last.width += width;
    } else {
      tokens.push({ kind, cells: kind === "break" ? [] : [{ index, text: segment, width }], width, start: index });
    }
  }
  return tokens;
}

/**
 * Wrap the answer on word boundaries, honouring explicit newlines.
 *
 * A word is kept whole whenever it fits on a line of its own, and the
 * whitespace at a wrap point stays on the line it broke from (or is dropped),
 * so every continuation line starts with a full word rather than a dangling
 * space. Only a word longer than the field itself is ever split.
 */
function layoutInput(text: string, width: number): InputRow[] {
  const limit = Math.max(1, width);
  const rows: InputRow[] = [];
  let cells: InputCell[] = [];
  let col = 0;
  let rowStart = 0;
  /** True at the start of the answer and just after an explicit newline. */
  let hardStart = true;
  /** Whitespace waiting to be attached to the row before the next word. */
  let pending: InputToken | null = null;

  const flush = (end: number, next: number): void => {
    rows.push({ cells, start: rowStart, end });
    cells = [];
    col = 0;
    rowStart = next;
    hardStart = false;
    pending = null;
  };
  const append = (token: InputToken): void => {
    for (const cell of token.cells) cells.push(cell);
    col += token.width;
  };

  for (const token of tokenizeInput(text)) {
    if (token.kind === "break") {
      if (pending && cells.length > 0 && col + pending.width <= limit) append(pending);
      pending = null;
      flush(token.start, token.start + 1);
      hardStart = true;
      continue;
    }
    if (token.kind === "space") {
      if (pending && cells.length > 0 && col + pending.width <= limit) append(pending);
      // Indentation at a real line start is kept; whitespace after a wrap is not.
      if (cells.length === 0 && hardStart) append(token);
      else pending = token;
      continue;
    }
    // A word: decide where its leading whitespace (if any) lands.
    if (pending) {
      if (cells.length === 0 || col + pending.width > limit) {
        // The space cannot sit on this row; drop it so the word starts flush.
        pending = null;
      } else if (col + pending.width + token.width <= limit) {
        append(pending);
        pending = null;
      } else {
        // The word moves down; the space stays at the end of this row.
        append(pending);
        pending = null;
        flush(token.start, token.start);
      }
    }
    if (col > 0 && col + token.width > limit) flush(token.start, token.start);
    if (token.width <= limit) {
      append(token);
      continue;
    }
    // A single word wider than the field: break it at the row edge.
    for (const cell of token.cells) {
      if (col > 0 && col + cell.width > limit) flush(cell.index, cell.index);
      cells.push(cell);
      col += cell.width;
    }
  }
  if (pending && cells.length > 0 && col + pending.width <= limit) append(pending);
  rows.push({ cells, start: rowStart, end: text.length });
  return rows;
}

function rowWidth(row: InputRow): number {
  return row.cells.reduce((sum, cell) => sum + cell.width, 0);
}

/** Row/column of a source cursor index within the wrapped layout. */
function locateCursor(rows: InputRow[], cursor: number): { row: number; col: number } {
  for (let i = 0; i < rows.length; i++) {
    const row = rows[i]!;
    if (cursor < row.start || cursor > row.end) continue;
    let col = 0;
    for (const cell of row.cells) {
      if (cell.index >= cursor) break;
      col += cell.width;
    }
    return { row: i, col };
  }
  const last = Math.max(0, rows.length - 1);
  return { row: last, col: rowWidth(rows[last]!) };
}

/** Source index nearest to a visual column within one row. */
function indexAtCol(row: InputRow, col: number): number {
  let acc = 0;
  for (const cell of row.cells) {
    if (acc + cell.width > col) return cell.index;
    acc += cell.width;
  }
  return row.end;
}

/**
 * Modal prompt for opencode's `question` tool. Prompts are answered in order.
 * Options are selectable (Space toggles when multiple, Enter confirms), and an
 * inline "Other" entry takes a free-text answer that can be combined with the
 * other selections before confirming. The free-text answer is a small multi-line
 * editor: it wraps, scrolls within {@link OTHER_MAX_LINES} visible rows, and the
 * cursor can be moved with the arrow keys or by clicking.
 */
export class QuestionDialog implements Component {
  private index = 0;
  private selected = 0;
  private picked = new Set<string>();
  private custom = "";
  private customCursor = 0;
  private customScroll = 0;
  private editing = false;
  private pasting = false;
  private pasteBuffer = "";
  private readonly answers: string[][];
  /** Per-question UI state, indexed by question. */
  private readonly states: Array<QState | undefined> = [];
  /** Rendered geometry for routing clicks on option rows. */
  private entryHit: Array<{ y: number; index: number }> = [];
  /** Rendered geometry for routing clicks into the free-text answer. */
  private inputHit: InputHit | undefined;

  constructor(
    private request: QuestionView,
    private onAnswer: (answers: string[][]) => void,
    private onReject: () => void,
  ) {
    this.answers = request.questions.map(() => []);
  }

  invalidate(): void {}

  private get prompt() {
    return this.request.questions[this.index];
  }

  private entries(): Entry[] {
    const options = this.prompt?.options ?? [];
    return [...options, { label: OTHER_LABEL, description: "Type your own answer", isOther: true }];
  }

  /** Snapshot the live fields so an answered question can be revisited later. */
  private snapshot(): QState {
    return {
      selected: this.selected,
      picked: new Set(this.picked),
      custom: this.custom,
      customCursor: this.customCursor,
      customScroll: this.customScroll,
      editing: this.editing,
    };
  }

  private reset(): void {
    this.selected = 0;
    this.picked = new Set();
    this.custom = "";
    this.customCursor = 0;
    this.customScroll = 0;
    this.editing = false;
  }

  private saveState(): void {
    this.states[this.index] = this.snapshot();
  }

  private loadState(index: number): void {
    const state = this.states[index];
    if (!state) return this.reset();
    this.selected = state.selected;
    this.picked = new Set(state.picked);
    this.custom = state.custom;
    this.customCursor = state.customCursor;
    this.customScroll = state.customScroll;
    this.editing = state.editing;
  }

  /**
   * Record this question's current choice, returning whether it is answered.
   * A single-choice question is answered by the highlighted option; a
   * multi-choice one needs at least one box ticked or text typed.
   */
  private commit(): boolean {
    const prompt = this.prompt;
    if (!prompt) return true;
    const entry = this.entries()[this.selected];
    if (prompt.multiple) {
      const answers = [...this.picked];
      const text = this.custom.trim();
      if (text) answers.push(text);
      this.answers[this.index] = answers;
      return answers.length > 0;
    }
    if (entry?.isOther) {
      const text = this.custom.trim();
      this.answers[this.index] = text ? [text] : [];
      return text.length > 0;
    }
    const label = entry?.label ?? "";
    this.answers[this.index] = label ? [label] : [];
    return Boolean(label);
  }

  /**
   * Move to a neighbouring question. Right is blocked until the current
   * question is answered; Left always works so an answer can be revised.
   * Neither end wraps around.
   */
  private moveQuestion(delta: number): void {
    const to = this.index + delta;
    if (to < 0 || to >= this.request.questions.length) return;
    if (delta > 0 && !this.commit()) return;
    if (delta < 0) this.commit();
    this.saveState();
    this.index = to;
    this.loadState(to);
  }

  /** Confirm the current question and advance, submitting after the last one. */
  private confirm(): void {
    if (!this.commit()) return;
    const next = this.index + 1;
    if (next >= this.request.questions.length) return this.onAnswer(this.answers);
    this.saveState();
    this.index = next;
    this.loadState(next);
  }

  private clampCursor(): number {
    return Math.max(0, Math.min(this.custom.length, this.customCursor));
  }

  /** Move the option selection by `delta`, focusing the answer when it lands there. */
  private moveSelection(delta: number, entries: Entry[]): void {
    // Clamp rather than wrap: Up at the first option and Down at the last stay put.
    this.selected = Math.max(0, Math.min(entries.length - 1, this.selected + delta));
    this.editing = false;
    if (entries[this.selected]?.isOther) this.customCursor = this.custom.length;
  }

  private insertCustom(text: string): void {
    const normalized = text.replace(/\r\n?/g, "\n");
    const cursor = this.clampCursor();
    this.custom = this.custom.slice(0, cursor) + normalized + this.custom.slice(cursor);
    this.customCursor = cursor + normalized.length;
    this.editing = true;
  }

  /** Buffer terminal bracketed-paste sequences and insert their text into Other. */
  private handlePaste(data: string, entries: Entry[]): boolean {
    const start = data.indexOf(PASTE_START);
    if (!this.pasting && start < 0) return false;

    if (!this.pasting) {
      this.pasting = true;
      this.pasteBuffer = "";
      data = data.slice(start + PASTE_START.length);
    }
    this.pasteBuffer += data;

    const end = this.pasteBuffer.indexOf(PASTE_END);
    if (end < 0) return true;
    const pasted = this.pasteBuffer.slice(0, end);
    const remaining = this.pasteBuffer.slice(end + PASTE_END.length);
    this.pasting = false;
    this.pasteBuffer = "";

    if (entries[this.selected]?.isOther) {
      const cleaned = pasted
        .replace(/\r\n?/g, "\n")
        .replace(/\t/g, "    ")
        .replace(/[\x00-\x09\x0b-\x1f\x7f]/g, "");
      if (cleaned) this.insertCustom(cleaned);
    }
    if (remaining) this.handleInput(remaining);
    return true;
  }

  /** Arrow/backspace/Enter handling for the free-text answer. */
  private handleOtherInput(data: string, entries: Entry[]): void {
    const cursor = this.clampCursor();
    const rows = layoutInput(this.custom, this.lastWrapWidth());
    const pos = locateCursor(rows, cursor);

    if (matchesKey(data, "shift+enter") || matchesKey(data, "ctrl+j")) {
      this.insertCustom("\n");
      return;
    }
    if (matchesKey(data, "enter")) {
      // Workaround for terminals without Shift+Enter: a trailing `\` inserts a newline.
      if (cursor > 0 && this.custom[cursor - 1] === "\\") {
        this.custom = this.custom.slice(0, cursor - 1) + this.custom.slice(cursor);
        this.customCursor = cursor - 1;
        this.insertCustom("\n");
        return;
      }
      this.confirm();
      return;
    }
    if (matchesKey(data, "up") || matchesKey(data, "down")) {
      const up = matchesKey(data, "up");
      if (up && pos.row === 0) return this.moveSelection(-1, entries);
      if (!up && pos.row === rows.length - 1) return this.moveSelection(1, entries);
      const target = rows[pos.row + (up ? -1 : 1)];
      if (target) this.customCursor = indexAtCol(target, Math.min(pos.col, rowWidth(target)));
      return;
    }
    if (matchesKey(data, "left")) {
      this.customCursor = cursor - 1;
      return;
    }
    if (matchesKey(data, "right")) {
      this.customCursor = cursor + 1;
      return;
    }
    if (matchesKey(data, "home")) {
      this.customCursor = rows[pos.row]?.start ?? 0;
      return;
    }
    if (matchesKey(data, "end")) {
      this.customCursor = rows[pos.row]?.end ?? this.custom.length;
      return;
    }
    if (matchesKey(data, "backspace")) {
      if (cursor > 0) {
        this.custom = this.custom.slice(0, cursor - 1) + this.custom.slice(cursor);
        this.customCursor = cursor - 1;
        this.editing = true;
      }
      return;
    }
    if (matchesKey(data, "delete")) {
      if (cursor < this.custom.length) {
        this.custom = this.custom.slice(0, cursor) + this.custom.slice(cursor + 1);
        this.editing = true;
      }
      return;
    }
    // Printable input and pastes. Escape sequences and stray control keys are
    // stripped so they cannot corrupt the answer; pasted newlines are kept.
    if (data.length > 0 && !data.includes("\x1b")) {
      const cleaned = data.replace(/[\x00-\x09\x0b-\x0c\x0e-\x1f\x7f]/g, "");
      if (cleaned) this.insertCustom(cleaned);
    }
  }

  handleInput(data: string): void {
    const prompt = this.prompt;
    if (!prompt) return this.onAnswer(this.answers);
    const entries = this.entries();
    if (this.handlePaste(data, entries)) return;
    if (matchesKey(data, "escape") || matchesKey(data, "ctrl+c")) return this.onReject();
    const entry = entries[this.selected];

    // ←/→ change question, except while the free-text answer has focus, where
    // they move the caret so that answer can still be edited.
    if (matchesKey(data, "left") || matchesKey(data, "right")) {
      if (entry?.isOther && this.editing) return this.handleOtherInput(data, entries);
      return this.moveQuestion(matchesKey(data, "right") ? 1 : -1);
    }

    if (matchesKey(data, "up")) {
      if (entry?.isOther) {
        // Inside the answer, Up moves between lines until it reaches the top.
        const rows = layoutInput(this.custom, this.lastWrapWidth());
        if (locateCursor(rows, this.clampCursor()).row > 0) return this.handleOtherInput(data, entries);
      }
      return this.moveSelection(-1, entries);
    }
    if (matchesKey(data, "down")) {
      if (entry?.isOther) {
        const rows = layoutInput(this.custom, this.lastWrapWidth());
        if (locateCursor(rows, this.clampCursor()).row < rows.length - 1) return this.handleOtherInput(data, entries);
      }
      return this.moveSelection(1, entries);
    }

    if (entry?.isOther) {
      return this.handleOtherInput(data, entries);
    }

    if (matchesKey(data, "space") && prompt.multiple) {
      const label = entry?.label;
      if (label) {
        if (this.picked.has(label)) this.picked.delete(label);
        else this.picked.add(label);
      }
      return;
    }
    if (matchesKey(data, "enter")) this.confirm();
  }

  handleMouse(event: TuiMouseEvent): TuiMouseEventResult | undefined {
    if (event.type !== "click" || event.button !== "left") return undefined;
    const entryHit = this.entryHit.find((hit) => hit.y === event.y);
    if (entryHit) {
      this.selected = entryHit.index;
      this.editing = false;
      if (this.entries()[this.selected]?.isOther) this.customCursor = this.custom.length;
      return { handled: true, render: true, focus: true };
    }
    const input = this.inputHit;
    if (input && event.y >= input.firstY && event.y < input.firstY + input.count) {
      const row = input.rows[input.scroll + (event.y - input.firstY)];
      if (!row) return undefined;
      const col = Math.max(0, Math.min(input.width, event.x - input.textX));
      this.customCursor = indexAtCol(row, col);
      // Clicking the answer also focuses the "Other" entry so typing continues there.
      this.selected = Math.max(0, this.entries().length - 1);
      this.editing = true;
      return { handled: true, render: true, focus: true };
    }
    return undefined;
  }

  /** Wrap width from the last render; used for input handling before a repaint. */
  private lastWrapWidth(): number {
    return Math.max(1, this.inputHit?.width ?? 40);
  }

  /** Render one visible answer line, drawing the block cursor when it belongs here. */
  private renderInputLine(row: InputRow, showCursor: boolean, cursor: number): string {
    const t = theme();
    let line = "";
    let cursorDrawn = false;
    for (const cell of row.cells) {
      if (showCursor && !cursorDrawn && cell.index === cursor) {
        line += t.fg("accent", `\x1b[7m${cell.text}\x1b[0m`);
        cursorDrawn = true;
      } else {
        line += t.fg("muted", cell.text);
      }
    }
    if (showCursor && !cursorDrawn) line += t.fg("accent", "\x1b[7m \x1b[0m");
    return line;
  }

  render(width: number): string[] {
    const t = theme();
    const total = Math.max(3, width);
    const inner = total - 2;
    // Match the other `/` panels: a one-column gutter that collapses on a narrow
    // terminal so the border still fits.
    const inset = total >= 9 ? 1 : 0;
    const contentWidth = Math.max(1, inner - inset * 2);
    const gutter = " ".repeat(inset);
    const border = (text: string) => t.fg("borderAccent", text);
    // Content bounds keep drag-selection to the text only, excluding the gutter,
    // fill and borders (same markers `PanelOverlay` uses).
    const edge = (text: string): string => DECORATION + CONTENT_START + CONTENT_END + text;
    const row = (line: string): string => {
      const fitted = truncateToWidth(line, contentWidth, "…");
      const fill = " ".repeat(Math.max(0, contentWidth - visibleWidth(fitted)));
      return border("│") + gutter + markContent(fitted) + fill + gutter + border("│");
    };
    // The counter sits in the top rule — `╭─ Question 1/2 ───╮` — and the body
    // shows the question itself: a blank line, the question, a blank line, then
    // the options. The prompt's `header` is intentionally not rendered.
    const prompt = this.prompt;
    const bottom = edge(border("╰" + "─".repeat(inner) + "╯"));

    this.entryHit = [];
    this.inputHit = undefined;
    if (!prompt) return [edge(border("╭" + "─".repeat(inner) + "╮")), row(t.fg("text", "Done")), bottom];

    const count = this.request.questions.length;
    const title = total >= 12 ? ` Question ${this.index + 1}/${count} ` : "";
    const top = edge(
      border("╭─") + t.fg("borderAccent", title) + border("─".repeat(Math.max(0, total - 3 - title.length)) + "╮"),
    );

    const body: string[] = [];
    body.push(row(""));
    // The counter sits in the top rule as `╭─ Question n/m `, so its text starts
    // one column past the `╭─`. Line the question up under it.
    const counterIndent = Math.max(0, 2 - inset);
    for (const line of wrap(prompt.question, contentWidth - counterIndent * 2)) {
      body.push(row(" ".repeat(counterIndent) + t.fg("text", line)));
    }
    body.push(row(""));

    const entries = this.entries();
    entries.forEach((entry, idx) => {
      const active = idx === this.selected;
      this.entryHit.push({ y: body.length + 1, index: idx });
      const chosen = entry.isOther
        ? active || this.custom.trim().length > 0
        : prompt.multiple
          ? this.picked.has(entry.label)
          : active;
      const check = chosen ? "[x] " : "[ ] ";
      const label = active ? t.fg("accent", entry.label) : t.fg("text", entry.label);
      body.push(row(" ".repeat(OPTION_INDENT) + `${check}${label}`));
      if (!entry.isOther && entry.description) {
        // Match the label indent on the right so the description never hugs the
        // border: the same padding on both sides.
        for (const line of wrap(entry.description, contentWidth - LABEL_INDENT * 2)) {
          body.push(row(" ".repeat(LABEL_INDENT) + t.fg("muted", line)));
        }
      }
      // The typed answer sits under "Other", indented to line up with the option
      // label text, as a wrapped block that scrolls to keep the cursor visible.
      if (entry.isOther && (active || this.custom)) {
        // Reserve a column so the block cursor never touches the right padding.
        const wrapWidth = Math.max(1, contentWidth - LABEL_INDENT - 1);
        const rows = layoutInput(this.custom, wrapWidth);
        const cursor = Math.max(0, Math.min(this.custom.length, this.customCursor));
        const cursorPos = locateCursor(rows, cursor);
        if (active) {
          if (cursorPos.row < this.customScroll) this.customScroll = cursorPos.row;
          else if (cursorPos.row >= this.customScroll + OTHER_MAX_LINES) {
            this.customScroll = cursorPos.row - OTHER_MAX_LINES + 1;
          }
        }
        this.customScroll = Math.max(0, Math.min(this.customScroll, Math.max(0, rows.length - OTHER_MAX_LINES)));
        const visible = rows.slice(this.customScroll, this.customScroll + OTHER_MAX_LINES);
        const firstY = body.length + 1;
        visible.forEach((line, offset) => {
          const isCursorRow = active && this.customScroll + offset === cursorPos.row;
          body.push(row(" ".repeat(LABEL_INDENT) + this.renderInputLine(line, isCursorRow, cursor)));
        });
        this.inputHit = {
          textX: 1 + inset + LABEL_INDENT,
          firstY,
          count: visible.length,
          rows,
          scroll: this.customScroll,
          width: wrapWidth,
        };
      }
    });
    // A blank row separates the last option (or its answer) from the bottom rule.
    body.push(row(""));

    return [top, ...body, bottom];
  }
}

function wrap(text: string, width: number): string[] {
  const words = text.split(/\s+/);
  const lines: string[] = [];
  let current = "";
  for (const word of words) {
    if (current.length === 0) current = word;
    else if (current.length + 1 + word.length <= width) current += " " + word;
    else {
      lines.push(current);
      current = word;
    }
  }
  if (current) lines.push(current);
  return lines.length > 0 ? lines : [""];
}
