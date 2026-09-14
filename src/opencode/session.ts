import type { Event, OpencodeClient, Part, Permission, Session } from "@opencode-ai/sdk";
import type { OpencodeClient as OpencodeV2Client } from "@opencode-ai/sdk/v2";
import { Transcript, isNetworkError, type QuestionView, type SessionPhase } from "../state/transcript.ts";
import type { PromptAttachment } from "../lib/attachments.ts";
import { BOARD_WORKER_AGENT, DEFAULT_INTERACTIVE_AGENT, ORCHESTRATOR_AGENT, type AgentPermissionRule } from "../lib/agents.ts";

export interface ModelChoice {
  providerID: string;
  modelID: string;
  name: string;
  providerName: string;
  /** USD per 1M tokens, from opencode's provider catalog. */
  cost?: { input: number; output: number; cacheRead?: number; cacheWrite?: number };
  contextLimit?: number;
  /** Whether the model emits reasoning tokens (gates character-based rate estimates). */
  reasoning?: boolean;
}

export interface AgentChoice {
  name: string;
  description?: string;
  mode: string;
  /** Model configured on the agent itself (opencode config), if any. */
  model?: { providerID: string; modelID: string };
  /** Ordered rules used to derive which agents can invoke this one. */
  permission?: AgentPermissionRule[];
  /** Built-in agents that should not appear as callers. */
  hidden?: boolean;
}

export interface CommandChoice {
  name: string;
  description?: string;
}

export interface AuthPrompt {
  type: "select" | "text";
  key: string;
  message: string;
  placeholder?: string;
  options?: Array<{ label: string; value: string; hint?: string }>;
  when?: { key: string; op: string; value: string };
}

export interface AuthMethod {
  type: string;
  label: string;
  prompts?: AuthPrompt[];
}

/** Aggregated usage for one session or a group of sessions. */
export interface UsageTotals {
  cost: number;
  input: number;
  output: number;
  reasoning: number;
  cacheRead: number;
  cacheWrite: number;
  /** Assistant messages counted. */
  messages: number;
}

interface UsageMessage {
  role?: string;
  cost?: number;
  tokens?: {
    input?: number;
    output?: number;
    reasoning?: number;
    cache?: { read?: number; write?: number };
  };
}

function emptyUsage(): UsageTotals {
  return { cost: 0, input: 0, output: 0, reasoning: 0, cacheRead: 0, cacheWrite: 0, messages: 0 };
}

/** Sum two usage totals (used to aggregate a scope). */
export function addUsage(a: UsageTotals, b: UsageTotals): UsageTotals {
  return {
    cost: a.cost + b.cost,
    input: a.input + b.input,
    output: a.output + b.output,
    reasoning: a.reasoning + b.reasoning,
    cacheRead: a.cacheRead + b.cacheRead,
    cacheWrite: a.cacheWrite + b.cacheWrite,
    messages: a.messages + b.messages,
  };
}

export interface SessionControllerOptions {
  client: OpencodeClient;
  /**
   * v2 API client, used to admit steered follow-ups through opencode's durable
   * `session_input` queue. Optional so older servers (or tests) keep working
   * through the v1 path.
   */
  clientV2?: OpencodeV2Client;
  cwd: string;
}

/**
 * A prompt submitted while the session is busy is a steer: opencode should
 * admit it into the running turn at its next step boundary. An idle session
 * starts a fresh turn instead.
 */
export function shouldSteer(phase: SessionPhase): boolean {
  return phase !== "idle";
}

/** The v2 `session.prompt` payload for a steer, with attachments remapped. */
export interface SteerPrompt {
  prompt: { text: string; files?: Array<{ uri: string; name: string }> };
  delivery: "steer";
}

/**
 * Map a Midas prompt onto the v2 `session.prompt` body. v1 file parts carry
 * `{ url, filename }`; the v2 queue expects `{ uri, name }` (a data URL works).
 */
export function steerPrompt(text: string, attachments: readonly PromptAttachment[] = []): SteerPrompt {
  const files = attachments.map((file) => ({ uri: file.url, name: file.filename }));
  return { prompt: { text, ...(files.length > 0 ? { files } : {}) }, delivery: "steer" };
}

