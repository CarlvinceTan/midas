import { spawn, type ChildProcess } from "node:child_process";
import type { OpencodeClient } from "@opencode-ai/sdk";
import type { OpencodeClient as OpencodeV2Client } from "@opencode-ai/sdk/v2";
import { createServerClients, createServerTransport, type ServerTransport } from "./transport.ts";

export interface ServerOptions {
  cwd: string;
  hostname?: string;
  port?: number;
  /** Override the opencode binary (default: `opencode` on PATH). */
  bin?: string;
  /** Merged opencode config (passed inline via OPENCODE_CONFIG_CONTENT). */
  configContent?: string;
  timeoutMs?: number;
  /** Test seam; overrides the per-server Node transport. */
  transportFactory?: () => ServerTransport;
}

export interface RunningServer {
  client: OpencodeClient;
  /**
   * v2 API client. opencode 1.18.30 exposes its durable `session_input` queue
   * (`delivery: "steer" | "queue"`) only under `/api/...`; the v1 client above
   * runs the legacy prompt path and has no `delivery` field. Used to admit
   * steered follow-ups into a running turn.
   */
  clientV2: OpencodeV2Client;
  url: string;
  proc: ChildProcess;
  close(): void;
}

const LISTEN_RE = /on\s+(https?:\/\/[^\s]+)/;

/**
 * Start a headless `opencode serve` and return a ready SDK client.
 * We spawn it ourselves rather than using the SDK helper so we can honor a
 * custom binary path, stream startup errors, and shut down cleanly.
 */
export async function startServer(options: ServerOptions): Promise<RunningServer> {
  const {
    cwd,
    hostname = "127.0.0.1",
    port = 0,
    bin = process.env.MIDAS_OPENCODE_BIN ?? "opencode",
    configContent,
    timeoutMs = 30_000,
    transportFactory = createServerTransport,
  } = options;

  const args = ["serve", `--hostname=${hostname}`, `--port=${port}`];
  // midas supplies its own context; never inherit Claude Code's global
  // `~/.claude/CLAUDE.md` (or per-project CLAUDE.md) into midas sessions.
  //
  // Skills, MCP servers and context are scoped to `~/.midas`, `<cwd>/.midas`
  // and `<cwd>/.agents` (see `midasOpencodeConfig`): disable opencode's own
  // `.claude`/`.agents` discovery and project `.opencode` config so nothing
  // else leaks in.
  const env = {
    ...process.env,
    OPENCODE_DISABLE_CLAUDE_CODE_PROMPT: "1",
    OPENCODE_DISABLE_CLAUDE_CODE_SKILLS: "1",
    OPENCODE_DISABLE_EXTERNAL_SKILLS: "1",
    OPENCODE_DISABLE_PROJECT_CONFIG: "1",
    ...(configContent ? { OPENCODE_CONFIG_CONTENT: configContent } : {}),
  };

  // One transport per server: its agent is destroyed with this server and is
  // never shared with another server, so closing one cannot abort another.
  const transport = transportFactory();
  let proc: ChildProcess | undefined;
  try {
    const child = spawn(bin, args, {
      cwd,
      env,
      stdio: ["ignore", "pipe", "pipe"],
    });
    proc = child;

    const url = await new Promise<string>((resolve, reject) => {
      let output = "";
      const timer = setTimeout(() => {
        child.kill();
        reject(new Error(`opencode server did not start within ${timeoutMs}ms.\n${output}`));
      }, timeoutMs);

      const onData = (chunk: Buffer): void => {
        output += chunk.toString();
        for (const line of output.split("\n")) {
          if (line.includes("opencode server listening")) {
            const match = line.match(LISTEN_RE);
            if (match) {
              clearTimeout(timer);
              resolve(match[1]!);
              return;
            }
          }
        }
      };
      child.stdout?.on("data", onData);
      child.stderr?.on("data", (chunk: Buffer) => {
        output += chunk.toString();
      });
      child.on("error", (error) => {
        clearTimeout(timer);
        reject(new Error(`Failed to launch '${bin}': ${error.message}`));
      });
      child.on("exit", (code) => {
        clearTimeout(timer);
        reject(new Error(`opencode server exited with code ${code}\n${output}`));
      });
    });

    // Version-sensitive: steer delivery depends on the v2 `/api/session/{id}/prompt`
    // route, which requires opencode 1.18.30+. The v2 client's durable prompt lives
    // under `.v2.session.prompt`; `.session.prompt` is the legacy v1 message API.
    const { client, clientV2 } = createServerClients({ baseUrl: url, cwd, fetch: transport.fetch });
    return {
      client,
      clientV2,
      url,
      proc: child,
      close() {
        if (!child.killed) child.kill();
        void transport.close().catch(() => undefined);
      },
    };
  } catch (error) {
    if (proc && proc.exitCode === null && proc.signalCode === null) proc.kill();
    await transport.close().catch(() => undefined);
    throw error;
  }
}
