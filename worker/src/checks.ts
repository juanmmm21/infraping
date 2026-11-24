// Sondas http, tcp y dns. fetch y connect se inyectan para probarlas sin salir a internet.
import type { CheckConfig, CheckResult } from "./types.js";

export type FetchFn = (input: string, init?: RequestInit) => Promise<Response>;

export interface TcpSocket {
  opened: Promise<unknown>;
  close(): Promise<void>;
}
export type ConnectFn = (address: { hostname: string; port: number }) => TcpSocket;

export interface ProbeDeps {
  fetch: FetchFn;
  connect: ConnectFn;
  now: () => number;
}

// Un cuerpo enorme no aporta nada a un «contains»; se corta para no gastar memoria del worker.
const MAX_BODY_BYTES = 1 << 20;
export const DOH_URL = "https://cloudflare-dns.com/dns-query";

class TimeoutError extends Error {
  constructor() {
    super("timed out");
    this.name = "TimeoutError";
  }
}

function withTimeout<T>(work: (signal: AbortSignal) => Promise<T>, ms: number): Promise<T> {
  const controller = new AbortController();
  let timer: ReturnType<typeof setTimeout> | undefined;
  const timeout = new Promise<never>((_, reject) => {
    timer = setTimeout(() => {
      controller.abort();
      reject(new TimeoutError());
    }, ms);
  });
  return Promise.race([work(controller.signal), timeout]).finally(() => {
    if (timer !== undefined) clearTimeout(timer);
  });
}

function describe(err: unknown): string {
  if (err instanceof TimeoutError) return "timed out";
  if (err instanceof Error) return err.message;
  return String(err);
}

async function readLimited(resp: Response): Promise<string> {
  if (resp.body === null) return "";
  const reader = resp.body.getReader();
  const chunks: Uint8Array[] = [];
  let size = 0;
  try {
    while (size < MAX_BODY_BYTES) {
      const { done, value } = await reader.read();
      if (done) break;
      chunks.push(value);
      size += value.byteLength;
    }
  } finally {
    await reader.cancel();
  }
  const all = new Uint8Array(size);
  let offset = 0;
  for (const c of chunks) {
    all.set(c, offset);
    offset += c.byteLength;
  }
  return new TextDecoder().decode(all.subarray(0, MAX_BODY_BYTES));
}

async function probeHTTP(c: CheckConfig, deps: ProbeDeps, signal: AbortSignal): Promise<[boolean, string]> {
  const resp = await deps.fetch(c.url, {
    method: c.method,
    headers: { "User-Agent": "infraping", ...c.headers },
    redirect: "follow",
    signal,
  });
  const ok = c.expectStatus.length === 0 ? resp.status >= 200 && resp.status < 400 : c.expectStatus.includes(resp.status);
  if (!ok) {
    await resp.body?.cancel();
    return [false, `unexpected status ${resp.status}`];
  }
  if (c.contains !== "") {
    const body = await readLimited(resp);
    if (!body.includes(c.contains)) return [false, `body does not contain "${c.contains}"`];
  } else {
    await resp.body?.cancel();
  }
  return [true, `status ${resp.status}`];
}

async function probeTCP(c: CheckConfig, deps: ProbeDeps): Promise<[boolean, string]> {
  const i = c.address.lastIndexOf(":");
  const hostname = c.address.slice(0, i).replace(/^\[|\]$/g, "");
  const port = Number(c.address.slice(i + 1));
  const socket = deps.connect({ hostname, port });
  try {
    await socket.opened;
  } catch (err) {
    return [false, `connect ${c.address}: ${describe(err)}`];
  } finally {
    // close() también rechaza si la conexión nunca se abrió; ese error no cambia el resultado.
    await socket.close().catch(() => undefined);
  }
  return [true, "port open"];
}

interface DohAnswer {
  type: number;
  data: string;
}

async function probeDNS(c: CheckConfig, deps: ProbeDeps, signal: AbortSignal): Promise<[boolean, string]> {
  const addresses: string[] = [];
  // A y AAAA, como hace el resolver de Go con LookupHost.
  for (const type of ["A", "AAAA"]) {
    const url = `${DOH_URL}?name=${encodeURIComponent(c.host)}&type=${type}`;
    const resp = await deps.fetch(url, { headers: { Accept: "application/dns-json" }, signal });
    if (!resp.ok) {
      await resp.body?.cancel();
      return [false, `DNS error: resolver answered ${resp.status}`];
    }
    const body = (await resp.json()) as { Status?: number; Answer?: DohAnswer[] };
    if (body.Status === 3) return [false, `DNS: ${c.host} not found`];
    if (body.Status !== 0) return [false, `DNS error: rcode ${String(body.Status)}`];
    for (const a of body.Answer ?? []) {
      if (a.type === 1 || a.type === 28) addresses.push(a.data);
    }
  }
  if (addresses.length === 0) return [false, `DNS: ${c.host} has no addresses`];
  const missing = c.expectIPs.filter((ip) => !addresses.includes(ip));
  if (missing.length > 0) return [false, `${c.host} does not resolve to ${missing.join(", ")}`];
  return [true, `resolves to ${addresses.join(", ")}`];
}

// runCheck nunca lanza: cualquier fallo es un resultado «down» con un mensaje legible.
export async function runCheck(c: CheckConfig, deps: ProbeDeps): Promise<CheckResult> {
  const start = deps.now();
  let ok: boolean;
  let message: string;
  try {
    [ok, message] = await withTimeout((signal) => {
      switch (c.type) {
        case "http":
          return probeHTTP(c, deps, signal);
        case "tcp":
          return probeTCP(c, deps);
        case "dns":
          return probeDNS(c, deps, signal);
      }
    }, c.timeoutMs);
  } catch (err) {
    ok = false;
    message = describe(err);
  }
  const latencyMs = Math.max(0, deps.now() - start);
  if (ok && c.maxLatencyMs > 0 && latencyMs > c.maxLatencyMs) {
    ok = false;
    message = `slow response: ${Math.round(latencyMs)}ms (limit ${c.maxLatencyMs}ms)`;
  }
  return { ok, message, latencyMs, checkedAt: start };
}

export function targetLabel(c: CheckConfig): string {
  switch (c.type) {
    case "http":
      return c.url;
    case "dns":
      return c.host;
    case "tcp":
      return c.address;
  }
}