/** SDK events use both top-level and nested session identities. */
export function eventSessionId(event: Event): string | undefined {
  const props = event.properties as unknown as { sessionID?: string; info?: { sessionID?: string; id?: string }; part?: { sessionID?: string } };
  return props.sessionID ?? props.part?.sessionID ?? props.info?.sessionID ?? (event.type.startsWith("session.") ? props.info?.id : undefined);
}

/**
 * Binds one opencode session to a Transcript: subscribes to the global event
 * stream, filters by session id, and exposes prompt/abort/permission action.
 */
export class SessionController {
  readonly transcript = new Transcript();
  private client: OpencodeClient;
  private clientV2: OpencodeV2Client | undefined;
  private cwd: string;
  private sessionId: string | undefined;
  private sessionTitle: string | undefined;
  /** Per-session usage totals, cached for `/stats`. */
  private usageCache = new Map<string, UsageTotals>();
  private eventAbort: AbortController | undefined;
  private model: { providerID: string; modelID: string } | undefined;
  private variant: string | undefined;
  private agent: string = DEFAULT_INTERACTIVE_AGENT;
  /** Serializes event handling so part/message ordering is deterministic. */
  private queue: Promise<void> = Promise.resolve();

  constructor(options: SessionControllerOptions) {
    this.client = options.client;
    this.clientV2 = options.clientV2;
    this.cwd = options.cwd;
  }

  get id(): string | undefined {
    return this.sessionId;
  }

  /** Persisted title of the active session, if the server stored one. */
  get title(): string | undefined {
    return this.sessionTitle;
  }

  async start(): Promise<void> {
    const session = (await this.client.session.create({
      body: {},
      query: { directory: this.cwd },
      signal: AbortSignal.timeout(15_000),
    })) as unknown as Session;
    this.sessionId = session.id;
    this.sessionTitle = session.title;
    this.transcript.reset(session);
    await this.subscribeEvents();
  }

  async resume(sessionId: string): Promise<void> {
    this.sessionId = sessionId;
    await this.subscribeEvents();
    // Fetch the session record first so a resumed run keeps its stored title.
    let info: Session | undefined;
    try {
      info = (await this.client.session.get({
        path: { id: sessionId },
        query: { directory: this.cwd },
        signal: AbortSignal.timeout(15_000),
      })) as unknown as Session;
    } catch {
      // Title restoration is best-effort; messages still load below.
    }
    this.sessionTitle = info?.title;
    const messages = (await this.client.session.messages({
      path: { id: sessionId },
      query: { directory: this.cwd },
      signal: AbortSignal.timeout(20_000),
    })) as unknown as Array<{ info: Parameters<Transcript["upsertMessage"]>[0]; parts: Part[] }>;
    this.transcript.reset(info);
    for (const item of messages) {
      this.transcript.upsertMessage(item.info);
      for (const part of item.parts) this.transcript.upsertPart(part);
    }
  }

  /**
   * Point the controller at a freshly started server, keeping the same session
   * and the same `Transcript` instance. Needed when a change (e.g. disabling a
   * skill) requires new opencode config, which the server reads only at startup.
   */
  async reconnect(options: { client: OpencodeClient; clientV2?: OpencodeV2Client; cwd?: string }): Promise<void> {
    this.client = options.client;
    this.clientV2 = options.clientV2;
    if (options.cwd) this.cwd = options.cwd;
    if (this.sessionId) await this.resume(this.sessionId);
    else await this.subscribeEvents();
  }

  /**
   * Revert the session to just before `messageId` (opencode's undo) and reload
   * the transcript so the reverted prompt and everything after it are hidden.
   */
  async revertTo(messageId: string): Promise<void> {
    if (!this.sessionId) return;
    await this.client.session.revert({
      path: { id: this.sessionId },
      query: { directory: this.cwd },
      body: { messageID: messageId },
    });
    await this.reloadAfterRevert(messageId);
  }

