import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { midasConfigDir } from "../config/pi.ts";

/** A `!` shell execution retained so it survives exiting and resuming. */
export interface StoredBash {
  command: string;
  output: string;
  exclude: boolean;
  status: "complete" | "error" | "cancelled";
  exitCode?: number;
  /** Epoch ms of the run, used to interleave with server messages on restore. */
  at: number;
}

/**
 * A file payload frozen when a queued prompt was prepared. Persisting this keeps
 * an edited/reloaded queue from silently re-reading a file that changed after it
 * was attached (and from sending only its visible label).
 */
export interface StoredFrozenFile {
  id?: string;
  marker: string;
  path: string;
  name: string;
  kind: "image" | "text";
  /** Exact UTF-8 text, for `kind: "text"`. */
  content?: string;
  /** Exact data-URL attachment, for `kind: "image"`. */
  attachment?: { mime: string; filename: string; url: string };
}

/**
 * A generic file chip whose payload could not be persisted. A restored queue
 * must never re-read these from disk: it pauses until the user explicitly
 * reattaches the file (or removes the chip).
 */
export interface StoredUnfrozenFile {
  marker: string;
  path: string;
  id?: string;
  name?: string;
}

/** A queued follow-up retained so an exited session's queue survives a resume. */
export interface StoredQueuedPrompt {
  text: string;
  attachments?: Array<{ mime: string; filename: string; url: string }>;
  /** Editor image chips (marker -> path) so an edited queue keeps them atomic. */
  chips?: Array<{ marker: string; path: string }>;
  /** Editor file chips (marker -> path) so an edited queue keeps them atomic. */
  files?: Array<{ marker: string; path: string; id?: string; name?: string }>;
  /** File payloads read when the prompt was prepared. */
  frozenFiles?: StoredFrozenFile[];
  /** Generic chips from `files` whose payload is unavailable; they need reattach. */
  unfrozenFiles?: StoredUnfrozenFile[];
  /**
   * Chips the app already flagged for reattachment (e.g. a malformed payload or
   * one missing its `frozenFiles` snapshot). Carried into `unfrozenFiles` so the
   * explicit marker survives re-persistence.
   */
  needsReattach?: StoredUnfrozenFile[];
}

/** Client-side session state that opencode does not persist for us. */
export interface StoredSessionState {
  /** Working directory the session was left in (may differ from its origin). */
  cwd?: string;
  bash?: StoredBash[];
  /** Follow-ups still waiting when the session was last exited. */
  queue?: StoredQueuedPrompt[];
}

interface SessionStateRecord extends StoredSessionState {
  updatedAt: number;
}

type Store = Record<string, SessionStateRecord>;

const MAX_SESSIONS = 100;
const MAX_BASH = 200;
const MAX_OUTPUT = 100_000;
const MAX_QUEUE = 50;
const MAX_ATTACHMENT_URL = 1_500_000;
const MAX_FROZEN_CONTENT = 1_500_000;

/**
 * Keep every frozen payload that fits the per-item persistence caps. Only the
 * offending payload is dropped; the rest of the batch stays frozen, so one
 * oversized image can never force a queued text file to be re-read on resume.
 * `writeSessionState` marks the chips those dropped payloads belonged to.
 */
function frozenFilesWithinCaps(files: readonly StoredFrozenFile[] | undefined): StoredFrozenFile[] {
  if (!files || files.length === 0) return [];
  return files.filter((file) => {
    if (file.kind === "image") return (file.attachment?.url.length ?? 0) <= MAX_ATTACHMENT_URL;
    return (file.content?.length ?? 0) <= MAX_FROZEN_CONTENT;
  });
}

export function sessionStatePath(): string {
  return join(midasConfigDir(), "session-state.json");
}

function readStore(): Store {
  try {
    const parsed = JSON.parse(readFileSync(sessionStatePath(), "utf8")) as unknown;
    if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) return {};
    return parsed as Store;
  } catch {
    return {};
  }
}

