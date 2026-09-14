import test from "node:test";
import assert from "node:assert/strict";
import { chmodSync, closeSync, ftruncateSync, mkdirSync, mkdtempSync, openSync, rmSync, symlinkSync, writeFileSync } from "node:fs";
import { homedir, tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { pathToFileURL } from "node:url";
import {
  MAX_BATCH_BYTES,
  MAX_BATCH_FILES,
  MAX_IMAGE_BYTES,
  MAX_TEXT_BYTES,
  parsePastedFilePaths,
  readPromptFile,
  readPromptFiles,
} from "./attachments.ts";

function withTempDir(run: (dir: string) => void): void {
  const dir = mkdtempSync(join(tmpdir(), "midas-attach-"));
  try {
    run(dir);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
}

/** Write a sparse file of exactly `size` bytes without materializing it. */
function sparseFile(path: string, size: number): void {
  const fd = openSync(path, "w");
  try {
    ftruncateSync(fd, size);
  } finally {
    closeSync(fd);
  }
}

// ---------------------------------------------------------------------------
// parsePastedFilePaths
// ---------------------------------------------------------------------------

test("parses a single absolute path", () => {
  assert.deepEqual(parsePastedFilePaths("/tmp/notes.md", "/work"), ["/tmp/notes.md"]);
});

test("parses one path per line", () => {
  assert.deepEqual(parsePastedFilePaths("/tmp/one.txt\n/tmp/two.txt", "/work"), ["/tmp/one.txt", "/tmp/two.txt"]);
  assert.deepEqual(parsePastedFilePaths("/tmp/one.txt\r\n/tmp/two.txt\r\n", "/work"), ["/tmp/one.txt", "/tmp/two.txt"]);
});

test("expands ~/ paths", () => {
  assert.deepEqual(parsePastedFilePaths("~/notes.md", "/work"), [join(homedir(), "notes.md")]);
});

test("parses file:// URLs including percent-encoded spaces", () => {
  const target = join(tmpdir(), "My Note.txt");
  assert.deepEqual(parsePastedFilePaths(pathToFileURL(target).href, "/work"), [target]);
});

test("parses quoted paths with spaces and Unicode", () => {
  assert.deepEqual(parsePastedFilePaths(`'/tmp/My File.txt'`, "/work"), ["/tmp/My File.txt"]);
  assert.deepEqual(parsePastedFilePaths(`"/tmp/日本語 メモ.md"`, "/work"), ["/tmp/日本語 メモ.md"]);
  assert.deepEqual(parsePastedFilePaths(`'/tmp/My File.txt' "/tmp/other file.md"`, "/work"), [
    "/tmp/My File.txt",
    "/tmp/other file.md",
  ]);
});

test("parses shell-escaped paths with spaces", () => {
  assert.deepEqual(parsePastedFilePaths("/tmp/My\\ File.txt", "/work"), ["/tmp/My File.txt"]);
  assert.deepEqual(parsePastedFilePaths("/tmp/My\\ File.txt /tmp/other.txt", "/work"), [
    "/tmp/My File.txt",
    "/tmp/other.txt",
  ]);
});

test("accepts bare paths with spaces when the basename has an extension", () => {
  assert.deepEqual(parsePastedFilePaths("/Users/me/Screenshot 2026-09-12 at 8.43.16 pm.png", "/work"), [
    "/Users/me/Screenshot 2026-09-12 at 8.43.16 pm.png",
  ]);
  assert.deepEqual(parsePastedFilePaths("/tmp/日本語 メモ.txt", "/work"), ["/tmp/日本語 メモ.txt"]);
});

test("keeps duplicate basenames from different directories", () => {
  assert.deepEqual(parsePastedFilePaths("/a/same.txt\n/b/same.txt", "/work"), ["/a/same.txt", "/b/same.txt"]);
});

test("normalizes dot segments against cwd", () => {
  assert.deepEqual(parsePastedFilePaths("/tmp/a/../b//c.txt", "/work"), [resolve("/work", "/tmp/b/c.txt")]);
});

test("returns no match for surrounding prose", () => {
  assert.equal(parsePastedFilePaths("can you read /tmp/notes.md?", "/work"), undefined);
  assert.equal(parsePastedFilePaths("please open /tmp/report.txt for me", "/work"), undefined);
  assert.equal(parsePastedFilePaths("/tmp/notes.md please", "/work"), undefined);
  assert.equal(parsePastedFilePaths("read this:\n/tmp/notes.md", "/work"), undefined);
  assert.equal(parsePastedFilePaths("[Image: copied shot.png]", "/work"), undefined);
});

test("returns no match for relative paths, URLs and empties", () => {
  assert.equal(parsePastedFilePaths("./notes.md", "/work"), undefined);
  assert.equal(parsePastedFilePaths("notes.md", "/work"), undefined);
  assert.equal(parsePastedFilePaths("https://example.com/a.txt", "/work"), undefined);
  assert.equal(parsePastedFilePaths("   \n\t  ", "/work"), undefined);
  assert.equal(parsePastedFilePaths("", "/work"), undefined);
  // A lone quoted token that is not a path is still prose.
  assert.equal(parsePastedFilePaths(`"just some words"`, "/work"), undefined);
});

// ---------------------------------------------------------------------------
// readPromptFile
// ---------------------------------------------------------------------------

const PNG_BYTES = Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x01, 0x02, 0xff]);