  private async reloadAfterRevert(messageId: string): Promise<void> {
    if (!this.sessionId) return;
    let info: Session | undefined;
    try {
      info = (await this.client.session.get({
        path: { id: this.sessionId },
        query: { directory: this.cwd },
        signal: AbortSignal.timeout(15_000),
      })) as unknown as Session;
    } catch {
      // Keep the previous session info; the message cut still applies below.
    }
    const messages = (await this.client.session.messages({
      path: { id: this.sessionId },
      query: { directory: this.cwd },
      signal: AbortSignal.timeout(20_000),
    })) as unknown as Array<{ info: Parameters<Transcript["upsertMessage"]>[0]; parts: Part[] }>;
    // The server records the revert point; otherwise cut at the given message.
    const cutId = info?.revert?.messageID ?? messageId;
    const cut = messages.findIndex((item) => item.info.id === cutId);
    const kept = cut >= 0 ? messages.slice(0, cut) : messages;
    this.sessionTitle = info?.title ?? this.sessionTitle;
    this.transcript.reset(info);
    for (const item of kept) {
      this.transcript.upsertMessage(item.info);
      for (const part of item.parts) this.transcript.upsertPart(part);
    }
  }

  /** Persist a generated title so it survives exit and shows in the picker. */
  async setTitle(title: string): Promise<void> {
    if (!this.sessionId) return;
    const updated = (await this.client.session.update({
      path: { id: this.sessionId },
      query: { directory: this.cwd },
      body: { title },
    })) as unknown as Session;
    this.sessionTitle = updated?.title ?? title;
    this.transcript.setSession(updated);
  }

  /** List sessions, newest first. `directory = null` lists across all projects. */
  async listSessions(directory: string | null = this.cwd): Promise<Session[]> {
    const query = directory === null ? undefined : { directory };
    const sessions = (await this.client.session.list(query ? { query } : {})) as unknown as Session[];
    return (sessions ?? []).slice().sort((a, b) => (b.time?.updated ?? 0) - (a.time?.updated ?? 0));
  }

  /**
   * Per-session usage totals (cost, tokens, assistant-message count), cached.
   * Fetches messages with a small concurrency cap so a `/stats` pass over every
   * session stays responsive.
   */
  async usageForSessions(
    sessions: Array<{ id: string; directory: string }>,
    concurrency = 8,
  ): Promise<Map<string, UsageTotals>> {
    const result = new Map<string, UsageTotals>();
    const queue = [...sessions];
    const worker = async (): Promise<void> => {
      for (;;) {
        const next = queue.shift();
        if (!next) return;
        try {
          result.set(next.id, await this.sessionUsage(next.id, next.directory));
        } catch {
          result.set(next.id, emptyUsage());
        }
      }
    };
    await Promise.all(Array.from({ length: Math.max(1, Math.min(concurrency, queue.length)) }, worker));
    return result;
  }

  private async sessionUsage(sessionId: string, directory: string): Promise<UsageTotals> {
    // The active session is still growing, so always re-read it.
    const cached = sessionId === this.sessionId ? undefined : this.usageCache.get(sessionId);
    if (cached) return cached;
    const messages = (await this.client.session.messages({
      path: { id: sessionId },
      query: { directory },
      signal: AbortSignal.timeout(20_000),
    })) as unknown as Array<{ info?: UsageMessage }>;
    const usage = emptyUsage();
    for (const item of messages ?? []) {
      const info = item?.info;
      if (!info || info.role !== "assistant") continue;
      usage.cost += info.cost ?? 0;
      const tokens = info.tokens;
      usage.input += tokens?.input ?? 0;
      usage.output += tokens?.output ?? 0;
      usage.reasoning += tokens?.reasoning ?? 0;
      usage.cacheRead += tokens?.cache?.read ?? 0;
      usage.cacheWrite += tokens?.cache?.write ?? 0;
      usage.messages += 1;
    }
    this.usageCache.set(sessionId, usage);
    return usage;
  }

  private async subscribeEvents(): Promise<void> {
    this.eventAbort?.abort();
    const controller = new AbortController();
    this.eventAbort = controller;
    const { stream } = await this.client.event.subscribe({ query: { directory: this.cwd }, signal: controller.signal });
    void (async () => {
      try {
        for await (const event of stream) {
          this.enqueue(event);
        }
      } catch {
        // Stream closed (server shutdown or abort); nothing to recover here.
      }
    })();
  }

  private enqueue(event: Event): void {
    const sessionId = this.sessionId;
    if (!sessionId) return;
    if (eventSessionId(event) !== sessionId) return;
    this.queue = this.queue.then(() => {
      if (this.sessionId !== sessionId) return;
      this.handle(event);
    });
  }

