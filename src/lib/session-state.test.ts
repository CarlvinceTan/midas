import test from "node:test";
import assert from "node:assert/strict";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { readSessionState, sessionStatePath, writeSessionState, type StoredBash } from "./session-state.ts";

function withTempConfigDir(run: () => void): void {
  const previous = process.env.MIDAS_CONFIG_DIR;
  const dir = mkdtempSync(join(tmpdir(), "midas-session-state-"));
  process.env.MIDAS_CONFIG_DIR = dir;
  try {
    run();
  } finally {
    if (previous === undefined) delete process.env.MIDAS_CONFIG_DIR;
    else process.env.MIDAS_CONFIG_DIR = previous;
    rmSync(dir, { recursive: true, force: true });
  }
}

const bash = (i: number): StoredBash => ({
  command: `! cmd ${i}`,
  output: `out ${i}\n`,
  exclude: false,
  status: "complete",
  exitCode: 0,
  at: i,
});

test("session state round-trips cwd and `!` history", () => {
  withTempConfigDir(() => {
    writeSessionState("ses_a", { cwd: "/Users/x", bash: [bash(1), bash(2)] });
    assert.deepEqual(readSessionState("ses_a"), { cwd: "/Users/x", bash: [bash(1), bash(2)] });
    assert.equal(readSessionState("ses_b"), undefined);
    assert.equal(readSessionState(undefined), undefined);
  });
});

test("empty session state removes the entry", () => {
  withTempConfigDir(() => {
    writeSessionState("ses_a", { cwd: "/Users/x" });
    assert.equal(readSessionState("ses_a")?.cwd, "/Users/x");
    writeSessionState("ses_a", {});
    assert.equal(readSessionState("ses_a"), undefined);
  });
});

test("`!` history is capped to the most recent entries", () => {
  withTempConfigDir(() => {
    writeSessionState("ses_a", { bash: Array.from({ length: 250 }, (_, i) => bash(i)) });
    const stored = readSessionState("ses_a")!.bash!;
    assert.equal(stored.length, 200);
    assert.equal(stored[0]!.command, "! cmd 50");
    assert.equal(stored[199]!.command, "! cmd 249");
    assert.ok(sessionStatePath().endsWith("session-state.json"));
  });
});

test("session state round-trips the queued follow-ups", () => {
  withTempConfigDir(() => {
    const queue = [
      { text: "first", attachments: [] },
      { text: "second", attachments: [{ mime: "image/png", filename: "s.png", url: "data:image/png;base64,AAAA" }], chips: [{ marker: "[Image: s.png]", path: "/tmp/s.png" }] },
    ];
    writeSessionState("ses_a", { queue });
    assert.deepEqual(readSessionState("ses_a")?.queue, queue);
  });
});

test("session state round-trips queued file chips and their frozen payloads", () => {
  withTempConfigDir(() => {
    const queue = [
      {
        text: "[File: notes.md] summarize",
        attachments: [],
        files: [{ marker: "[File: notes.md]", path: "/tmp/notes.md", id: "file-1", name: "notes.md" }],
        frozenFiles: [
          { id: "file-1", marker: "[File: notes.md]", path: "/tmp/notes.md", name: "notes.md", kind: "text" as const, content: "line1\n\tline2\n" },
        ],
      },
    ];
    writeSessionState("ses_files", { queue });
    assert.deepEqual(readSessionState("ses_files")?.queue, queue);
  });
});

test("a queue item whose frozen content exceeds the cap drops the payload, keeping the chip", () => {
  withTempConfigDir(() => {
    writeSessionState("ses_big", {
      queue: [
        {
          text: "[File: big.md]",
          files: [{ marker: "[File: big.md]", path: "/tmp/big.md" }],
          frozenFiles: [
            { marker: "[File: big.md]", path: "/tmp/big.md", name: "big.md", kind: "text" as const, content: "x".repeat(1_500_001) },
          ],
        },
      ],
    });
    const stored = readSessionState("ses_big")!.queue![0]!;
    assert.equal(stored.text, "[File: big.md]");
    assert.deepEqual(stored.files, [{ marker: "[File: big.md]", path: "/tmp/big.md" }]);
    assert.equal(stored.frozenFiles, undefined, "an oversized payload is dropped so it can be rehydrated, never sent as a bare label");
  });
});

test("a queue on its own persists, is capped, and drops oversized attachments", () => {
  withTempConfigDir(() => {
    writeSessionState("ses_a", { queue: Array.from({ length: 80 }, (_, i) => ({ text: `q${i}`, attachments: [] })) });
    const stored = readSessionState("ses_a")!.queue!;
    assert.equal(stored.length, 50);
    assert.equal(stored[0]!.text, "q30");
    assert.equal(stored[49]!.text, "q79");
    writeSessionState("ses_b", { queue: [{ text: "big", attachments: [{ mime: "image/png", filename: "b.png", url: "x".repeat(1_500_001) }] }] });
    const big = readSessionState("ses_b")!.queue![0]!;
    assert.equal(big.text, "big");
    assert.deepEqual(big.attachments, []);
  });
});
