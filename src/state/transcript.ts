import type { AssistantMessage, Message, Part, Permission, Session, UserMessage } from "@opencode-ai/sdk";

export type ToolStatus = "pending" | "running" | "completed" | "error";

export interface ToolView {
  kind: "tool";
  id: string;
  callID: string;
  tool: string;
  status: ToolStatus;
  input: Record<string, unknown>;
  title?: string;
  output?: string;
  error?: string;
  metadata?: Record<string, unknown>;
  start: number;
  end?: number;
}

export interface TextView {
  kind: "text";
  id: string;
  text: string;
}

export interface ReasoningView {
  kind: "reasoning";
  id: string;
  text: string;
  ended: boolean;
  /** Wall-clock bounds of the thinking episode, for the Thought for Xs row. */
  startedAt?: number;
  endedAt?: number;
}

/** A `!` shell command execution (rendered like pi's BashExecutionComponent). */
export interface BashView {
  kind: "bash";
  id: string;
  command: string;
  output: string;
  status: "running" | "complete" | "error" | "cancelled";
  exitCode?: number;
  exclude: boolean;
}

export type PartView = TextView | ReasoningView | ToolView | BashView;

export interface MessageView {
  id: string;
  role: "user" | "assistant";
  agent?: string;
  providerID?: string;
  modelID?: string;
  cost: number;
  tokens: { input: number; output: number; reasoning: number; cacheRead: number; cacheWrite: number };
  created?: number;
  completed?: number;
  error?: string;
  /** Locally generated status/notice line (dim), not a model message. */
  notice?: boolean;
  /** Stored in the session (e.g. thinking/model changes) but not rendered. */
  hidden?: boolean;
  /** A mid-turn steer from the user: part of the current run, not a new one. */
  steer?: boolean;
  /**
   * Monotonic counter bumped on every mutation of this message or any of its
   * parts. Views sum it to detect changes without diffing all content, which
   * lets them cache rendered lines across frames.
   */
  version?: number;
  parts: PartView[];
  /** Filenames of image/file parts attached to this message (for chip styling). */
  imageFilenames?: string[];
}

export type SessionPhase = "idle" | "busy" | "retry";

/** A pending opencode `question` tool request awaiting the user's answer. */
export interface QuestionOption {
  label: string;
  description?: string;
}
export interface QuestionPrompt {
  header?: string;
  question: string;
  options: QuestionOption[];
  multiple?: boolean;
}
export interface QuestionView {
  id: string;
  sessionID?: string;
  questions: QuestionPrompt[];
}

/**
 * Provider/API failures often surface as a bare URL-connection error
 * ("Unable to connect. Is the computer able to access the url?") that hides the
 * cause. When the failure looks like a network/DNS problem, name it so the user
 * checks their internet rather than the model or the API key.
 */
const NETWORK_ERROR_REGEX =
  /cannot connect to api|unable to connect|fetch failed|enotfound|eai_again|getaddrinfo|network is unreachable|network error|econnrefused|econnreset|etimedout|socket hang up/i;

/** True when an API failure message looks like a network/connection problem. */
export function isNetworkError(message: string): boolean {
  return NETWORK_ERROR_REGEX.test(message);
}

export function formatApiError(message: string): string {
  if (isNetworkError(message)) {
    return "No internet connection: couldn't reach the model API. Check your network and try again.";
  }
  return message;
}

function toMessageView(info: Message): MessageView {
  if (info.role === "assistant") {
    const a = info as AssistantMessage;
    return {
      id: a.id,
      role: "assistant",
      agent: a.mode,
      providerID: a.providerID,
      modelID: a.modelID,
      cost: a.cost ?? 0,
      tokens: {
        input: a.tokens?.input ?? 0,
        output: a.tokens?.output ?? 0,
        reasoning: a.tokens?.reasoning ?? 0,
        cacheRead: a.tokens?.cache?.read ?? 0,
        cacheWrite: a.tokens?.cache?.write ?? 0,
      },
      created: a.time?.created,
      completed: a.time?.completed,
      error: a.error
        ? formatApiError((a.error as { data?: { message?: string } }).data?.message ?? a.error.name)
        : undefined,
      parts: [],
    };
  }
  return {
    id: info.id,
    role: "user",
    // Remember which agent/mode sent this, so its prompt card keeps that mode's
    // colour even after multitask is toggled.
    agent: (info as UserMessage).agent,
    cost: 0,
    tokens: { input: 0, output: 0, reasoning: 0, cacheRead: 0, cacheWrite: 0 },
    created: info.time?.created,
    parts: [],
  };
}