  private handle(event: Event): void {
    // opencode's `question` tool asks the user; the SDK's event union predates
    // it, so match it by name before the typed switch.
    const raw = event as { type: string; properties?: Record<string, unknown> };
    if (raw.type === "question.asked" && raw.properties) {
      this.transcript.addQuestion(raw.properties as unknown as QuestionView);
      return;
    }
    if (raw.type === "question.replied" || raw.type === "question.rejected") {
      const requestID = (raw.properties as { requestID?: string } | undefined)?.requestID;
      if (requestID) this.transcript.resolveQuestion(requestID);
      return;
    }
    // Streaming prose arrives as deltas; the SDK's event union predates this
    // (newer than `message.part.updated`), so match it by name.
    if (raw.type === "message.part.delta" && raw.properties) {
      const { partID, field, delta } = raw.properties as { partID?: string; field?: string; delta?: string };
      if (partID && typeof field === "string" && typeof delta === "string") {
        this.transcript.appendPartDelta(partID, field, delta);
      }
      return;
    }
    switch (event.type) {
      case "message.updated":
        this.transcript.upsertMessage(event.properties.info);
        break;
      case "message.part.updated":
        this.transcript.upsertPart(event.properties.part, event.properties.delta);
        break;
      case "message.removed":
        this.transcript.removeMessage(event.properties.messageID);
        break;
      case "message.part.removed":
        this.transcript.removePart(event.properties.messageID, event.properties.partID);
        break;
      case "session.status":
        if (event.properties.status.type === "retry") {
          this.transcript.setPhase("retry");
          // A retry caused by a dropped connection is a reconnect, not a normal
          // model hiccup, so the live status can say so immediately.
          this.transcript.setReconnecting(isNetworkError(event.properties.status.message));
        } else {
          this.transcript.setPhase(event.properties.status.type === "busy" ? "busy" : "idle");
        }
        break;
      case "session.error": {
        // Some failures arrive as an error without a retry status first; while a
        // turn is in flight, a network error still means "reconnecting".
        const error = event.properties.error as { name?: string; data?: { message?: string } } | undefined;
        const message = error?.data?.message ?? error?.name ?? "";
        if (this.transcript.phase !== "idle" && isNetworkError(message)) {
          this.transcript.setPhase("retry");
          this.transcript.setReconnecting(true);
        }
        break;
      }
      case "session.idle":
        this.transcript.setPhase("idle");
        break;
      case "session.updated":
        this.sessionTitle = event.properties.info.title;
        this.transcript.setSession(event.properties.info);
        break;
      case "permission.updated":
        this.transcript.addPermission(event.properties);
        break;
      case "permission.replied":
        this.transcript.resolvePermission(event.properties.permissionID);
        break;
      default:
        break;
    }
  }

  async prompt(text: string, attachments: PromptAttachment[] = []): Promise<void> {
    if (!this.sessionId) throw new Error("No active session");
    // Decide the delivery from the phase as it was *before* we mark the session
    // busy, so a steer is not mistaken for the fresh turn we start right after.
    const wasBusy = shouldSteer(this.transcript.phase);
    this.transcript.setPhase("busy");
    if (wasBusy && (await this.steer(text, attachments))) return;
    // A steer needs opencode's v2 endpoint (1.18.30+). If the v2 client is
    // absent or the route is unreachable, fall through to the v1 path so the
    // prompt still lands instead of being dropped.
    const body = {
      parts: [
        ...(text ? [{ type: "text" as const, text }] : []),
        ...attachments.map((file) => ({ type: "file" as const, mime: file.mime, filename: file.filename, url: file.url })),
      ],
      ...(this.model ? { model: this.model } : {}),
      ...(this.agent ? { agent: this.agent } : {}),
      ...(this.variant ? { variant: this.variant } : {}),
    };
    await this.client.session.promptAsync({
      path: { id: this.sessionId },
      query: { directory: this.cwd },
      body,
    });
  }