function writeStore(store: Store): void {
  try {
    mkdirSync(midasConfigDir(), { recursive: true });
    writeFileSync(sessionStatePath(), `${JSON.stringify(store, null, 2)}\n`);
  } catch {
    // Best-effort: a failed write must never break the shell.
  }
}

export function readSessionState(sessionId: string | undefined): StoredSessionState | undefined {
  if (!sessionId) return undefined;
  const entry = readStore()[sessionId];
  if (!entry) return undefined;
  return {
    ...(typeof entry.cwd === "string" ? { cwd: entry.cwd } : {}),
    ...(Array.isArray(entry.bash) ? { bash: entry.bash } : {}),
    ...(Array.isArray(entry.queue) ? { queue: entry.queue } : {}),
  };
}

export function writeSessionState(sessionId: string | undefined, state: StoredSessionState): void {
  if (!sessionId) return;
  const store = readStore();
  const bash = state.bash?.slice(-MAX_BASH).map((entry) => ({
    ...entry,
    output: entry.output.length > MAX_OUTPUT ? entry.output.slice(-MAX_OUTPUT) : entry.output,
  }));
  // Keep the queue bounded; oversized inline attachments are dropped so an
  // image-heavy queue cannot bloat the state file (the text is still kept).
  // Frozen payloads are filtered per file: a batch that contains one over-cap
  // payload still keeps every other file frozen. Each chip left without a
  // payload is recorded explicitly in `unfrozenFiles`, so a resumed queue can
  // pause for an explicit reattach instead of silently re-reading the disk.
  const queue = state.queue?.slice(-MAX_QUEUE).map((item) => {
    const frozenFiles = frozenFilesWithinCaps(item.frozenFiles);
    const frozenKeys = new Set(frozenFiles.map((file) => file.id ?? file.path));
    const unfrozenFiles: StoredUnfrozenFile[] = [];
    const seen = new Set<string>();
    const markUnfrozen = (chip: StoredUnfrozenFile): void => {
      const key = chip.id ?? chip.path;
      if (seen.has(key)) return;
      seen.add(key);
      unfrozenFiles.push(chip);
    };
    for (const chip of item.needsReattach ?? []) markUnfrozen(chip);
    for (const chip of item.files ?? []) {
      if (!frozenKeys.has(chip.id ?? chip.path)) markUnfrozen(chip);
    }
    return {
      text: item.text,
      ...(item.attachments
        ? { attachments: item.attachments.filter((attachment) => attachment.url.length <= MAX_ATTACHMENT_URL) }
        : {}),
      ...(item.chips && item.chips.length > 0 ? { chips: item.chips } : {}),
      ...(item.files && item.files.length > 0 ? { files: item.files } : {}),
      ...(frozenFiles.length > 0 ? { frozenFiles } : {}),
      ...(unfrozenFiles.length > 0 ? { unfrozenFiles } : {}),
    };
  });
  if (!state.cwd && (!bash || bash.length === 0) && (!queue || queue.length === 0)) {
    if (!(sessionId in store)) return;
    delete store[sessionId];
  } else {
    const newest = Object.values(store).reduce((max, record) => Math.max(max, record.updatedAt ?? 0), 0);
    store[sessionId] = {
      ...(state.cwd ? { cwd: state.cwd } : {}),
      ...(bash && bash.length > 0 ? { bash } : {}),
      ...(queue && queue.length > 0 ? { queue } : {}),
      updatedAt: Math.max(Date.now(), newest + 1),
    };
  }
  prune(store);
  writeStore(store);
}

export function deleteSessionState(sessionId: string | undefined): void {
  if (!sessionId) return;
  const store = readStore();
  if (!(sessionId in store)) return;
  delete store[sessionId];
  writeStore(store);
}

function prune(store: Store): void {
  const ids = Object.keys(store);
  if (ids.length <= MAX_SESSIONS) return;
  ids
    .sort((a, b) => (store[b]?.updatedAt ?? 0) - (store[a]?.updatedAt ?? 0))
    .slice(MAX_SESSIONS)
    .forEach((id) => delete store[id]);
}
