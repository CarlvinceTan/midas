import { spawn } from "node:child_process";
import { createInterface } from "node:readline";
import { fileURLToPath } from "node:url";

/** One line of the streaming STT helper protocol. */
export interface SttEvent {
  type: "ready" | "listening" | "paused" | "partial" | "final" | "error";
  text?: string;
  message?: string;
}

const STT_EVENT_TYPES = new Set(["ready", "listening", "paused", "partial", "final", "error"]);

/** Parse a JSONL line emitted by the helper; junk lines are ignored. */
export function parseSttLine(line: string): SttEvent | undefined {
  const trimmed = line.trim();
  if (!trimmed) return undefined;
  let parsed: unknown;
  try { parsed = JSON.parse(trimmed); } catch { return undefined; }
  if (!parsed || typeof parsed !== "object") return undefined;
  const { type, text, message } = parsed as { type?: unknown; text?: unknown; message?: unknown };
  if (typeof type !== "string" || !STT_EVENT_TYPES.has(type)) return undefined;
  return {
    type: type as SttEvent["type"],
    text: typeof text === "string" ? text : undefined,
    message: typeof message === "string" ? message : undefined,
  };
}

/** Full editor text: whatever was there before voice + finalized + in-progress. */
export function composeVoiceText(base: string, committed: string, partial: string): string {
  return `${base}${committed}${partial}`;
}

export interface SttProcess {
  stdout: NodeJS.ReadableStream;
  /** Commands are written here (`listen`/`pause`/`stop`); absent for one-shot helpers. */
  stdin?: NodeJS.WritableStream;
  /** OS process id; used to take down the whole helper group on teardown. */
  pid?: number;
  on(event: "exit", cb: (code: number | null) => void): void;
  kill(): void;
}

export type SttSpawn = (command: string, args: string[]) => SttProcess;

export interface VoiceControllerOptions {
  command: string;
  args?: string[];
  spawn?: SttSpawn;
  /** Called with finalized + in-progress text as it arrives. */
  onText: (committed: string, partial: string) => void;
  /** Called once on a helper error; the controller stops itself. */
  onError: (message: string) => void;
  /**
   * Called once when the helper has loaded its model and can listen. Lets the UI
   * show a loading state only on the very first (cold) start.
   */
  onReady?: () => void;
  /** Called when the helper exits unexpectedly. */
  onStop?: () => void;
}

/**
 * Owns the long-lived speech-to-text helper. The process is spawned once and
 * kept warm: `listen` starts capture, `pause` stops it but keeps the model
 * loaded (so re-entering `/voice` is instant), and `stop` tears it down. Text is
 * only surfaced while listening, and each listening session starts from a clean
 * transcript so re-entry never duplicates prior dictation.
 */
export class VoiceController {
  private child?: SttProcess;
  private committed = "";
  private partial = "";
  private teardown = false;
  private processReady = false;
  private listening = false;

  constructor(private options: VoiceControllerOptions) {}

  /** True once the helper has the model loaded and can start listening at once. */
  get ready(): boolean {
    return this.processReady;
  }

  /** Spawn and warm the helper without opening the microphone. */
  preload(): void {
    this.ensureStarted();
  }

  /** Start capturing; if the helper was preloaded this is immediate. */
  listen(): void {
    this.ensureStarted();
    if (this.listening) return;
    this.listening = true;
    this.committed = "";
    this.partial = "";
    this.send("listen");
  }

  /** Stop capturing but keep the model warm for the next `/voice`. */
  pause(): void {
    if (!this.listening) return;
    this.listening = false;
    this.send("pause");
  }

  /** Tear the helper down for good (app exit or a fatal error). */
  stop(): void {
    this.teardown = true;
    this.listening = false;
    const child = this.child;
    this.child = undefined;
    this.processReady = false;
    if (!child) return;
    this.sendTo(child, "stop");
    this.killTree(child);
  }

  /**
   * Kill the helper and everything below it (`uvx`/python/ffmpeg). The helper is
   * spawned detached, so on POSIX it leads its own process group; signalling the
   * group guarantees no orphaned capture process is left behind.
   */
  private killTree(child: SttProcess): void {
    const pid = child.pid;
    if (pid !== undefined && process.platform !== "win32") {
      try {
        process.kill(-pid, "SIGTERM");
        return;
      } catch {
        // Not a group leader (or already gone); fall back to the direct kill.
      }
    }
    try { child.kill(); } catch { /* already exited */ }
  }

  private ensureStarted(): void {
    if (this.child) return;
    this.teardown = false;
    this.processReady = false;
    const spawnImpl: SttSpawn = this.options.spawn
      ?? ((command, args) => spawn(command, args, {
        stdio: ["pipe", "pipe", "ignore"],
        // Lead a new process group so teardown can signal uvx/python/ffmpeg at
        // once, and so a terminal Ctrl+C aimed at Midas does not reach the helper.
        detached: process.platform !== "win32",
      }) as unknown as SttProcess);
    let child: SttProcess;
    try {
      child = spawnImpl(this.options.command, this.options.args ?? []);
    } catch (error) {
      this.options.onError(error instanceof Error ? error.message : String(error));
      return;
    }
    this.child = child;
    const lines = createInterface({ input: child.stdout });
    lines.on("line", (line) => this.handleLine(line));
    child.on("exit", () => this.handleExit());
  }

  private handleLine(line: string): void {
    const event = parseSttLine(line);
    if (!event) return;
    if (event.type === "ready") {
      this.markReady();
    } else if (event.type === "partial") {
      this.markReady();
      if (!this.listening) return;
      const text = event.text ?? "";
      // A silent or noisy gap can make the engine report an empty partial (the
      // streaming model drops its un-finalized tail). Ignore it so words already
      // recognised are not wiped from the input; only real speech replaces them.
      if (!text && this.partial) return;
      this.partial = text;
      this.options.onText(this.committed, this.partial);
    } else if (event.type === "final") {
      this.markReady();
      if (!this.listening) { this.partial = ""; return; }
      this.committed += event.text ?? "";
      this.partial = "";
      this.options.onText(this.committed, "");
    } else if (event.type === "error") {
      this.options.onError(event.message ?? "Speech recognition failed");
      this.stop();
    }
    // "listening"/"paused" are informational acknowledgements.
  }

  private markReady(): void {
    if (this.processReady) return;
    this.processReady = true;
    this.options.onReady?.();
  }

  private handleExit(): void {
    this.child = undefined;
    this.processReady = false;
    this.listening = false;
    if (this.teardown) return;
    if (this.partial) {
      this.committed += this.partial;
      this.partial = "";
    }
    this.options.onStop?.();
  }

  private send(command: "listen" | "pause" | "stop"): void {
    if (this.child) this.sendTo(this.child, command);
  }

  private sendTo(child: SttProcess, command: "listen" | "pause" | "stop"): void {
    const stdin = child.stdin;
    if (!stdin) return;
    try { stdin.write(`${JSON.stringify({ type: command })}\n`); } catch { /* process gone */ }
  }
}

/** Default helper: a bundled Node script wrapping a local STT engine. */
export function defaultVoiceCommand(): { command: string; args: string[] } {
  const script = fileURLToPath(new URL("../../scripts/voice-stt.mjs", import.meta.url));
  return { command: process.execPath, args: [script] };
}