  /**
   * Admit a prompt as a "steer" input via the v2 API, so a running agent picks
   * it up at its next step boundary rather than after the whole loop.
   *
   * Returns whether the steer was admitted. Never throws: a missing client,
   * an unreachable v2 route, or any other failure returns false so the caller
   * can fall back to the v1 prompt path.
   */
  private async steer(text: string, attachments: PromptAttachment[]): Promise<boolean> {
    const sessionId = this.sessionId;
    const clientV2 = this.clientV2;
    if (!sessionId || !clientV2) return false;
    try {
      // With `responseStyle: "data"` a successful admission resolves to the
      // admission record; a rejected request (e.g. a server without the v2
      // route) resolves to `undefined`. Only a real admission is trusted.
      const admitted = await clientV2.v2.session.prompt({ sessionID: sessionId, ...steerPrompt(text, attachments) });
      return admitted !== undefined;
    } catch {
      return false;
    }
  }

  /**
   * Append a user-visible message to the session without asking the model to
   * reply. Used to seed a session with imported context (e.g. continuing a
   * session that started in another agent).
   */
  async addContext(text: string): Promise<void> {
    if (!this.sessionId) throw new Error("No active session");
    await this.client.session.prompt({
      path: { id: this.sessionId },
      query: { directory: this.cwd },
      body: { parts: [{ type: "text" as const, text }], noReply: true },
      signal: AbortSignal.timeout(20_000),
    });
    // Re-read messages so the appended context is visible immediately.
    await this.resume(this.sessionId);
  }

  async abort(): Promise<void> {
    if (!this.sessionId) return;
    await this.client.session.abort({ path: { id: this.sessionId }, query: { directory: this.cwd } });
    this.transcript.setPhase("idle");
  }

  async respondPermission(permissionID: string, response: "once" | "always" | "reject"): Promise<void> {
    if (!this.sessionId) return;
    await this.client.postSessionIdPermissionsPermissionId({
      path: { id: this.sessionId, permissionID },
      body: { response },
      query: { directory: this.cwd },
    });
    this.transcript.resolvePermission(permissionID);
  }

  /** Answer an opencode `question` request. `answers` is one label array per prompt. */
  async answerQuestion(requestID: string, answers: string[][]): Promise<void> {
    await this.questionReply(requestID, answers);
    this.transcript.resolveQuestion(requestID);
  }

  async rejectQuestion(requestID: string): Promise<void> {
    await this.questionReject(requestID);
    this.transcript.resolveQuestion(requestID);
  }

  /**
   * The question routes live only on the v2 client (the SDK's v1 surface predates
   * them). `responseStyle: "data"` rejects on a non-2xx, so a failed reply/reject
   * surfaces instead of silently leaving the prompt in the transcript.
   */
  private async questionReply(requestID: string, answers: string[][]): Promise<void> {
    if (!this.clientV2) throw new Error("Question requests require the v2 API client");
    await this.clientV2.question.reply({ requestID, directory: this.cwd, answers });
  }

  private async questionReject(requestID: string): Promise<void> {
    if (!this.clientV2) throw new Error("Question requests require the v2 API client");
    await this.clientV2.question.reject({ requestID, directory: this.cwd });
  }

  setModel(model: { providerID: string; modelID: string } | undefined): void {
    this.model = model;
  }

  setVariant(variant: string | undefined): void {
    this.variant = variant;
  }

  setAgent(agent: string | undefined): void {
    this.agent = agent === BOARD_WORKER_AGENT ? DEFAULT_INTERACTIVE_AGENT : agent ?? DEFAULT_INTERACTIVE_AGENT;
  }

  /** Update the working directory used for subsequent requests. */
  setCwd(cwd: string): void {
    this.cwd = cwd;
  }

  /** Start a fresh session (clears the transcript). */
  async newSession(): Promise<void> {
    await this.start();
  }

  async deleteSession(id: string): Promise<void> {
    await this.client.session.delete({ path: { id }, query: { directory: this.cwd } });
  }

  /** Compact the current session (opencode summarize). */
  async compact(providerID: string, modelID: string): Promise<boolean> {
    if (!this.sessionId) return false;
    this.transcript.setPhase("busy");
    try {
      const result = (await this.client.session.summarize({
        path: { id: this.sessionId },
        query: { directory: this.cwd },
        body: { providerID, modelID },
      })) as unknown as boolean;
      return Boolean(result);
    } finally {
      this.transcript.setPhase("idle");
    }
  }

  async mcpStatuses(): Promise<Array<{ name: string; status: string }>> {
    const statuses = (await this.client.mcp.status()) as unknown as Record<string, { type?: string }>;
    return Object.entries(statuses ?? {})
      .map(([name, status]) => ({ name, status: status?.type ?? "unknown" }))
      .sort((a, b) => a.name.localeCompare(b.name));
  }

