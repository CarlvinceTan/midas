import assert from "node:assert/strict";
import { once } from "node:events";
import { chmod, mkdtemp, rm, writeFile } from "node:fs/promises";
import { createServer, type Server } from "node:http";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";
import { Agent, fetch as undiciFetch, type RequestInit as UndiciRequestInit } from "undici";
import { createServerClients, createServerTransport, type ServerTransport } from "./transport.ts";
import { startServer } from "./server.ts";

/**
 * Deliberately short "default" transport deadline, far below the stall below.
 * undici's H1 header/body deadlines run on a ~1s-resolution "fast timer", so a
 * 1s deadline fires at roughly 1.5s; the fixture stalls 2.5s to leave margin.
 */
const SHORT_TIMEOUT_MS = 1000;
/** How long the fixture withholds headers/body data. */
const STALL_MS = 2500;
/** Opt-in smoke: exceeds the real five-minute undici default. */
const SMOKE = process.env.MIDAS_TRANSPORT_SMOKE === "1";
const SMOKE_STALL_MS = 310_000;

interface Fixture {
  url: string;
  close(): Promise<void>;
}

/**
 * Deterministic local HTTP fixture.
 * - `/fast` responds immediately.
 * - `/slow-headers` withholds the response headers for `stallMs`.
 * - `/slow-body` sends headers and one chunk, then withholds the rest.
 * - `/echo` returns the request method, directory header and body read back.
 */
async function startFixture(stallMs = STALL_MS): Promise<Fixture> {
  const server: Server = createServer((req, res) => {
    res.on("error", () => undefined);
    const path = (req.url ?? "/").split("?")[0];
    if (path === "/echo") {
      const chunks: Buffer[] = [];
      req.on("data", (chunk: Buffer) => chunks.push(chunk));
      req.on("end", () => {
        res.writeHead(200, { "content-type": "application/json", "x-echo-method": req.method ?? "" });
        res.end(
          JSON.stringify({
            method: req.method,
            directory: req.headers["x-opencode-directory"],
            body: Buffer.concat(chunks).toString("utf8"),
          }),
        );
      });
      return;
    }
    if (path === "/slow-headers") {
      const timer = setTimeout(() => {
        if (res.writableEnded || res.destroyed) return;
        res.writeHead(200, { "content-type": "application/json" });
        res.end("[]");
      }, stallMs);
      res.on("close", () => clearTimeout(timer));
      return;
    }
    if (path === "/slow-body") {
      res.writeHead(200, { "content-type": "application/json" });
      res.write("[");
      const timer = setTimeout(() => {
        if (res.writableEnded || res.destroyed) return;
        res.end("]");
      }, stallMs);
      res.on("close", () => clearTimeout(timer));
      return;
    }
    res.writeHead(200, { "content-type": "application/json" });
    res.end("[]");
  });
  server.listen(0, "127.0.0.1");
  await once(server, "listening");
  const address = server.address();
  assert.ok(address && typeof address === "object", "fixture bound to an ephemeral port");
  return {
    url: `http://127.0.0.1:${address.port}`,
    async close() {
      server.closeAllConnections();
      await new Promise<void>((resolve) => server.close(() => resolve()));
    },
  };
}

/**
 * A fetch bound to an explicitly short-lived dispatcher, standing in for the
 * old default behavior where undici's implicit transport deadlines preempt the
 * caller. It must use undici's own `fetch`, not `globalThis.fetch`: Node's
 * built-in fetch rejects an Agent from the standalone undici package with
 * `UND_ERR_INVALID_ARG`, which would make this baseline pass for the wrong
 * reason instead of exercising a real transport timeout.
 */
function shortTimeoutFetch(timeoutMs = SHORT_TIMEOUT_MS): { fetch: typeof fetch; close(): Promise<void> } {
  const agent = new Agent({ headersTimeout: timeoutMs, bodyTimeout: timeoutMs });
  const bound = ((input: string | URL | Request, init?: RequestInit) =>
    undiciFetch(input instanceof Request ? input.url : input, {
      ...(init as UndiciRequestInit | undefined),
      dispatcher: agent,
    })) as unknown as typeof fetch;
  return { fetch: bound, close: () => agent.destroy() };
}

