import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { midasConfigDir } from "../config/pi.ts";

/** An unsent input draft, keyed by session id so a resume can restore it. */
export interface StoredDraft {
  text: string;
  /** Image chips referenced by `text`, so they re-attach when the draft is sent. */
  attachments?: Array<{ marker: string; path: string }>;
  /** Generic file chips referenced by `text`, so they re-attach when the draft is sent. */
  files?: Array<{ marker: string; path: string; id?: string; name?: string }>;
}

interface DraftRecord extends StoredDraft {
  updatedAt: number;
}

type DraftStore = Record<string, DraftRecord>;

/** Keep this many sessions' drafts before pruning the least recently updated. */
const MAX_DRAFTS = 100;

/** `~/.midas/drafts.json` (respects `MIDAS_CONFIG_DIR`). */
export function draftsFilePath(): string {
  return join(midasConfigDir(), "drafts.json");
}

function readStore(): DraftStore {
  try {
    const parsed = JSON.parse(readFileSync(draftsFilePath(), "utf8")) as unknown;
    if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) return {};
    return parsed as DraftStore;
  } catch {
    return {};
  }
}

function writeStore(store: DraftStore): void {
  try {
    mkdirSync(midasConfigDir(), { recursive: true });
    writeFileSync(draftsFilePath(), `${JSON.stringify(store, null, 2)}\n`);
  } catch {
    // Best-effort: a failed draft write must never break input.
  }
}

/** The unsent draft for a session, or undefined when there is none. */
export function readDraft(sessionId: string | undefined): StoredDraft | undefined {
  if (!sessionId) return undefined;
  const entry = readStore()[sessionId];
  if (!entry || typeof entry.text !== "string") return undefined;
  return {
    text: entry.text,
    ...(Array.isArray(entry.attachments) ? { attachments: entry.attachments } : {}),
    ...(Array.isArray(entry.files) ? { files: entry.files } : {}),
  };
}

/** Persist a session's draft. An empty draft removes the entry. */
export function writeDraft(sessionId: string | undefined, draft: StoredDraft): void {
  if (!sessionId) return;
  const empty =
    draft.text.length === 0 && (draft.attachments?.length ?? 0) === 0 && (draft.files?.length ?? 0) === 0;
  const store = readStore();
  if (empty) {
    if (!(sessionId in store)) return;
    delete store[sessionId];
  } else {
    // Keep timestamps strictly increasing even for same-millisecond writes, so
    // pruning always drops the genuinely oldest entries.
    const newest = Object.values(store).reduce((max, record) => Math.max(max, record.updatedAt ?? 0), 0);
    store[sessionId] = {
      text: draft.text,
      ...(draft.attachments && draft.attachments.length > 0 ? { attachments: draft.attachments } : {}),
      ...(draft.files && draft.files.length > 0 ? { files: draft.files } : {}),
      updatedAt: Math.max(Date.now(), newest + 1),
    };
  }
  prune(store);
  writeStore(store);
}

/** Drop a session's draft (e.g. once its content has been sent). */
export function deleteDraft(sessionId: string | undefined): void {
  if (!sessionId) return;
  const store = readStore();
  if (!(sessionId in store)) return;
  delete store[sessionId];
  writeStore(store);
}

function prune(store: DraftStore): void {
  const ids = Object.keys(store);
  if (ids.length <= MAX_DRAFTS) return;
  ids
    .sort((a, b) => (store[b]?.updatedAt ?? 0) - (store[a]?.updatedAt ?? 0))
    .slice(MAX_DRAFTS)
    .forEach((id) => delete store[id]);
}