  async connectMcp(name: string): Promise<void> {
    await this.client.mcp.connect({ path: { name }, query: { directory: this.cwd } });
  }

  async disconnectMcp(name: string): Promise<void> {
    await this.client.mcp.disconnect({ path: { name }, query: { directory: this.cwd } });
  }

  /** Providers with their available auth methods and prompts. */
  async providerAuth(): Promise<Array<{ id: string; name: string; methods: AuthMethod[] }>> {
    const [list, methods] = await Promise.all([
      this.client.provider.list() as unknown as Promise<{ all?: Array<{ id: string; name: string }> }>,
      this.client.provider.auth() as unknown as Promise<Record<string, AuthMethod[]>>,
    ]);
    const names = new Map((list?.all ?? []).map((provider) => [provider.id, provider.name]));
    return Object.entries(methods ?? {}).map(([id, entries]) => ({
      id,
      name: names.get(id) ?? id,
      methods: entries ?? [],
    }));
  }

  async setApiKey(providerID: string, key: string, metadata?: Record<string, string>): Promise<void> {
    await this.client.auth.set({
      path: { id: providerID },
      body: { type: "api", key, ...(metadata && Object.keys(metadata).length > 0 ? { metadata } : {}) },
    });
  }

  /**
   * Register an OpenAI-compatible provider from manual details (name, base URL,
   * API key) and store the key. Backs `/login` → API key → Add provider manually.
   */
  async addCustomProvider(id: string, name: string, baseURL: string, apiKey: string): Promise<void> {
    await this.client.config.update({
      query: { directory: this.cwd },
      body: {
        provider: {
          [id]: {
            name,
            npm: "@ai-sdk/openai-compatible",
            options: baseURL ? { baseURL } : {},
            models: {},
          },
        },
      } as never,
    });
    await this.setApiKey(id, apiKey, baseURL ? { baseURL } : undefined);
  }

  async oauthAuthorize(
    providerID: string,
    method: number,
    inputs?: Record<string, string>,
  ): Promise<{ url: string; instructions: string; method: string }> {
    const result = (await this.client.provider.oauth.authorize({
      path: { id: providerID },
      body: { method, ...(inputs ?? {}) } as never,
    })) as unknown as { url: string; instructions: string; method: string };
    return result;
  }

  async oauthCallback(
    providerID: string,
    method: number,
    code?: string,
    inputs?: Record<string, string>,
  ): Promise<void> {
    await this.client.provider.oauth.callback({
      path: { id: providerID },
      body: { method, ...(inputs ?? {}), ...(code ? { code } : {}) } as never,
    });
  }

  /**
   * Generate a short title for the first user message using opencode's `title`
   * agent and the given model.
   */
  async generateTitle(
    source: string,
    model: { providerID: string; modelID: string },
    maxWords: number,
  ): Promise<string | undefined> {
    return this.generateHelperText(
      `In at most ${maxWords} words, name the overarching goal or outcome of the work, as a title (for example "Preparation for deployment for v0.3.0"). Describe the overall objective, never the current activity, tool, command, or agent. Reply with only the title, no quotes.\n\n${source}`,
      model,
      maxWords,
    );
  }

  /**
   * Generate a couple-of-words status describing progress toward the goal,
   * e.g. "Two of five tasks complete". Never tool/command level detail.
   */
  async generateStatus(
    source: string,
    model: { providerID: string; modelID: string },
    maxWords: number,
  ): Promise<string | undefined> {
    return this.generateHelperText(
      `In at most ${maxWords} words, describe the progress made toward the overarching goal, as a short status phrase (for example "Two of five tasks complete" or "Parser implemented, tests running"). Describe progress toward the goal, not the current tool, command, file, or agent. Reply with only the phrase, no quotes.\n\n${source}`,
      model,
      maxWords,
    );
  }

  /**
   * Generate a short board-task title from its contract. Backs the lightweight
   * "title agent"; titles stay stable unless the contract direction changes.
   */
  async generateTaskTitle(
    source: string,
    model: { providerID: string; modelID: string },
    maxWords = 6,
  ): Promise<string | undefined> {
    return this.generateHelperText(
      `In at most ${maxWords} words, name this engineering task as a title (for example "Cache parsed config" or "Fix worktree cleanup"). Describe the change itself, never the tool, command, or agent. Use no punctuation. Reply with only the title, no quotes.\n\n${source}`,
      model,
      maxWords,
    );
  }

