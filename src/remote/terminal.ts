import type { Terminal } from "@earendil-works/pi-tui";

/**
 * A pi-tui `Terminal` backed by a browser. It never touches process stdin or
 * stdout: everything the TUI writes is forwarded to the client (an xterm.js
 * instance), and the client's keystrokes/resizes are fed back in. That lets the
 * remote run the *real* `MidasApp` instead of a re-implementation, so every
 * screen (`/sessions`, `/model`, dialogs, the editor) behaves identically.
 */
export interface VirtualTerminalOptions {
  /** Forward raw ANSI output to the browser. */
  write: (data: string) => void;
  /** Called once the TUI stops the terminal (session over). */
  onStop?: () => void;
  cols?: number;
  rows?: number;
}

export class VirtualTerminal implements Terminal {
  private inputHandler?: (data: string) => void;
  private resizeHandler?: () => void;
  private _cols: number;
  private _rows: number;
  private started = false;

  constructor(private readonly options: VirtualTerminalOptions) {
    this._cols = clamp(options.cols, 80, 20, 500);
    this._rows = clamp(options.rows, 24, 5, 300);
  }

  get kittyProtocolActive(): boolean {
    // The browser client translates modified keys to CSI-u itself, so the TUI
    // never needs to negotiate the Kitty protocol over the wire.
    return false;
  }

  get columns(): number {
    return this._cols;
  }

  get rows(): number {
    return this._rows;
  }

  start(onInput: (data: string) => void, onResize: () => void): void {
    this.inputHandler = onInput;
    this.resizeHandler = onResize;
    this.started = true;
    // Bracketed paste, matching ProcessTerminal, so the browser wraps pastes and
    // the editor can keep them intact.
    this.write("\x1b[?2004h");
  }

  stop(): void {
    if (!this.started) return;
    this.started = false;
    this.write("\x1b[?2004l");
    this.options.onStop?.();
  }

  write(data: string): void {
    this.options.write(data);
  }

  /** Feed raw client input (keystrokes, pastes, mouse sequences) to the TUI. */
  feed(data: string): void {
    this.inputHandler?.(data);
  }

  /** Apply a client resize and let the TUI repaint at the new dimensions. */
  resize(cols: number, rows: number): void {
    const nextCols = clamp(cols, this._cols, 20, 500);
    const nextRows = clamp(rows, this._rows, 5, 300);
    if (nextCols === this._cols && nextRows === this._rows) return;
    this._cols = nextCols;
    this._rows = nextRows;
    this.resizeHandler?.();
  }

  async drainInput(): Promise<void> {
    // Nothing to drain: the browser owns the input stream.
  }

  moveBy(lines: number): void {
    if (lines === 0) return;
    this.write(lines > 0 ? `\x1b[${lines}B` : `\x1b[${-lines}A`);
  }

  hideCursor(): void {
    this.write("\x1b[?25l");
  }

  showCursor(): void {
    this.write("\x1b[?25h");
  }

  clearLine(): void {
    this.write("\x1b[2K");
  }

  clearFromCursor(): void {
    this.write("\x1b[0J");
  }

  clearScreen(): void {
    this.write("\x1b[2J\x1b[H");
  }

  setTitle(title: string): void {
    // Strip control bytes so a session title can't inject sequences.
    this.write(`\x1b]0;${title.replace(/[\x00-\x1f\x7f]/g, "")}\x07`);
  }

  setProgress(_active: boolean): void {
    // The browser has no native taskbar progress; ignore.
  }
}

function clamp(value: unknown, fallback: number, min: number, max: number): number {
  const number = typeof value === "number" && Number.isFinite(value) ? Math.floor(value) : fallback;
  return Math.min(max, Math.max(min, number));
}
