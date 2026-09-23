#!/usr/bin/env node

import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, relative, resolve } from "node:path";
import { readMidasSessions, upsertMidasSession, midasSessionsPath } from "../../src/lib/session-store.ts";
import { draftsFilePath, readDraft, writeDraft } from "../../src/lib/drafts.ts";
import { readSessionState, sessionStatePath, writeSessionState } from "../../src/lib/session-state.ts";

const root = resolve(import.meta.dirname, "..", "..");
const outFile = join(root, "internal", "sessionstore", "testdata", "reference.json");
const check = process.argv.includes("--check");
const temporary = mkdtempSync(join(tmpdir(), "midas-session-store-oracle-"));
const previousConfig = process.env.MIDAS_CONFIG_DIR;
const originalNow = Date.now;
let now = 1000;
process.env.MIDAS_CONFIG_DIR = temporary;
Date.now = () => now;

try {
  upsertMidasSession({ id: "s1", cwd: "/tmp/one", title: "Real title" });
  now = 900;
  upsertMidasSession({ id: "s1", cwd: "", title: "New session" });
  now = 1100;
  upsertMidasSession({ id: "s1", title: "" });
  now = 1200;
  upsertMidasSession({ id: "s2", cwd: "/tmp/two", title: "Second", createdAt: 5, updatedAt: 5000 });

  now = 2000;
  writeDraft("s1", {
    text: "draft one",
    attachments: [{ marker: "[Image: a.png]", path: "/tmp/a.png" }],
    files: [{ marker: "[File: a.txt]", path: "/tmp/a.txt", id: "f1", name: "a.txt" }],
  });
  now = 1900;
  writeDraft("s2", { text: "draft two" });
  writeDraft("empty", { text: "" });

  now = 3000;
  writeSessionState("s1", {
    cwd: "/work",
    bash: [{ command: "! pwd", output: "/work\n", exclude: false, status: "complete", exitCode: 0, at: 12 }],
    queue: [
      { text: "empty attachments", attachments: [] },
      {
        text: "files",
        files: [
          { marker: "[File: kept.txt]", path: "/tmp/kept.txt", id: "kept", name: "kept.txt" },
          { marker: "[File: missing.txt]", path: "/tmp/missing.txt", id: "missing", name: "missing.txt" },
        ],
        frozenFiles: [
          { id: "kept", marker: "[File: kept.txt]", path: "/tmp/kept.txt", name: "kept.txt", kind: "text", content: "body" },
        ],
        needsReattach: [{ marker: "[File: prior.txt]", path: "/tmp/prior.txt", id: "prior", name: "prior.txt" }],
      },
    ],
    queueHold: true,
  });

  const result = {
    sessions: { read: readMidasSessions(), file: JSON.parse(readFileSync(midasSessionsPath(), "utf8")) },
    drafts: { s1: readDraft("s1"), s2: readDraft("s2"), missing: readDraft("missing") ?? null, file: JSON.parse(readFileSync(draftsFilePath(), "utf8")) },
    state: { read: readSessionState("s1"), file: JSON.parse(readFileSync(sessionStatePath(), "utf8")) },
  };
  const output = JSON.stringify(result, null, 2) + "\n";
  if (check) {
    const current = existsSync(outFile) ? readFileSync(outFile, "utf8") : "";
    if (current !== output) { console.error("gen-session-store-goldens: goldens are stale; re-run without --check"); process.exitCode = 1; }
    else console.log("session store goldens are current");
  } else {
    mkdirSync(join(root, "internal", "sessionstore", "testdata"), { recursive: true });
    writeFileSync(outFile, output, { flag: "w" });
    console.log(`wrote ${relative(root, outFile)}`);
  }
} finally {
  Date.now = originalNow;
  if (previousConfig === undefined) delete process.env.MIDAS_CONFIG_DIR; else process.env.MIDAS_CONFIG_DIR = previousConfig;
  rmSync(temporary, { recursive: true, force: true });
}
