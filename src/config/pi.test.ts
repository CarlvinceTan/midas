import assert from "node:assert/strict";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";
import { listSkills, midasOpencodeConfig, parseJsonc } from "./pi.ts";

function withSandbox(run: (root: string, cwd: string) => void): void {
  const root = mkdtempSync(join(tmpdir(), "midas-pi-"));
  const cwd = join(root, "project");
  const savedConfigDir = process.env.MIDAS_CONFIG_DIR;
  const savedConfigFile = process.env.MIDAS_CONFIG_FILE;
  mkdirSync(cwd, { recursive: true });
  process.env.MIDAS_CONFIG_DIR = join(root, "midas");
  delete process.env.MIDAS_CONFIG_FILE;
  try {
    run(root, cwd);
  } finally {
    if (savedConfigDir === undefined) delete process.env.MIDAS_CONFIG_DIR;
    else process.env.MIDAS_CONFIG_DIR = savedConfigDir;
    if (savedConfigFile === undefined) delete process.env.MIDAS_CONFIG_FILE;
    else process.env.MIDAS_CONFIG_FILE = savedConfigFile;
    rmSync(root, { recursive: true, force: true });
  }
}

function write(path: string, content: string): void {
  mkdirSync(join(path, ".."), { recursive: true });
  writeFileSync(path, content);
}

test("parseJsonc tolerates comments and trailing commas", () => {
  const parsed = parseJsonc(`{
    // line comment
    "mcp": {
      /* block comment */
      "search": { "type": "local", "command": ["run", "a,}"], },
    },
  }`);
  assert.deepEqual(parsed, { mcp: { search: { type: "local", command: ["run", "a,}"] } } });
});

test("midasOpencodeConfig merges midas dirs into mcp, skills and instructions", () => {
  withSandbox((_root, cwd) => {
    const midas = process.env.MIDAS_CONFIG_DIR!;
    write(join(midas, "midas.jsonc"), `{ "mcp": { "global": { "type": "remote", "url": "https://g" } } }`);
    write(join(cwd, ".midas", "midas.json"), `{ "mcp": { "project": { "type": "local", "command": ["p"] } } }`);
    write(join(cwd, ".agents", "midas.jsonc"), `{ "mcp": { "agents": { "type": "local", "command": ["a"] } } }`);
    mkdirSync(join(midas, "skills", "notion"), { recursive: true });
    write(join(cwd, ".agents", "AGENTS.md"), "# agents");

    const config = midasOpencodeConfig(cwd) as {
      mcp: Record<string, unknown>;
      skills: string[];
      instructions: string[];
    };

    assert.deepEqual(Object.keys(config.mcp).sort(), ["agents", "global", "project"]);
    assert.deepEqual(config.skills, [join(midas, "skills")]);
    assert.deepEqual(config.instructions, [join(cwd, ".agents", "AGENTS.md")]);
  });
});

test("midasOpencodeConfig drops disabled skills by passing individual folders", () => {
  withSandbox((_root, cwd) => {
    const midas = process.env.MIDAS_CONFIG_DIR!;
    for (const name of ["alpha", "beta"]) {
      const dir = join(midas, "skills", name);
      mkdirSync(dir, { recursive: true });
      write(join(dir, "SKILL.md"), `---\nname: ${name}\ndescription: test\n---\n# ${name}\n`);
    }

    const config = midasOpencodeConfig(cwd, { disabledSkills: new Set(["beta"]) }) as { skills: string[] };
    assert.deepEqual(config.skills, [join(midas, "skills", "alpha")], "only the enabled skill folder is passed");

    const none = midasOpencodeConfig(cwd, { disabledSkills: new Set(["alpha", "beta"]) }) as { skills?: string[] };
    assert.equal(none.skills, undefined, "disabling everything passes no skill paths");
  });
});

test("midasOpencodeConfig installs midas-owned agents", () => {
  withSandbox((_root, cwd) => {
    const config = midasOpencodeConfig(cwd) as {
      agent: Record<string, { prompt?: string; permission?: Record<string, unknown> }>;
    };
    for (const name of ["main", "advisor", "explore", "orchestrator", "task", "merge"]) {
      assert.ok(config.agent[name]?.prompt, `${name} prompt missing`);
    }
    // main is lean: no task-board or multitask references at all.
    assert.doesNotMatch(config.agent.main!.prompt!, /task board|multitask|orchestrator|dispatcher/i);
    // advisor/explore are scoped and mention no board pipeline.
    for (const name of ["advisor", "explore"]) {
      assert.doesNotMatch(config.agent[name]!.prompt!, /task board|multitask|orchestrator|dispatcher/i);
    }
    // The orchestrator may ask the user; the task agent may not.
    assert.equal(config.agent.orchestrator!.permission?.question, "allow");
    assert.equal(config.agent.task!.permission?.question, "deny");
  });
});

test("midasOpencodeConfig applies MIDAS_CONFIG_FILE last", () => {
  withSandbox((root, cwd) => {
    const override = join(root, "override.jsonc");
    write(override, `{ "mcp": { "winner": { "type": "local", "command": ["w"] } }, "model": "a/b" }`);
    process.env.MIDAS_CONFIG_FILE = override;
    const config = midasOpencodeConfig(cwd) as { model: string; mcp: Record<string, unknown> };
    assert.equal(config.model, "a/b");
    assert.deepEqual(Object.keys(config.mcp), ["winner"]);
  });
});

test("listSkills includes only folders with a SKILL.md, using its frontmatter name", () => {
  withSandbox((_root, cwd) => {
    const midas = process.env.MIDAS_CONFIG_DIR!;
    mkdirSync(join(midas, "skills", "notion"), { recursive: true });
    write(join(midas, "skills", "notion", "SKILL.md"), "# notion");
    mkdirSync(join(midas, "skills", "career-ops"), { recursive: true });
    write(join(midas, "skills", "career-ops", "SKILL.md"), `---\nname: job-search\ndescription: find jobs\n---\n# jobs`);
    mkdirSync(join(midas, "skills", "work"), { recursive: true });
    write(join(midas, "skills", "work", "whatsapp-share", "x.md"), "# no skill file");
    mkdirSync(join(cwd, ".agents", "skills", "research"), { recursive: true });
    write(join(cwd, ".agents", "skills", "research", "SKILL.md"), "# research");

    const skills = listSkills(cwd);
    assert.deepEqual(
      skills.map((skill) => [skill.name, skill.scope]),
      [
        ["job-search", "global"],
        ["notion", "global"],
        ["research", "local"],
      ],
    );
    assert.equal(skills[0]!.path, join(midas, "skills", "career-ops"));
  });
});