/**
 * Mutable transcript mirroring the opencode session. Events arrive as full
 * snapshots of messages/parts, so upserts are idempotent and order-stable.
 */
export class Transcript {
  messages: MessageView[] = [];
  phase: SessionPhase = "idle";
  /**
   * True while an in-flight turn is blocked on a network failure. Set from
   * opencode's retry/error events so the live status flips to "Reconnecting"
   * the moment the drop is seen, without waiting for the next poll.
   */
  reconnecting = false;
  session: Session | undefined;
  permissions: Permission[] = [];
  /** Pending question requests from the agent's `question` tool. */
  questions: QuestionView[] = [];
  private messageIndex = new Map<string, MessageView>();
  private partIndex = new Map<string, PartView>();
  /** Part id -> owning message id, so a part mutation can bump its message. */
  private owner = new Map<string, string>();
  /** Source of `MessageView.version`; only ever increases. */
  private revision = 0;
  private listeners = new Set<() => void>();

  subscribe(listener: () => void): () => void {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  }

  private emit(): void {
    for (const listener of this.listeners) listener();
  }

  private touch(message: MessageView | undefined): void {
    if (message) message.version = ++this.revision;
  }

  private messageForPart(partId: string): MessageView | undefined {
    const messageID = this.owner.get(partId);
    return messageID ? this.messageIndex.get(messageID) : undefined;
  }

  private findMessage(id: string): MessageView | undefined {
    return this.messageIndex.get(id);
  }

  upsertMessage(info: Message): void {
    let view = this.findMessage(info.id);
    if (!view) {
      view = toMessageView(info);
      this.messageIndex.set(info.id, view);
      this.messages.push(view);
    } else {
      const next = toMessageView(info);
      next.parts = view.parts;
      Object.assign(view, next);
    }
    this.touch(view);
    this.emit();
  }

  removeMessage(id: string): void {
    const view = this.findMessage(id);
    if (!view) return;
    this.messageIndex.delete(id);
    this.messages = this.messages.filter((m) => m.id !== id);
    for (const part of view.parts) {
      this.partIndex.delete(part.id);
      this.owner.delete(part.id);
    }
    this.emit();
  }

  upsertPart(part: Part, delta?: string): void {
    const message = this.findMessage(part.messageID);
    if (!message) return;
    this.owner.set(part.id, part.messageID);
    const existing = this.partIndex.get(part.id);
    // File parts (pasted images) don't render on their own, but the user's text
    // contains an `[Image: name]` chip for them. Record the name so the prompt
    // card can style only real attachments, not typed lookalikes.
    if (part.type === "file") {
      if (part.filename) {
        const names = (message.imageFilenames ??= []);
        if (!names.includes(part.filename)) names.push(part.filename);
      }
      this.touch(message);
      this.emit();
      return;
    }
    // opencode streams prose as deltas; apply them so text grows incrementally
    // instead of jumping between sparse full-part snapshots.
    if (existing && delta && (existing.kind === "text" || existing.kind === "reasoning")) {
      existing.text += delta;
      this.touch(message);
      this.emit();
      return;
    }
    const view = partToView(part, existing);
    if (!existing) {
      message.parts.push(view);
      this.partIndex.set(view.id, view);
    }
    this.touch(message);
    this.emit();
  }

  /** Append a streamed `message.part.delta` to its text/reasoning part. */
  appendPartDelta(id: string, field: string, delta: string): void {
    const part = this.partIndex.get(id);
    if (part?.kind === "text" && field === "text") part.text += delta;
    else if (part?.kind === "reasoning" && field === "reasoning") part.text += delta;
    else return;
    this.touch(this.messageForPart(id));
    this.emit();
  }

  removePart(messageID: string, partID: string): void {
    const message = this.findMessage(messageID);
    if (!message) return;
    message.parts = message.parts.filter((p) => p.id !== partID);
    this.partIndex.delete(partID);
    this.owner.delete(partID);
    this.touch(message);
    this.emit();
  }

  addPermission(permission: Permission): void {
    if (!this.permissions.some((p) => p.id === permission.id)) {
      this.permissions.push(permission);
    }
    this.emit();
  }

  resolvePermission(id: string): void {
    this.permissions = this.permissions.filter((p) => p.id !== id);
    this.emit();
  }

  addQuestion(question: QuestionView): void {
    if (!this.questions.some((q) => q.id === question.id)) {
      this.questions.push(question);
    }
    this.emit();
  }

