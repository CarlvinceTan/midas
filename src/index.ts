#!/usr/bin/env -S npx tsx
import { startServer } from "./opencode/server.ts";
import { SessionController, type ModelChoice } from "./opencode/session.ts";
import { MidasApp } from "./ui/app.ts";
import { loadPiSettings, midasOpencodeConfig } from "./config/pi.ts";
import { dim } from "./lib/ansi.ts";
import { taskCli } from "./tasks/cli.ts";
import { BOARD_WORKER_AGENT } from "./lib/agents.ts";
import { readCachedModels, writeCachedModels } from "./lib/model-cache.ts";

interface Cli {
  cwd: string;
  model?: string;
  agent?: string;
  session?: string;
  print: boolean;
  listModels: boolean;
  listAgents: boolean;
  help: boolean;
  prompt: string;
}

function parseArgs(argv: string[]): Cli {
  const cli: Cli = {
    cwd: process.cwd(),
    print: false,
    listModels: false,
    listAgents: false,
    help: false,
    prompt: "",
  };
  const positionals: string[] = [];
  for (let i = 0; i < argv.length; i++) {
    const arg = argv[i]!;
    switch (arg) {
      case "--cwd":
      case "-C":
        cli.cwd = argv[++i] ?? cli.cwd;
        break;
      case "--model":
      case "-m":
        cli.model = argv[++i];
        break;
      case "--agent":
      case "-a":
        cli.agent = argv[++i];
        break;
      case "--session":
      case "-s":
        cli.session = argv[++i];
        break;
      case "--print":
      case "-p":
        cli.print = true;
        break;
      case "--list-models":
        cli.listModels = true;
        break;
      case "--list-agents":
        cli.listAgents = true;
        break;
      case "--help":
      case "-h":
        cli.help = true;
        break;
      default:
        positionals.push(arg);
    }
  }
  cli.prompt = positionals.join(" ");
  return cli;
}

function usage(): void {
  process.stdout.write(`midas - opencode agent backend with a pi-tui frontend

Usage:
  midas [options] [prompt...]
  midas task [--cwd DIR] add CONTRACT.json | update ID CONTRACT.json | remove ID | list | run ID | merge ID | cleanup [ID]
  midas task dispatch [--once] [--concurrency N]   run the board autonomously

Options:
  -C, --cwd <dir>        Working directory (default: cwd)
  -m, --model <p/m>      Model as provider/model
  -a, --agent <name>     Agent name
  -s, --session <id>     Resume an existing session
  -p, --print            Headless: stream the reply to stdout and exit
      --list-models      List models and exit
      --list-agents      List agents and exit
  -h, --help             Show this help

Keys:
  enter      send              ctrl+c   abort / quit
  ctrl+t     cycle reasoning
  cmd+enter  steer typed/queued  ctrl+o   expand all details
  super+m    model picker      esc      abort

Commands:
  exit                      quit
  /model /agents /thinking /settings
  /copy                     copy last message or whole session
  /new /compact /sessions   session management
  /tasks                    grouped task board (Tab: groups/worktrees)
  /multitask [on|off]       orchestration + autonomous board runner (default: off)
  /voice [on|off]           dictate into the input with the microphone (default: off)
  /reload                   reload settings, models and resources
  /mcps /skills             manage MCP servers and skills
  /login /logout            manage provider credentials
  /goal                     resume, pause, edit or clear the goal
  /title [words]            set (max two words) or reset the terminal title
  /<command>                opencode command
`);
}

function parseModel(value: string | undefined): { providerID: string; modelID: string } | undefined {
  if (!value) return undefined;
  const slash = value.indexOf("/");
  if (slash < 0) return undefined;
  return { providerID: value.slice(0, slash), modelID: value.slice(slash + 1) };
}