test("reads an image into the existing PromptAttachment with exact bytes", () => {
  withTempDir((dir) => {
    const path = join(dir, "shot.png");
    writeFileSync(path, PNG_BYTES);
    const result = readPromptFile(path);
    assert.equal(result.ok, true);
    if (!result.ok || result.kind !== "image") throw new Error("expected image");
    assert.equal(result.filename, "shot.png");
    assert.equal(result.mime, "image/png");
    assert.equal(result.byteLength, PNG_BYTES.byteLength);
    assert.equal(result.attachment.filename, "shot.png");
    assert.equal(result.attachment.mime, "image/png");
    const base64 = result.attachment.url.slice("data:image/png;base64,".length);
    assert.deepEqual(Buffer.from(base64, "base64"), PNG_BYTES);
  });
});

test("reads UTF-8 text verbatim, preserving CRLF, tabs, Unicode and trailing newline", () => {
  withTempDir((dir) => {
    const content = "first\r\n\tsecond line\r\n日本語 — emoji 🎉\n";
    const path = join(dir, "notes.md");
    writeFileSync(path, content, "utf8");
    const result = readPromptFile(path);
    assert.equal(result.ok, true);
    if (!result.ok || result.kind !== "text") throw new Error("expected text");
    assert.equal(result.filename, "notes.md");
    assert.equal(result.mime, "text/markdown");
    assert.equal(result.text, content);
    assert.equal(result.byteLength, Buffer.byteLength(content, "utf8"));
  });
});

test("reads extensionless code files as plain text", () => {
  withTempDir((dir) => {
    const path = join(dir, "Makefile");
    writeFileSync(path, "all:\n\t@echo hi\n", "utf8");
    const result = readPromptFile(path);
    assert.equal(result.ok, true);
    if (!result.ok || result.kind !== "text") throw new Error("expected text");
    assert.equal(result.mime, "text/plain");
    assert.equal(result.text, "all:\n\t@echo hi\n");
  });
});

test("rejects invalid UTF-8, NUL bytes and PDFs as unsupported binary", () => {
  withTempDir((dir) => {
    const invalid = join(dir, "invalid.txt");
    writeFileSync(invalid, Buffer.from([0x61, 0xff, 0xfe, 0x62]));
    assert.deepEqual(errorOf(readPromptFile(invalid)), "unsupported-binary");

    const nul = join(dir, "nul.txt");
    writeFileSync(nul, Buffer.from([0x61, 0x00, 0x62]));
    assert.deepEqual(errorOf(readPromptFile(nul)), "unsupported-binary");

    const pdf = join(dir, "doc.pdf");
    writeFileSync(pdf, "%PDF-1.4\n1 0 obj\n<<>>\nendobj\n%%EOF\n", "utf8");
    assert.deepEqual(errorOf(readPromptFile(pdf)), "unsupported-binary");
  });
});

