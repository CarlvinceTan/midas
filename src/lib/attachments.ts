import { readFileSync, statSync } from "node:fs";
import { basename, extname, join, resolve } from "node:path";
import { homedir } from "node:os";
import { fileURLToPath } from "node:url";

/** A file part sent to the model alongside the text prompt. */
export interface PromptAttachment {
  mime: string;
  filename: string;
  url: string;
}

export interface ImageMatch {
  /** The exact substring in the prompt, replaced when the image is attached. */
  raw: string;
  /** Resolved absolute path to try to read. */
  path: string;
}

const EXT_MIME: Record<string, string> = {
  png: "image/png",
  jpg: "image/jpeg",
  jpeg: "image/jpeg",
  gif: "image/gif",
  webp: "image/webp",
  bmp: "image/bmp",
  heic: "image/heic",
  heif: "image/heif",
  tif: "image/tiff",
  tiff: "image/tiff",
};

/**
 * Advisory MIME types for UTF-8 text, code and documents. Anything not listed
 * falls back to `text/plain`; the bytes are what matter, the type is a hint.
 */
const TEXT_MIME: Record<string, string> = {
  txt: "text/plain",
  text: "text/plain",
  log: "text/plain",
  md: "text/markdown",
  markdown: "text/markdown",
  rst: "text/plain",
  csv: "text/csv",
  tsv: "text/tab-separated-values",
  json: "application/json",
  jsonl: "application/json",
  ndjson: "application/json",
  yaml: "application/yaml",
  yml: "application/yaml",
  toml: "application/toml",
  xml: "application/xml",
  html: "text/html",
  htm: "text/html",
  css: "text/css",
  scss: "text/css",
  less: "text/css",
  js: "text/javascript",
  mjs: "text/javascript",
  cjs: "text/javascript",
  jsx: "text/javascript",
  ts: "text/x-typescript",
  tsx: "text/x-typescript",
  mts: "text/x-typescript",
  cts: "text/x-typescript",
  py: "text/x-python",
  sh: "text/x-shellscript",
  bash: "text/x-shellscript",
  zsh: "text/x-shellscript",
  fish: "text/x-shellscript",
  sql: "application/sql",
  svg: "text/plain",
  gitignore: "text/plain",
};