async function runPrint(cli: Cli): Promise<void> {
  const server = await startServer({ cwd: cli.cwd, configContent: JSON.stringify(midasOpencodeConfig(cli.cwd)) });
  const controller = new SessionController({ client: server.client, clientV2: server.clientV2, cwd: cli.cwd });
  try {
    if (cli.session) await controller.resume(cli.session);
    else await controller.start();
    controller.setAgent(cli.agent);

    if (cli.listModels) {
      for (const model of await controller.listModels()) {
        process.stdout.write(`${model.providerID}/${model.modelID}\t${model.name}\n`);
      }
      return;
    }
    if (cli.listAgents) {
      for (const agent of await controller.listAgents()) {
        process.stdout.write(`${agent.name}\t${agent.mode}\t${agent.description ?? ""}\n`);
      }
      return;
    }
    if (!cli.prompt) return;

    const printed = new Map<string, number>();
    let settle: () => void = () => {};
    const settled = new Promise<void>((resolve) => {
      settle = resolve;
    });
    let started = false;
    controller.transcript.subscribe(() => {
      for (const message of controller.transcript.messages) {
        if (message.role !== "assistant") continue;
        for (const part of message.parts) {
          if (part.kind === "text") {
            const shown = printed.get(part.id) ?? 0;
            if (part.text.length > shown) {
              process.stdout.write(part.text.slice(shown));
              printed.set(part.id, part.text.length);
            }
          }
        }
      }
      if (started && controller.transcript.phase === "idle") settle();
    });
    await controller.prompt(cli.prompt);
    started = true;
    await Promise.race([settled, new Promise((r) => setTimeout(r, 120_000))]);
    process.stdout.write("\n");
  } finally {
    controller.dispose();
    server.close();
  }
}

async function runTui(cli: Cli): Promise<void> {
  let server = await startServer({ cwd: cli.cwd, configContent: JSON.stringify(midasOpencodeConfig(cli.cwd)) });
  const controller = new SessionController({ client: server.client, clientV2: server.clientV2, cwd: cli.cwd });
  // Restarting swaps in new config (skills disabled for the session); opencode
  // only reads `skills.paths` at startup, so a toggle needs a fresh server.
  const restartBackend = async (disabledSkills: ReadonlySet<string>) => {
    const next = await startServer({
      cwd: cli.cwd,
      configContent: JSON.stringify(midasOpencodeConfig(cli.cwd, { disabledSkills })),
    });
    const previous = server;
    server = next;
    try {
      previous.close();
    } catch {
      // Already gone.
    }
    return { client: next.client, clientV2: next.clientV2 };
  };
  const settings = loadPiSettings(cli.cwd);
  try {
    if (cli.session) await controller.resume(cli.session);
    else await controller.start();
    controller.setAgent(cli.agent);
    controller.setModel(parseModel(cli.model));

    const choice: ModelChoice | undefined = parseModel(cli.model)
      ? { providerID: parseModel(cli.model)!.providerID, modelID: parseModel(cli.model)!.modelID, name: parseModel(cli.model)!.modelID, providerName: parseModel(cli.model)!.providerID }
      : undefined;

    const app = new MidasApp({
      controller,
      cwd: cli.cwd,
      settings,
      model: choice,
      cachedModels: readCachedModels(),
      cacheModels: writeCachedModels,
      agent: cli.agent,
      opencode: { client: server.client, clientV2: server.clientV2 },
      restartBackend,
    });
    // quit() flushes the unsent input synchronously, so the draft survives an
    // accidentally closed terminal (SIGHUP) as well as Ctrl+C / termination.
    process.on("SIGINT", () => app.quit());
    process.on("SIGTERM", () => app.quit());
    process.on("SIGHUP", () => app.quit());
    await app.run();

    // Print a resume hint on the restored screen, like pi's "resume-hint".
    // Only when the session actually has content (a prompt was sent).
    const resumeId = controller.id;
    const sent = controller.transcript.messages.some(
      (message) => message.role === "user" && !message.notice && !message.hidden,
    );
    if (resumeId && sent && settings.fullscreenExitOutput !== "off") {
      process.stdout.write(`${dim("Resume session:")} midas --session ${resumeId}\n`);
    }
  } finally {
    controller.dispose();
    server.close();
  }
}

async function main(): Promise<void> {
  if (process.argv[2] === "task") return taskCli(process.argv.slice(3));
  const cli = parseArgs(process.argv.slice(2));
  if (cli.agent === BOARD_WORKER_AGENT) {
    throw new Error("Agent 'task' is reserved for autonomous board workers; use 'main' or /multitask");
  }
  if (cli.help) {
    usage();
    return;
  }
  const useTui = !cli.print && !cli.listModels && !cli.listAgents && Boolean(process.stdin.isTTY) && Boolean(process.stdout.isTTY);
  if (useTui) await runTui(cli);
  else await runPrint(cli);
}

main().catch((error) => {
  process.stderr.write(`midas: ${error instanceof Error ? error.message : String(error)}\n`);
  process.exitCode = 1;
});