  /**
   * Generate a short board-task status: the stage the task is at toward the
   * goal its title names. Changes more often than the title.
   */
  async generateTaskStatus(
    source: string,
    model: { providerID: string; modelID: string },
    maxWords = 5,
  ): Promise<string | undefined> {
    return this.generateHelperText(
      `In at most ${maxWords} words, describe the stage this task is at toward its goal as a short phrase (for example "writing parser tests" or "waiting on merge"). Describe the stage, never the tool, command, or agent. Reply with only the phrase, no quotes.\n\n${source}`,
      model,
      maxWords,
    );
  }

  private helperSessionId: string | undefined;

  private async helperSession(): Promise<string> {
    if (this.helperSessionId) return this.helperSessionId;
    const session = (await this.client.session.create({
      body: {},
      query: { directory: this.cwd },
      signal: AbortSignal.timeout(10_000),
    })) as unknown as Session;
    this.helperSessionId = session.id;
    return session.id;
  }

  private async generateHelperText(
    prompt: string,
    model: { providerID: string; modelID: string },
    maxWords: number,
  ): Promise<string | undefined> {
    const id = await this.helperSession();
    const result = (await this.client.session.prompt({
      path: { id },
      query: { directory: this.cwd },
      // Bounded so a stalled helper can never leave the title/status flags set.
      signal: AbortSignal.timeout(20_000),
      body: { parts: [{ type: "text", text: prompt }], model, agent: "title" },
    })) as unknown as { parts?: Array<{ type: string; text?: string }> };
    const text = dedupeRepeatedLines(
      (result?.parts ?? [])
        .filter((part) => part.type === "text" && typeof part.text === "string")
        .map((part) => part.text!.trim())
        .filter(Boolean)
        .join("\n"),
    );
    return cleanTitle(text, maxWords);
  }

  /** Drop the reusable helper session (e.g. when idle or on directory change). */
  disposeHelper(): void {
    if (this.helperSessionId) {
      void this.client.session.delete({ path: { id: this.helperSessionId }, query: { directory: this.cwd } }).catch(() => undefined);
    }
    this.helperSessionId = undefined;
  }

  async listModels(): Promise<ModelChoice[]> {
    const result = (await this.client.config.providers()) as unknown as {
      providers: Array<{
        id: string;
        name: string;
        models: Record<
          string,
          {
            id: string;
            name: string;
            api?: { id?: string };
            cost?: {
              input: number;
              output: number;
              cache_read?: number;
              cache_write?: number;
              cache?: { read?: number; write?: number };
            };
            limit?: { context?: number };
            capabilities?: { reasoning?: boolean };
          }
        >;
      }>;
    };
    const providers = result?.providers ?? [];
    // Custom providers (e.g. a private gateway) often publish zero cost. Borrow
    // pricing from any configured provider serving the same upstream model,
    // matched by the normalized model / api id (last path segment).
    const key = (id: string | undefined): string => (id ?? "").toLowerCase().split("/").pop()!.replace(/\s+/g, "");
    const published = new Map<string, NonNullable<ModelChoice["cost"]>>();
    for (const provider of providers) {
      for (const model of Object.values(provider.models ?? {})) {
        const cost = model.cost;
        if (!cost || (cost.input <= 0 && cost.output <= 0)) continue;
        const value = {
          input: cost.input,
          output: cost.output,
          cacheRead: cost.cache?.read ?? cost.cache_read,
          cacheWrite: cost.cache?.write ?? cost.cache_write,
        };
        if (model.id) published.set(key(model.id), value);
        if (model.api?.id) published.set(key(model.api.id), value);
      }
    }

    const choices: ModelChoice[] = [];
    for (const provider of providers) {
      for (const model of Object.values(provider.models ?? {})) {
        const cost = model.cost;
        const own: ModelChoice["cost"] | undefined =
          cost && (cost.input > 0 || cost.output > 0)
            ? {
                input: cost.input,
                output: cost.output,
                cacheRead: cost.cache?.read ?? cost.cache_read,
                cacheWrite: cost.cache?.write ?? cost.cache_write,
              }
            : undefined;
        choices.push({
          providerID: provider.id,
          modelID: model.id,
          name: model.name,
          providerName: provider.name,
          cost: own ?? published.get(key(model.api?.id)) ?? published.get(key(model.id)),
          contextLimit: model.limit?.context,
          reasoning: model.capabilities?.reasoning,
        });
      }
    }
    return choices;
  }

