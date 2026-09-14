import type { BashView, MessageView, ReasoningView, TextView, ToolView } from "../state/transcript.ts";
import { formatMcpDisplayName, mcpServerDisplayName } from "./components/tool-call.ts";

/**
 * Run/segment model behind the ChatGPT-style transcript:
 *
 *   + Worked for 12s
 *     + Ran commands, read files        <- activity chain
 *       ✓ Ran `npm test`
 *       ✓ Read src/a.ts
 *     intermediate assistant text
 *   final assistant text
 *
 * A run starts at each real user message (or at the first message when the
 * transcript opens mid-session). Within a run, consecutive tool/thinking parts
 * form one collapsible activity chain; text parts break chains.
 */

export interface Run {
  id: string;
  /** The user message that starts the run, if the run has one. */
  prompt?: MessageView;
  /** Every message in the run, in transcript order. */
  messages: MessageView[];
}

export type ChainItem =
  | { kind: "reasoning"; part: ReasoningView }
  | { kind: "tool"; part: ToolView };

export type RunSegment =
  | { kind: "text"; id: string; parts: TextView[]; error?: string }
  | { kind: "activity"; id: string; items: ChainItem[] }
  | { kind: "bash"; id: string; part: BashView };

/** Group messages into runs, starting a new run at each real user message. */
export function computeRuns(messages: MessageView[]): Run[] {
  const runs: Run[] = [];
  let run: Run | undefined;
  for (const message of messages) {
    const isUser = message.role === "user" && !message.notice && !message.hidden;
    if (!run || isUser) {
      run = { id: message.id, messages: [] };
      runs.push(run);
      if (isUser) run.prompt = message;
    }
    run.messages.push(message);
  }
  return runs;
}

/** True if a message is a real (non-steer) user turn starter. */
function isAssistantOutput(message: MessageView): boolean {
  return (
    message.role === "assistant" &&
    !message.notice &&
    !message.hidden &&
    (message.parts.length > 0 || Boolean(message.error))
  );
}

/**
 * A steer run stays "pending" until the agent actually responds to it: its user
 * message has landed but no assistant output follows yet. Pending steers render
 * in the transcript, but the agent's in-flight turn stays the live one.
 */
export function isPendingSteerRun(run: Run): boolean {
  if (!run.prompt?.steer) return false;
  return !run.messages.some(isAssistantOutput);
}

/**
 * Id of the run that should stay live. A pending steer renders in the
 * transcript, but the in-flight turn it steers into must remain the active one
 * until the agent picks the steer up.
 */
export function liveRunId(runs: Run[]): string | undefined {
  for (let i = runs.length - 1; i >= 0; i--) {
    if (!isPendingSteerRun(runs[i]!)) return runs[i]!.id;
  }
  return undefined;
}

/**
 * Flatten a run's assistant output into ordered segments. Notice/hidden messages
 * and the user prompt are excluded; callers render those separately.
 */
export function buildRunSegments(run: Run): RunSegment[] {
  const segments: RunSegment[] = [];
  let textParts: TextView[] = [];
  let pendingError: string | undefined;
  let chainItems: ChainItem[] = [];

  const flushText = (): void => {
    if (textParts.length === 0 && !pendingError) return;
    const first = textParts[0];
    segments.push({
      kind: "text",
      id: first ? first.id : `error_${segments.length}`,
      parts: textParts,
      ...(pendingError ? { error: pendingError } : {}),
    });
    textParts = [];
    pendingError = undefined;
  };
  const flushChain = (): void => {
    if (chainItems.length === 0) return;
    segments.push({ kind: "activity", id: chainItems[0]!.part.id, items: chainItems });
    chainItems = [];
  };

  for (const message of run.messages) {
    if (message === run.prompt || message.hidden || message.notice) continue;
    // Each assistant step starts a fresh text segment, so an earlier "let me
    // look" reply is intermediate while the last step is the final answer.
    // Activity chains may still span the boundary when no text separates them.
    flushText();
    for (const part of message.parts) {
      if (part.kind === "text") {
        // An empty text part is a stream artefact, not a separator: keep the
        // chain together so consecutive tools compact into one summary.
        if (part.text.trim().length === 0) continue;
        flushChain();
        textParts.push(part);
      } else if (part.kind === "reasoning") {
        flushText();
        chainItems.push({ kind: "reasoning", part });
      } else if (part.kind === "tool") {
        flushText();
        chainItems.push({ kind: "tool", part });
      } else {
        flushText();
        flushChain();
        segments.push({ kind: "bash", id: part.id, part });
      }
    }
    if (message.error) {
      flushChain();
      pendingError = message.error;
    }
  }
  flushText();
  flushChain();
  return segments;
}

/** Number of failed tool calls in an activity chain. */
export function countFailures(items: ChainItem[]): number {
  let failures = 0;
  for (const item of items) {
    if (item.kind === "tool" && item.part.status === "error") failures += 1;
  }
  return failures;
}

/** True when a tool/thinking item is still in flight. */
export function isLiveItem(item: ChainItem): boolean {
  if (item.kind === "reasoning") return !item.part.ended;
  return item.part.status === "running" || item.part.status === "pending";
}