test("baseline fetch fails when the fixture stalls past its transport deadline", async () => {
  const fixture = await startFixture();
  const baseline = shortTimeoutFetch();
  try {
    // Headers never arrive in time: the fetch itself rejects.
    await assert.rejects(baseline.fetch(`${fixture.url}/slow-headers`), (error: unknown) => {
      return (error as { cause?: { code?: string } }).cause?.code === "UND_ERR_HEADERS_TIMEOUT";
    });
    // Headers arrive but the body stalls: the fetch resolves, then reading the
    // body rejects. This is the same five-minute class of failure.
    const response = await baseline.fetch(`${fixture.url}/slow-body`);
    assert.equal(response.status, 200);
    await assert.rejects(response.text(), (error: unknown) => {
      return (error as { cause?: { code?: string } }).cause?.code === "UND_ERR_BODY_TIMEOUT";
    });
  } finally {
    await baseline.close();
    await fixture.close();
  }
});

test("configured transport waits through delayed response headers", async () => {
  const fixture = await startFixture();
  const transport = createServerTransport();
  try {
    const response = await transport.fetch(`${fixture.url}/slow-headers`);
    assert.equal(response.status, 200);
    assert.equal(await response.text(), "[]");
  } finally {
    await transport.close();
    await fixture.close();
  }
});

test("configured transport waits through a stalled response body", async () => {
  const fixture = await startFixture();
  const transport = createServerTransport();
  try {
    const response = await transport.fetch(`${fixture.url}/slow-body`);
    assert.equal(response.status, 200);
    assert.equal(await response.text(), "[]");
  } finally {
    await transport.close();
    await fixture.close();
  }
});

test("configured transport still cancels promptly on the caller's AbortSignal", async () => {
  const fixture = await startFixture();
  const transport = createServerTransport();
  try {
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(new Error("caller cancelled")), 50);
    const started = Date.now();
    await assert.rejects(
      transport.fetch(`${fixture.url}/slow-headers`, { signal: controller.signal }),
      (error: unknown) => {
        clearTimeout(timer);
        return (
          error instanceof Error &&
          (error.name === "AbortError" || error.message.includes("caller cancelled"))
        );
      },
    );
    assert.ok(Date.now() - started < STALL_MS, "cancellation preempts the fixture stall");
  } finally {
    await transport.close();
    await fixture.close();
  }
});

test("configured transport preserves method, directory header and body", async () => {
  const fixture = await startFixture();
  const transport = createServerTransport();
  try {
    const response = await transport.fetch(`${fixture.url}/echo`, {
      method: "POST",
      headers: { "content-type": "application/json", "x-opencode-directory": encodeURIComponent("/some/dir") },
      body: JSON.stringify({ hello: "world" }),
    });
    assert.equal(response.status, 200);
    const echoed = (await response.json()) as { method: string; directory: string; body: string };
    assert.equal(echoed.method, "POST");
    assert.equal(echoed.directory, encodeURIComponent("/some/dir"), "directory scoping header survives");
    assert.equal(echoed.body, JSON.stringify({ hello: "world" }));
  } finally {
    await transport.close();
    await fixture.close();
  }
});

test("configured transport rebuilds an SDK-style Request without losing its body", async () => {
  const fixture = await startFixture();
  const transport = createServerTransport();
  try {
    // The SDK always hands its fetch a fully-formed Request; this exercises the
    // Request-input path (including `duplex` for the streamed body).
    const request = new Request(`${fixture.url}/echo`, {
      method: "POST",
      headers: { "content-type": "application/json", "x-opencode-directory": encodeURIComponent("/sdk/dir") },
      body: JSON.stringify({ fromRequest: true }),
    });
    const response = await transport.fetch(request);
    assert.equal(response.status, 200);
    const echoed = (await response.json()) as { method: string; directory: string; body: string };
    assert.equal(echoed.method, "POST");
    assert.equal(echoed.directory, encodeURIComponent("/sdk/dir"));
    assert.equal(echoed.body, JSON.stringify({ fromRequest: true }));
  } finally {
    await transport.close();
    await fixture.close();
  }
});