  async listAgents(): Promise<AgentChoice[]> {
    const agents = (await this.client.app.agents()) as unknown as Array<AgentChoice & { builtIn?: boolean }>;
    return agents ?? [];
  }

  /**
   * The agent Midas uses by default: `main`, then another user-facing primary.
   * The orchestrator is entered explicitly through `/multitask`; the task agent
   * is reserved for isolated board workers.
   */
  async defaultAgent(): Promise<string | undefined> {
    const internal = new Set(["compaction", "summary", "title", "general", BOARD_WORKER_AGENT, ORCHESTRATOR_AGENT]);
    const agents = await this.listAgents();
    const primary = agents.filter((agent) => agent.mode === "primary" && !internal.has(agent.name));
    return (
      primary.find((agent) => agent.name === DEFAULT_INTERACTIVE_AGENT)?.name ??
      primary[0]?.name
    );
  }

  /** Number of connected MCP servers, for the footer resource count. */
  async mcpServerCount(): Promise<number> {
    return (await this.mcpServerNames()).length;
  }

  /** Names of connected MCP servers, for the startup summary. */
  async mcpServerNames(): Promise<string[]> {
    const statuses = (await this.client.mcp.status()) as unknown as Record<string, { type?: string }>;
    return Object.entries(statuses ?? {})
      .filter(([, status]) => status?.type === "connected")
      .map(([name]) => name)
      .sort((a, b) => a.localeCompare(b));
  }

  /** opencode's configured default model (`provider/model`), if any. */
  async defaultModel(): Promise<{ providerID: string; modelID: string } | undefined> {
    try {
      const config = (await this.client.config.get()) as unknown as { model?: string };
      const value = config?.model;
      if (typeof value !== "string") return undefined;
      const slash = value.indexOf("/");
      if (slash < 0) return undefined;
      return { providerID: value.slice(0, slash), modelID: value.slice(slash + 1) };
    } catch {
      return undefined;
    }
  }

  /** Slash commands available in this project (opencode config + built-ins). */
  async listCommands(): Promise<CommandChoice[]> {
    try {
      const commands = (await this.client.command.list({ query: { directory: this.cwd } })) as unknown as Array<{
        name: string;
        description?: string;
      }>;
      return (commands ?? []).map((command) => ({ name: command.name, description: command.description }));
    } catch {
      return [];
    }
  }

  /** Run a slash command as a new prompt in the session. */
  async runCommand(name: string, args: string): Promise<void> {
    if (!this.sessionId) throw new Error("No active session");
    this.transcript.setPhase("busy");
    await this.client.session.command({
      path: { id: this.sessionId },
      query: { directory: this.cwd },
      body: {
        command: name,
        arguments: args,
        ...(this.model ? { model: `${this.model.providerID}/${this.model.modelID}` } : {}),
      },
    });
  }

  async loadPendingPermission(): Promise<Permission | undefined> {
    return this.transcript.permissions[0];
  }

  dispose(): void {
    this.eventAbort?.abort();
  }
}

/** First non-empty line, stripped of quotes/markdown, limited to maxWords. */
function cleanTitle(text: string, maxWords: number): string {
  const firstLine = text.split(/\r?\n/).map((line) => line.trim()).find((line) => line.length > 0) ?? "";
  const stripped = firstLine.replace(/^["'`#*\-\s]+/, "").replace(/["'`*\s]+$/, "");
  const words = stripped.split(/\s+/).filter(Boolean);
  return (maxWords > 0 ? words.slice(0, maxWords) : words).join(" ");
}

/** Collapse immediately repeated lines, which models sometimes emit twice. */
function dedupeRepeatedLines(text: string): string {
  const out: string[] = [];
  for (const line of text.split(/\r?\n/)) {
    const trimmed = line.trim();
    if (trimmed && out.length > 0 && trimmed === out[out.length - 1]!.trim()) continue;
    out.push(line);
  }
  return out.join("\n");
}