test("reports directories, missing files and unreadable targets distinctly", (t) => {
  withTempDir((dir) => {
    const sub = join(dir, "sub");
    mkdirSync(sub);
    assert.deepEqual(errorOf(readPromptFile(sub)), "directory");
    assert.deepEqual(errorOf(readPromptFile(join(dir, "nope.txt"))), "missing");

    // A symlink loop makes the stat itself fail reliably, independent of file
    // permission bits (which root would bypass).
    const a = join(dir, "a");
    const b = join(dir, "b");
    symlinkSync(a, b);
    symlinkSync(b, a);
    assert.deepEqual(errorOf(readPromptFile(a)), "unreadable");

    if (typeof process.getuid === "function" && process.getuid() === 0) {
      t.diagnostic("skipping permission-bits check when running as root");
      return;
    }
    const locked = join(dir, "locked.txt");
    writeFileSync(locked, "secret", "utf8");
    chmodSync(locked, 0o000);
    try {
      assert.deepEqual(errorOf(readPromptFile(locked)), "unreadable");
    } finally {
      chmodSync(locked, 0o600);
    }
  });
});

test("enforces per-file text and image size limits without truncation", () => {
  withTempDir((dir) => {
    const bigText = join(dir, "big.txt");
    sparseFile(bigText, MAX_TEXT_BYTES + 1);
    assert.deepEqual(errorOf(readPromptFile(bigText)), "oversize");

    const bigImage = join(dir, "big.png");
    sparseFile(bigImage, MAX_IMAGE_BYTES + 1);
    assert.deepEqual(errorOf(readPromptFile(bigImage)), "oversize");
  });
});

function errorOf(result: ReturnType<typeof readPromptFile>): string | undefined {
  return result.ok ? undefined : result.code;
}

// ---------------------------------------------------------------------------
// readPromptFiles (batch validation)
// ---------------------------------------------------------------------------

test("reads a mixed image/text batch and sums its bytes", () => {
  withTempDir((dir) => {
    const image = join(dir, "shot.png");
    const text = join(dir, "notes.txt");
    writeFileSync(image, PNG_BYTES);
    writeFileSync(text, "hello", "utf8");
    const result = readPromptFiles([image, text]);
    assert.equal(result.ok, true);
    if (!result.ok || !("files" in result)) throw new Error("expected batch success");
    assert.equal(result.files.length, 2);
    assert.equal(result.byteLength, PNG_BYTES.byteLength + 5);
    assert.deepEqual(
      result.files.map((file) => (file.ok ? file.kind : file.code)),
      ["image", "text"],
    );
    const readText = result.files[1];
    assert.equal(readText?.ok && readText.kind === "text" ? readText.text : undefined, "hello");
  });
});

test("rejects a batch with too many files", () => {
  const paths = Array.from({ length: MAX_BATCH_FILES + 1 }, (_, i) => `/tmp/f${i}.txt`);
  const result = readPromptFiles(paths);
  assert.equal(result.ok, false);
  if (result.ok) throw new Error("expected failure");
  assert.equal(result.code, "batch-too-many");
});

test("rejects a batch that exceeds the aggregate byte limit before reading it", () => {
  withTempDir((dir) => {
    const image = join(dir, "big.png");
    sparseFile(image, MAX_IMAGE_BYTES); // allowed on its own
    const text = join(dir, "one.txt");
    writeFileSync(text, "x", "utf8");
    const result = readPromptFiles([image, text]);
    assert.equal(result.ok, false);
    if (result.ok) throw new Error("expected failure");
    assert.equal(result.code, "batch-too-large");
  });
});

test("propagates a per-file failure from a batch", () => {
  withTempDir((dir) => {
    const result = readPromptFiles([join(dir, "missing.txt")]);
    assert.equal(result.ok, false);
    if (result.ok) throw new Error("expected failure");
    assert.equal(result.code, "missing");
  });
});

// MAX_BATCH_BYTES is exported so the follow-up wiring can reason about the
// same budget; reference it here to keep the contract visible.
test("batch limit is at least the per-image limit", () => {
  assert.ok(MAX_BATCH_BYTES >= MAX_IMAGE_BYTES);
});