/** Index of the last text segment that should render as the run's final output. */
export function finalTextIndex(segments: RunSegment[]): number | undefined {
  for (let i = segments.length - 1; i >= 0; i--) {
    const segment = segments[i]!;
    if (segment.kind === "text" && (segment.parts.some((p) => p.text.trim().length > 0) || segment.error)) {
      return i;
    }
  }
  return undefined;
}

/** Wall-clock duration of a run, from stored message/part timestamps only. */
export function runDurationMs(run: Run): number | undefined {
  let start: number | undefined;
  let end: number | undefined;
  const lower = (value: number | undefined): void => {
    if (value === undefined) return;
    start = start === undefined ? value : Math.min(start, value);
  };
  const upper = (value: number | undefined): void => {
    if (value === undefined) return;
    end = end === undefined ? value : Math.max(end, value);
  };
  for (const message of run.messages) {
    lower(message.created);
    upper(message.completed);
    for (const part of message.parts) {
      if (part.kind === "tool") {
        lower(part.start);
        upper(part.end);
      } else if (part.kind === "reasoning") {
        lower(part.startedAt);
        upper(part.endedAt);
      }
    }
  }
  if (start === undefined || end === undefined || end < start) return undefined;
  // Resumed/instant runs can round to zero; never surface "Worked for 0ms".
  return end - start > 0 ? end - start : undefined;
}

function formatLong(totalSeconds: number): string {
  if (totalSeconds < 60) return `${totalSeconds}s`;
  const minutes = Math.floor(totalSeconds / 60);
  const seconds = totalSeconds % 60;
  if (minutes < 60) return seconds === 0 ? `${minutes}m` : `${minutes}m ${seconds}s`;
  const hours = Math.floor(minutes / 60);
  const restMinutes = minutes % 60;
  return restMinutes === 0 ? `${hours}h` : `${hours}h ${restMinutes}m`;
}

export function formatRunDuration(ms: number): string {
  if (ms < 1000) return `${ms}ms`;
  return formatLong(Math.round(ms / 1000));
}

/** Seconds-resolution duration (never milliseconds), for live "Thinking for Xs". */
export function formatSeconds(ms: number): string {
  return formatLong(Math.max(0, Math.floor(ms / 1000)));
}

/**
 * One-line ChatGPT-style summary of an activity chain with counts, e.g.
 * "Read 2 files, edited 1 file, ran 3 commands". Thinking is not named when the
 * chain also contains tools (matches ChatGPT, which hides process details).
 */
export function summarizeChain(items: ChainItem[]): string {
  const order: string[] = [];
  const groups = new Map<string, { verb: string; noun: string; count: number; names?: string[] }>();
  const add = (key: string, verb: string, noun: string): void => {
    const existing = groups.get(key);
    if (existing) {
      existing.count += 1;
      return;
    }
    groups.set(key, { verb, noun, count: 1 });
    order.push(key);
  };
  const addNamed = (key: string, verb: string, noun: string, name: string): void => {
    const existing = groups.get(key);
    if (existing) {
      existing.names ??= [];
      if (!existing.names.includes(name)) existing.names.push(name);
      return;
    }
    groups.set(key, { verb, noun, count: 1, names: [name] });
    order.push(key);
  };
  let reasoning = 0;
  for (const item of items) {
    if (item.kind === "reasoning") {
      reasoning += 1;
      continue;
    }
    const mcp = mcpServerDisplayName(item.part.tool);
    if (mcp) {
      addNamed("mcp", "Used", "MCP", mcp);
      continue;
    }
    switch (item.part.tool) {
      case "read": add("read", "Read", "file"); break;
      case "bash":
      case "shell": add("bash", "Ran", "command"); break;
      case "edit":
      case "multiedit":
      case "write": add("edit", "Edited", "file"); break;
      case "grep": add("grep", "Searched", "pattern"); break;
      case "glob":
      case "find":
      case "list":
      case "ls": add("find", "Explored", "path"); break;
      case "webfetch":
      case "fetch": add("fetch", "Fetched", "page"); break;
      case "skill": {
        const input = item.part.input;
        const raw = input.name ?? input.skill ?? input.skill_name;
        if (typeof raw === "string" && raw.trim()) addNamed("skill", "Used", "skill", formatMcpDisplayName(raw));
        else add("skill", "Used", "skill");
        break;
      }
      default: add("tool", "Used", "tool"); break;
    }
  }
  const parts = order.map((key, index) => {
    const { verb, noun, count, names } = groups.get(key)!;
    const joinedNames = names && names.length > 1
      ? `${names.slice(0, -1).join(", ")} and ${names[names.length - 1]}`
      : names?.[0];
    const label = joinedNames
      ? `${verb} ${joinedNames} ${noun}${names!.length === 1 ? "" : "s"}`
      : `${verb} ${count} ${noun}${count === 1 ? "" : "s"}`;
    return index === 0 ? label : label.charAt(0).toLowerCase() + label.slice(1);
  });
  if (parts.length === 0) {
    if (reasoning > 0) return "Thought through the task";
    return "Worked";
  }
  return parts.slice(0, 3).join(", ");
}
