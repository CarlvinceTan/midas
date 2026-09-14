import {
  Agent,
  fetch as undiciFetch,
  type RequestInit as UndiciRequestInit,
} from "undici";
import { createOpencodeClient, type OpencodeClient } from "@opencode-ai/sdk";
import { createOpencodeClient as createV2Client, type OpencodeClient as OpencodeV2Client } from "@opencode-ai/sdk/v2";

/**
 * Node transport for the opencode SDK clients.
 *
 * Node's built-in `fetch` is undici, and undici applies implicit transport
 * deadlines to every request: `headersTimeout` and `bodyTimeout` both default
 * to five minutes. Those preempt the caller's much longer `AbortSignal` budget
 * — a Midas worker runs a prompt for up to 60 minutes and a merge agent for up
 * to 20 — so a turn that takes longer than five minutes to produce response
 * headers dies with an unrelated `TypeError: fetch failed`.
 *
 * The SDK's bundled mitigation (`req.timeout = false`) is Bun-specific and a
 * no-op under Node. Node does not export its internal undici classes, so a
 * per-request dispatcher cannot be handed to `globalThis.fetch` using Node's
 * own undici copy without relying on cross-copy class identity. Instead each
 * server owns an explicit undici `Agent` with the transport deadlines
 * disabled, and requests flow through undici's own `fetch` bound to that
 * agent. Requests are rebuilt from the incoming `Request` so body, headers and
 * streamed responses are preserved, the caller's `AbortSignal` is forwarded
 * unchanged, and only undici's implicit timers are removed.
 *
 * This never touches `globalThis.fetch` or the global dispatcher, so unrelated
 * code (and other Midas servers) keep their own transport behavior.
 */
export interface ServerTransport {
  /** `fetch` bound to this transport's dispatcher. */
  fetch: typeof fetch;
  /** Tear down the transport's sockets. Idempotent. */
  close(): Promise<void>;
}

/** Rebuild a fetch call from the SDK's `Request` without losing its body. */
function requestInit(request: Request, init?: RequestInit): UndiciRequestInit {
  const hasBody = request.body !== null;
  return {
    ...init,
    method: request.method,
    headers: request.headers,
    body: request.body,
    ...(hasBody ? { duplex: "half" } : {}),
    redirect: request.redirect,
    signal: request.signal,
  } as unknown as UndiciRequestInit;
}

export function createServerTransport(): ServerTransport {
  const agent = new Agent({ headersTimeout: 0, bodyTimeout: 0 });
  let closed = false;
  const fetchWithAgent = ((input: string | URL | Request, init?: RequestInit) => {
    if (closed) {
      return Promise.reject(new TypeError("opencode transport is closed"));
    }
    if (input instanceof Request) {
      return undiciFetch(
        input.url,
        { ...requestInit(input, init), dispatcher: agent } as UndiciRequestInit,
      ) as unknown as Promise<Response>;
    }
    return undiciFetch(input, {
      ...(init as UndiciRequestInit | undefined),
      dispatcher: agent,
    }) as unknown as Promise<Response>;
  }) as typeof fetch;
  return {
    fetch: fetchWithAgent,
    async close() {
      if (closed) return;
      closed = true;
      // Forceful teardown: a server shutdown must not wait on an in-flight
      // 60-minute prompt. Sockets cancelled here belong only to this server.
      await agent.destroy();
    },
  };
}

export interface ServerClients {
  client: OpencodeClient;
  clientV2: OpencodeV2Client;
}

/**
 * Build both SDK clients against the same server URL and transport. v1 backs
 * the legacy prompt path; v2 exposes the durable `session_input` queue and the
 * question routes. Both must share the repaired transport so neither can be cut
 * off by undici's default deadlines.
 */
export function createServerClients(options: {
  baseUrl: string;
  cwd: string;
  fetch: typeof fetch;
}): ServerClients {
  const { baseUrl, cwd } = options;
  const client = createOpencodeClient({ baseUrl, directory: cwd, responseStyle: "data", fetch: options.fetch });
  const clientV2 = createV2Client({ baseUrl, directory: cwd, responseStyle: "data", fetch: options.fetch });
  return { client, clientV2 };
}
