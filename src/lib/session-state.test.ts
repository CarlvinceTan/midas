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

test("a queue item whose frozen content exceeds the cap drops the payload and marks the chip", () => {
  withTempConfigDir(() => {
    const chip = { marker: "[File: big.md]", path: "/tmp/big.md", id: "file-big", name: "big.md" };
    writeSessionState("ses_big", {
      queue: [
        {
          text: "[File: big.md]",
          files: [chip],
          frozenFiles: [
            { id: "file-big", marker: "[File: big.md]", path: "/tmp/big.md", name: "big.md", kind: "text" as const, content: "x".repeat(1_500_001) },
          ],
        },
      ],
    });
    const stored = readSessionState("ses_big")!.queue![0]!;
    assert.equal(stored.text, "[File: big.md]");
    assert.deepEqual(stored.files, [chip]);
    assert.equal(stored.frozenFiles, undefined, "the oversized payload is not stored");
    assert.deepEqual(stored.unfrozenFiles, [chip], "the chip is explicitly marked for reattachment");
  });
});

test("an over-cap payload is dropped per file, keeping the rest of the batch frozen", () => {
  withTempConfigDir(() => {
    const imageChip = { marker: "[File: shot.png]", path: "/tmp/shot.png", id: "file-image", name: "shot.png" };
    const textChip = { marker: "[File: notes.md]", path: "/tmp/notes.md", id: "file-text", name: "notes.md" };
    writeSessionState("ses_mixed", {
      queue: [
        {
          text: "[File: shot.png] [File: notes.md]",
          files: [imageChip, textChip],
          frozenFiles: [
            {
              id: "file-image",
              marker: "[File: shot.png]",
              path: "/tmp/shot.png",
              name: "shot.png",
              kind: "image" as const,
              attachment: { mime: "image/png", filename: "shot.png", url: `data:image/png;base64,${"x".repeat(1_500_000)}` },
            },
            {
              id: "file-text",
              marker: "[File: notes.md]",
              path: "/tmp/notes.md",
              name: "notes.md",
              kind: "text" as const,
              content: "KEPT-BODY",
            },
          ],
        },
      ],
    });
    const stored = readSessionState("ses_mixed")!.queue![0]!;
    assert.deepEqual(stored.frozenFiles, [
      {
        id: "file-text",
        marker: "[File: notes.md]",
        path: "/tmp/notes.md",
        name: "notes.md",
        kind: "text" as const,
        content: "KEPT-BODY",
      },
    ], "the text payload survives even though the image payload was dropped");
    assert.deepEqual(stored.unfrozenFiles, [imageChip], "only the dropped image chip needs reattachment");
  });
});

test("a queue with file chips but no frozen payload is marked for reattachment", () => {
  withTempConfigDir(() => {
    const chip = { marker: "[File: legacy.txt]", path: "/tmp/legacy.txt", id: "file-legacy", name: "legacy.txt" };
    writeSessionState("ses_legacy", { queue: [{ text: "[File: legacy.txt]", files: [chip] }] });
    const stored = readSessionState("ses_legacy")!.queue![0]!;
    assert.deepEqual(stored.unfrozenFiles, [chip], "a legacy generic file queue is never treated as valid");
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