  resolveQuestion(id: string): void {
    const next = this.questions.filter((q) => q.id !== id);
    if (next.length === this.questions.length) return;
    this.questions = next;
    this.emit();
  }

  setPhase(phase: SessionPhase): void {
    // Leaving the retry phase means the connection is usable again (or the turn
    // ended), so the "Reconnecting" flag is cleared with it.
    const reconnecting = phase === "retry" ? this.reconnecting : false;
    if (this.phase === phase && this.reconnecting === reconnecting) return;
    this.phase = phase;
    this.reconnecting = reconnecting;
    this.emit();
  }

  /** Mark (or clear) an in-flight turn as blocked on a network failure. */
  setReconnecting(value: boolean): void {
    if (this.reconnecting === value) return;
    this.reconnecting = value;
    this.emit();
  }

  setSession(session: Session | undefined): void {
    this.session = session;
    this.emit();
  }

  /** Local (client-side) error rendered as an assistant message. */
  addError(message: string): void {
    this.pushLocal("error", message);
  }

  /** Local status line rendered dim in the transcript. */
  addNotice(message: string): void {
    this.pushLocal("notice", message);
  }

  /** Session record (thinking/model change): kept for copy/sessions, not rendered. */
  addRecord(message: string): void {
    this.pushLocal("record", message);
  }

  /**
   * Show a user message locally when the server won't echo it promptly — a steer
   * admitted through opencode's v2 input queue. Marked `steer` so it folds into
   * the live run; `tagSteers` removes it once the server's own message lands.
   */
  addLocalUserMessage(text: string, steer = true, agent?: string): string {
    const id = `local_user_${Date.now()}_${Math.random().toString(36).slice(2, 8)}`;
    const part: TextView = { kind: "text", id: `${id}_p`, text };
    const view: MessageView = {
      id,
      role: "user",
      ...(agent ? { agent } : {}),
      cost: 0,
      tokens: { input: 0, output: 0, reasoning: 0, cacheRead: 0, cacheWrite: 0 },
      created: Date.now(),
      parts: [part],
      ...(steer ? { steer: true } : {}),
    };
    this.messageIndex.set(view.id, view);
    this.messages.push(view);
    this.touch(view);
    this.emit();
    return id;
  }

  /** Start a `!` shell execution; returns its part id. */
  addBash(command: string, exclude: boolean, at = Date.now()): string {
    const id = `bash_${at}_${Math.random().toString(36).slice(2, 8)}`;
    const part: BashView = { kind: "bash", id, command, output: "", status: "running", exclude };
    const view: MessageView = {
      id: `bashmsg_${id}`,
      role: "assistant",
      cost: 0,
      tokens: { input: 0, output: 0, reasoning: 0, cacheRead: 0, cacheWrite: 0 },
      created: at,
      parts: [part],
    };
    this.messageIndex.set(view.id, view);
    this.partIndex.set(part.id, part);
    this.owner.set(part.id, view.id);
    this.messages.push(view);
    this.touch(view);
    this.emit();
    return id;
  }

  /**
   * Re-insert a persisted `!` block (on resume) at its original position, so
   * shell output survives exiting and reopening a session.
   */
  restoreBash(entry: {
    command: string;
    output: string;
    exclude: boolean;
    status: "complete" | "error" | "cancelled";
    exitCode?: number;
    at: number;
  }): void {
    const id = `bash_${entry.at}_${Math.random().toString(36).slice(2, 8)}`;
    const part: BashView = {
      kind: "bash",
      id,
      command: entry.command,
      output: entry.output,
      status: entry.status,
      exitCode: entry.exitCode,
      exclude: entry.exclude,
    };
    const view: MessageView = {
      id: `bashmsg_${id}`,
      role: "assistant",
      cost: 0,
      tokens: { input: 0, output: 0, reasoning: 0, cacheRead: 0, cacheWrite: 0 },
      created: entry.at,
      parts: [part],
    };
    // Server messages are chronological, so splice by the recorded timestamp.
    let index = this.messages.length;
    for (let i = 0; i < this.messages.length; i++) {
      const created = this.messages[i]!.created;
      if (created !== undefined && created > entry.at) {
        index = i;
        break;
      }
    }
    this.messages.splice(index, 0, view);
    this.messageIndex.set(view.id, view);
    this.partIndex.set(part.id, part);
    this.owner.set(part.id, view.id);
    this.touch(view);
    this.emit();
  }

  appendBashOutput(id: string, chunk: string): void {
    const part = this.partIndex.get(id);
    if (part?.kind !== "bash") return;
    part.output += chunk;
    this.touch(this.messageForPart(id));
    this.emit();
  }