// A dragged/pasted path: absolute or ~/, optionally a file:// URL, containing
// spaces (macOS screenshot names), ending in an image extension.
const IMAGE_RE = /(?:^|[\s'"(])((?:file:\/\/)?(?:~\/|\/)[^\n]*?\.(?:png|jpe?g|gif|webp|bmp|heic|heif|tiff?))(?=$|[\s'")])/gim;

/** Guard against inlining enormous files into a request body. */
export const MAX_IMAGE_BYTES = 25 * 1024 * 1024;

/** Conservative per-file cap for UTF-8 text, code and documents. */
export const MAX_TEXT_BYTES = 1 * 1024 * 1024;

/** Conservative combined cap for one batch of files handed to a prompt. */
export const MAX_BATCH_BYTES = 25 * 1024 * 1024;

/** Conservative number of files admitted in one batch. */
export const MAX_BATCH_FILES = 8;

/** Find image file paths in a prompt (e.g. a screenshot dragged into the input). */
export function findImagePaths(text: string): ImageMatch[] {
  const matches: ImageMatch[] = [];
  for (const match of text.matchAll(IMAGE_RE)) {
    const raw = match[1]!;
    let path = raw;
    try {
      if (path.startsWith("file://")) path = fileURLToPath(path);
    } catch {
      continue;
    }
    if (path.startsWith("~/")) path = join(homedir(), path.slice(2));
    matches.push({ raw, path });
  }
  return matches;
}

/** Read an image from disk into a base64 data URL, or undefined if unreadable. */
export function readImageAttachment(path: string): PromptAttachment | undefined {
  try {
    const info = statSync(path);
    if (!info.isFile() || info.size > MAX_IMAGE_BYTES) return undefined;
    const ext = path.slice(path.lastIndexOf(".") + 1).toLowerCase();
    const mime = EXT_MIME[ext];
    if (!mime) return undefined;
    return { mime, filename: basename(path), url: `data:${mime};base64,${readFileSync(path).toString("base64")}` };
  } catch {
    return undefined;
  }
}

// ---------------------------------------------------------------------------
// Prompt file reader
// ---------------------------------------------------------------------------

/**
 * Why a file could not be delivered to a prompt. Codes are stable so callers
 * can branch without parsing messages; messages are deliberately generic and
 * never embed file contents.
 */
export type PromptFileErrorCode =
  | "missing"
  | "unreadable"
  | "directory"
  | "unsupported-binary"
  | "oversize";

export interface PromptFileError {
  ok: false;
  code: PromptFileErrorCode;
  message: string;
  path: string;
}

/** An image read as the existing base64 `PromptAttachment` payload. */
export interface PromptImageRead {
  ok: true;
  kind: "image";
  path: string;
  filename: string;
  mime: string;
  byteLength: number;
  attachment: PromptAttachment;
}

/** A UTF-8 text, code or document file delivered verbatim (no normalization). */
export interface PromptTextRead {
  ok: true;
  kind: "text";
  path: string;
  filename: string;
  mime: string;
  byteLength: number;
  /** Exact decoded contents: line endings, tabs and Unicode are preserved. */
  text: string;
}

export type PromptFileRead = PromptImageRead | PromptTextRead | PromptFileError;

export type PromptBatchErrorCode = "batch-too-large" | "batch-too-many";

export interface PromptBatchError {
  ok: false;
  code: PromptBatchErrorCode;
  message: string;
}

export type PromptBatchResult =
  | { ok: true; files: PromptFileRead[]; byteLength: number }
  | PromptBatchError
  | PromptFileError;

// Magic prefixes that are valid UTF-8 but are genuinely binary formats. NUL
// bytes and invalid UTF-8 already catch most binaries; PDFs are the canonical
// case that is ASCII-clean yet unsupported (OpenCode/provider support for
// non-image file parts is not established, so we reject rather than guess).
const BINARY_PREFIXES = [
  "%PDF-",
  "%!PS",
  "\x7fELF",
  "\x89PNG",
  "\xff\xd8\xff",
  "GIF87a",
  "GIF89a",
  "PK\x03\x04",
  "\x1f\x8b",
  "\x1aE\xdf\xa3",
  "OggS",
  "fLaC",
  "RIFF",
];

const extensionOf = (path: string): string => extname(path).slice(1).toLowerCase();

const isImagePath = (path: string): boolean => EXT_MIME[extensionOf(path)] !== undefined;

function looksBinary(bytes: Buffer): boolean {
  if (bytes.includes(0)) return true;
  const head = bytes.subarray(0, 16).toString("latin1");
  return BINARY_PREFIXES.some((prefix) => head.startsWith(prefix));
}

type InspectResult = { ok: true; size: number } | PromptFileError;

function inspectFile(path: string): InspectResult {
  try {
    const info = statSync(path);
    if (info.isDirectory()) return { ok: false, code: "directory", message: "Path is a directory", path };
    if (!info.isFile()) return { ok: false, code: "unreadable", message: "Path is not a regular file", path };
    return { ok: true, size: info.size };
  } catch (error) {
    const code = (error as NodeJS.ErrnoException | undefined)?.code;
    if (code === "ENOENT" || code === "ENOTDIR") return { ok: false, code: "missing", message: "File not found", path };
    return { ok: false, code: "unreadable", message: "File could not be read", path };
  }
}

type ReadBytesResult = { ok: true; bytes: Buffer } | PromptFileError;

function readBytes(path: string): ReadBytesResult {
  try {
    return { ok: true, bytes: readFileSync(path) };
  } catch (error) {
    const code = (error as NodeJS.ErrnoException | undefined)?.code;
    if (code === "ENOENT" || code === "ENOTDIR") return { ok: false, code: "missing", message: "File not found", path };
    if (code === "EISDIR") return { ok: false, code: "directory", message: "Path is a directory", path };
    return { ok: false, code: "unreadable", message: "File could not be read", path };
  }
}

/**
 * Read one local file for prompt attachment. Images return the existing
 * `PromptAttachment` (base64 data URL, exact bytes, <= MAX_IMAGE_BYTES); UTF-8
 * text/code/documents return their exact contents and byte length
 * (<= MAX_TEXT_BYTES). PDF and other binary payloads are rejected honestly
 * because provider support for non-image file parts is not established.
 * No truncation ever happens: a file that is too large fails with `oversize`.
 */
export function readPromptFile(path: string): PromptFileRead {
  const inspected = inspectFile(path);
  if (!inspected.ok) return inspected;

  const filename = basename(path);
  const ext = extensionOf(path);
  const imageMime = EXT_MIME[ext];

  if (imageMime) {
    if (inspected.size > MAX_IMAGE_BYTES) {
      return { ok: false, code: "oversize", message: `Image exceeds ${MAX_IMAGE_BYTES} bytes`, path };
    }
    const read = readBytes(path);
    if (!read.ok) return read;
    const bytes = read.bytes;
    return {
      ok: true,
      kind: "image",
      path,
      filename,
      mime: imageMime,
      byteLength: bytes.byteLength,
      attachment: { mime: imageMime, filename, url: `data:${imageMime};base64,${bytes.toString("base64")}` },
    };
  }

  if (inspected.size > MAX_TEXT_BYTES) {
    return { ok: false, code: "oversize", message: `File exceeds ${MAX_TEXT_BYTES} bytes`, path };
  }
  const read = readBytes(path);
  if (!read.ok) return read;
  const bytes = read.bytes;
  if (looksBinary(bytes)) {
    return { ok: false, code: "unsupported-binary", message: "Binary files are not supported", path };
  }
  let text: string;
  try {
    text = new TextDecoder("utf-8", { fatal: true }).decode(bytes);
  } catch {
    return { ok: false, code: "unsupported-binary", message: "File is not valid UTF-8 text", path };
  }
  return {
    ok: true,
    kind: "text",
    path,
    filename,
    mime: TEXT_MIME[ext] ?? "text/plain",
    byteLength: bytes.byteLength,
    text,
  };
}

/**
 * Read several files with the shared batch limits. Per-file limits are applied
 * first (via `readPromptFile`), then the combined byte budget and file count.
 * Any failure is returned as-is and the whole batch is rejected, so a caller
 * never silently drops a file or truncates one.
 */
export function readPromptFiles(paths: readonly string[]): PromptBatchResult {
  if (paths.length > MAX_BATCH_FILES) {
    return { ok: false, code: "batch-too-many", message: `At most ${MAX_BATCH_FILES} files can be attached` };
  }
  // Inspect sizes up front so an oversized batch is rejected before any large
  // file is pulled into memory.
  let total = 0;
  for (const path of paths) {
    const inspected = inspectFile(path);
    if (!inspected.ok) return inspected;
    const perFile = isImagePath(path) ? MAX_IMAGE_BYTES : MAX_TEXT_BYTES;
    if (inspected.size > perFile) {
      return { ok: false, code: "oversize", message: `File exceeds ${perFile} bytes`, path };
    }
    total += inspected.size;
  }
  if (total > MAX_BATCH_BYTES) {
    return { ok: false, code: "batch-too-large", message: `Files exceed the ${MAX_BATCH_BYTES}-byte batch limit` };
  }
  const files: PromptFileRead[] = [];
  for (const path of paths) {
    const result = readPromptFile(path);
    if (!result.ok) return result;
    files.push(result);
  }
  return { ok: true, files, byteLength: total };
}

// ---------------------------------------------------------------------------
// Pasted path parsing
// ---------------------------------------------------------------------------

// A quote only starts a shell word at the beginning of a line or after
// whitespace, so an apostrophe inside a filename is not mistaken for quoting.
const STRUCTURAL_QUOTE_RE = /(^|\s)['"]/;

// A bare multi-word candidate is only treated as a path when its basename ends
// in a filename extension. This is what separates
// `/Users/me/Screenshot 2026.png` (a path with spaces) from surrounding prose
// such as `/Users/me/report.txt please` (a path plus a sentence).
const FILENAME_EXT_RE = /\.[A-Za-z0-9]{1,10}$/;

/**
 * Tokenize a single shell-like line respecting single quotes, double quotes
 * and backslash escapes. Returns undefined for unterminated quotes/escapes
 * rather than guessing.
 */
function tokenizeLine(line: string): string[] | undefined {
  const tokens: string[] = [];
  let current = "";
  let started = false;
  let quote: "'" | '"' | undefined;
  for (let i = 0; i < line.length; i++) {
    const ch = line[i]!;
    if (quote === "'") {
      if (ch === "'") quote = undefined;
      else current += ch;
      started = true;
    } else if (quote === '"') {
      if (ch === '"') {
        quote = undefined;
      } else if (ch === "\\") {
        const next = line[i + 1];
        if (next === undefined) return undefined;
        current += next;
        i++;
      } else {
        current += ch;
      }
      started = true;
    } else if (ch === "\\") {
      const next = line[i + 1];
      if (next === undefined) return undefined;
      current += next;
      i++;
      started = true;
    } else if (ch === "'" || ch === '"') {
      quote = ch;
      started = true;
    } else if (ch === " " || ch === "\t") {
      if (started) {
        tokens.push(current);
        current = "";
        started = false;
      }
    } else {
      current += ch;
      started = true;
    }
  }
  if (quote !== undefined) return undefined;
  if (started) tokens.push(current);
  return tokens;
}

const needsTokenizing = (line: string): boolean => line.includes("\\") || STRUCTURAL_QUOTE_RE.test(line);

/**
 * Resolve one pasted token to an absolute path, or undefined when it is not an
 * unambiguous absolute path. Only `/...`, `~/...` and `file://` inputs qualify;
 * relative paths, prose and URLs are refused, so a pasted sentence never turns
 * an arbitrary mentioned path into an attachment.
 */
function resolveToken(token: string, cwd: string): string | undefined {
  if (token === "" || token.includes("\u0000")) return undefined;
  if (/\s/.test(token) && !FILENAME_EXT_RE.test(basename(token))) return undefined;
  let expanded: string;
  if (token.startsWith("file://")) {
    try {
      expanded = fileURLToPath(token);
    } catch {
      return undefined;
    }
  } else if (token.startsWith("~/")) {
    expanded = join(homedir(), token.slice(2));
  } else if (token.startsWith("/")) {
    expanded = token;
  } else {
    return undefined;
  }
  if (!expanded.startsWith("/") || expanded.includes("\u0000")) return undefined;
  return resolve(cwd, expanded);
}

/**
 * Parse a clipboard paste into normalized absolute paths, but only when the
 * whole paste is unambiguously path-only. Supports one path per line, quoted
 * paths, shell backslash escapes, spaces and Unicode, plus `~/` and `file://`
 * forms. Returns undefined for anything containing prose, a relative path or a
 * URL. This never touches the filesystem, runs shell syntax or fetches URLs --
 * it is pure syntax, so no directory is scanned and no file is read.
 */
export function parsePastedFilePaths(text: string, cwd: string): string[] | undefined {
  const trimmed = text.trim();
  if (trimmed === "") return undefined;
  const lines = trimmed
    .split(/\r\n|\n|\r/)
    .map((line) => line.trim())
    .filter((line) => line !== "");
  if (lines.length === 0) return undefined;

  const paths: string[] = [];
  for (const line of lines) {
    const candidates = needsTokenizing(line) ? tokenizeLine(line) : [line];
    if (!candidates || candidates.length === 0) return undefined;
    for (const candidate of candidates) {
      const resolved = resolveToken(candidate, cwd);
      if (resolved === undefined) return undefined;
      paths.push(resolved);
    }
  }
  return paths;
}