test("both SDK clients route requests through the configured fetch", async () => {
  const fixture = await startFixture();
  const transport = createServerTransport();
  let calls = 0;
  const spyFetch = ((input: string | URL | Request, init?: RequestInit) => {
    calls += 1;
    return transport.fetch(input, init);
  }) as typeof fetch;
  try {
    const { client, clientV2 } = createServerClients({ baseUrl: fixture.url, cwd: "/tmp", fetch: spyFetch });
    calls = 0;
    await client.app.agents();
    assert.equal(calls, 1, "v1 client used the configured fetch exactly once");
    calls = 0;
    await clientV2.app.agents();
    assert.equal(calls, 1, "v2 client used the configured fetch exactly once");
  } finally {
    await transport.close();
    await fixture.close();
  }
});

test("transport close destroys its sockets and is idempotent", async () => {
  const fixture = await startFixture();
  const transport = createServerTransport();
  try {
    const response = await transport.fetch(`${fixture.url}/fast`);
    assert.equal(response.status, 200);
    await transport.close();
    await transport.close();
    await assert.rejects(transport.fetch(`${fixture.url}/fast`), "destroyed transport rejects new requests");
  } finally {
    await fixture.close();
  }
});

test("startServer destroys its transport when startup fails", async () => {
  let closed = 0;
  const fake: ServerTransport = {
    fetch: async () => new Response("[]"),
    close: async () => {
      closed += 1;
    },
  };
  await assert.rejects(
    startServer({
      cwd: process.cwd(),
      bin: "/definitely/not/a/real/midas-opencode-binary",
      transportFactory: () => fake,
      timeoutMs: 1000,
    }),
  );
  assert.equal(closed, 1, "transport closed exactly once on startup failure");
});

test("startServer close destroys its transport", async () => {
  const dir = await mkdtemp(join(tmpdir(), "midas-transport-"));
  const bin = join(dir, "fake-opencode");
  // `exec` replaces the shell with sleep, so killing the child reaps the last
  // process instead of orphaning a grandchild that keeps stdio open.
  await writeFile(bin, "#!/bin/sh\necho 'opencode server listening on http://127.0.0.1:1'\nexec sleep 30\n");
  await chmod(bin, 0o755);
  let closed = 0;
  const fake: ServerTransport = {
    fetch: async () => new Response("[]"),
    close: async () => {
      closed += 1;
    },
  };
  try {
    const server = await startServer({ cwd: dir, bin, transportFactory: () => fake, timeoutMs: 5000 });
    server.close();
    assert.equal(closed, 1, "transport closed with the server");
    if (server.proc.exitCode === null && server.proc.signalCode === null) {
      await Promise.race([
        once(server.proc, "exit"),
        new Promise((resolve) => setTimeout(resolve, 3000).unref()),
      ]);
    }
    assert.ok(server.proc.exitCode !== null || server.proc.signalCode !== null, "server process exited after close");
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
});

/**
 * Opt-in, >5-minute smoke that crosses undici's real five-minute default.
 *
 * Run: MIDAS_TRANSPORT_SMOKE=1 ./node_modules/.bin/tsx --test src/opencode/transport.test.ts
 *
 * It is skipped by default so the normal suite stays fast and deterministic.
 * No model, network or credentials: the 310s stall is a local fixture.
 */
test("smoke: configured transport outlives undici's five-minute default", { skip: !SMOKE }, async () => {
  const fixture = await startFixture(SMOKE_STALL_MS);
  const baseline = shortTimeoutFetch(300_000);
  const transport = createServerTransport();
  try {
    const started = Date.now();
    await assert.rejects(baseline.fetch(`${fixture.url}/slow-headers`));
    const response = await transport.fetch(`${fixture.url}/slow-headers`);
    assert.equal(response.status, 200);
    assert.ok(Date.now() - started > 300_000, "smoke actually crossed the default deadline");
  } finally {
    await transport.close();
    await baseline.close();
    await fixture.close();
  }
});