  finishBash(id: string, exitCode: number | undefined, cancelled = false): void {
    const part = this.partIndex.get(id);
    if (part?.kind !== "bash") return;
    part.status = cancelled ? "cancelled" : exitCode === 0 || exitCode === undefined ? "complete" : "error";
    part.exitCode = exitCode;
    this.touch(this.messageForPart(id));
    this.emit();
  }

  private pushLocal(kind: "error" | "notice" | "record", message: string): void {
    const id = `local_${kind}_${Date.now()}_${Math.random().toString(36).slice(2, 8)}`;
    const textPart: TextView = { kind: "text", id: `${id}_p`, text: message };
    const view: MessageView = {
      id,
      role: "assistant",
      cost: 0,
      tokens: { input: 0, output: 0, reasoning: 0, cacheRead: 0, cacheWrite: 0 },
      created: Date.now(),
      parts: kind === "error" ? [] : [textPart],
      ...(kind === "error" ? { error: message } : kind === "notice" ? { notice: true } : { hidden: true }),
    };
    this.messageIndex.set(view.id, view);
    this.messages.push(view);
    this.touch(view);
    this.emit();
  }

  reset(session: Session | undefined): void {
    this.messages = [];
    this.messageIndex.clear();
    this.partIndex.clear();
    this.owner.clear();
    this.permissions = [];
    this.questions = [];
    this.phase = "idle";
    this.reconnecting = false;
    this.session = session;
    this.emit();
  }

  totals(): { cost: number; output: number; input: number; reasoning: number; cacheRead: number } {
    let cost = 0;
    let output = 0;
    let input = 0;
    let reasoning = 0;
    let cacheRead = 0;
    for (const message of this.messages) {
      if (message.role !== "assistant") continue;
      cost += message.cost;
      output += message.tokens.output;
      input += message.tokens.input;
      reasoning += message.tokens.reasoning;
      cacheRead += message.tokens.cacheRead;
    }
    return { cost, output, input, reasoning, cacheRead };
  }

  countTools(): { running: number; total: number } {
    let running = 0;
    let total = 0;
    for (const message of this.messages) {
      for (const part of message.parts) {
        if (part.kind !== "tool") continue;
        total += 1;
        if (part.status === "running" || part.status === "pending") running += 1;
      }
    }
    return { running, total };
  }
}

function partToView(part: Part, existing: PartView | undefined): PartView {
  switch (part.type) {
    case "text":
      if (existing?.kind === "text") {
        // A snapshot may lag the deltas we already applied; never truncate.
        if (part.text.length >= existing.text.length) existing.text = part.text;
        return existing;
      }
      return { kind: "text", id: part.id, text: part.text };
    case "reasoning":
      if (existing?.kind === "reasoning") {
        if (part.text.length >= existing.text.length) existing.text = part.text;
        existing.ended = Boolean(part.time?.end);
        existing.startedAt = part.time?.start ?? existing.startedAt;
        existing.endedAt = part.time?.end ?? existing.endedAt;
        return existing;
      }
      return {
        kind: "reasoning",
        id: part.id,
        text: part.text,
        ended: Boolean(part.time?.end),
        startedAt: part.time?.start,
        endedAt: part.time?.end,
      };
    case "tool": {
      const state = part.state;
      const view: ToolView =
        existing?.kind === "tool"
          ? existing
          : {
              kind: "tool",
              id: part.id,
              callID: part.callID,
              tool: part.tool,
              status: "pending",
              input: {},
              start: Date.now(),
            };
      view.callID = part.callID;
      view.tool = part.tool;
      view.status = state.status;
      view.input = (state.input ?? {}) as Record<string, unknown>;
      view.metadata = (state as { metadata?: Record<string, unknown> }).metadata ?? view.metadata;
      if (state.status === "running") {
        view.title = state.title ?? view.title;
        view.start = state.time?.start ?? view.start;
      } else if (state.status === "completed") {
        view.output = state.output;
        view.title = state.title;
        view.start = state.time?.start ?? view.start;
        view.end = state.time?.end;
      } else if (state.status === "error") {
        view.error = state.error;
        view.start = state.time?.start ?? view.start;
        view.end = state.time?.end;
      } else {
        view.title = undefined;
      }
      return view;
    }
    default:
      // step-start / step-finish / patch / snapshot / agent / retry / file
      // are not rendered directly; keep a stable placeholder so part ordering holds.
      if (existing) return existing;
      return { kind: "text", id: part.id, text: "" };
  }
}
