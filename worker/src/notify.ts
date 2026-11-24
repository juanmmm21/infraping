// Avisos con el mismo texto y los mismos formatos que internal/notify del binario.
import type { FetchFn } from "./checks.js";
import type { Message, NotifierConfig } from "./types.js";

export function title(m: Message): string {
  switch (m.kind) {
    case "down":
      return `🔴 ${m.check} is DOWN`;
    case "recovered":
      return `🟢 ${m.check} is back UP`;
    case "test":
      return "🔔 InfraPing test notification";
  }
}

export function formatDuration(ms: number): string {
  const total = Math.round(ms / 1000);
  const h = Math.floor(total / 3600);
  const m = Math.floor(total / 60) % 60;
  const s = total % 60;
  if (h > 0) return `${h}h ${m}m`;
  if (m > 0) return `${m}m ${s}s`;
  return `${s}s`;
}

function formatAt(at: number): string {
  return new Date(at).toISOString().replace("T", " ").replace(/\.\d+Z$/, " UTC");
}

export function body(m: Message): string {
  const lines: string[] = [];
  if (m.target !== "") lines.push(`Target: ${m.target}`);
  if (m.reason !== "") lines.push(`Reason: ${m.reason}`);
  if (m.kind === "recovered" && m.downtimeMs > 0) lines.push(`Downtime: ${formatDuration(m.downtimeMs)}`);
  lines.push(`At: ${formatAt(m.at)}`);
  return lines.join("\n");
}

function webhookPayload(m: Message): Record<string, unknown> {
  const out: Record<string, unknown> = { kind: m.kind, check: m.check };
  if (m.target !== "") out.target = m.target;
  if (m.reason !== "") out.reason = m.reason;
  out.at = new Date(m.at).toISOString();
  const seconds = Math.floor(m.downtimeMs / 1000);
  if (seconds > 0) out.downtime_seconds = seconds;
  out.title = title(m);
  out.text = body(m);
  return out;
}

interface Request {
  url: string;
  contentType: string;
  body: string;
  headers: Record<string, string>;
}

export function buildRequest(n: NotifierConfig, m: Message): Request {
  const json = (url: string, payload: unknown, headers: Record<string, string> = {}): Request => ({
    url,
    contentType: "application/json",
    body: JSON.stringify(payload),
    headers,
  });
  switch (n.type) {
    case "telegram":
      return json(`https://api.telegram.org/bot${n.token}/sendMessage`, {
        chat_id: n.chatId,
        text: `${title(m)}\n\n${body(m)}`,
        disable_web_page_preview: true,
      });
    case "discord": {
      const color = m.kind === "down" ? 0xef4444 : m.kind === "recovered" ? 0x22c55e : 0x3b82f6;
      return json(n.url, { username: "InfraPing", embeds: [{ title: title(m), description: body(m), color }] });
    }
    case "slack":
      return json(n.url, { text: `*${title(m)}*\n${body(m)}` });
    case "ntfy": {
      const headers: Record<string, string> = { Title: title(m), Tags: "bell" };
      if (m.kind === "down") {
        headers.Priority = "high";
        headers.Tags = "rotating_light";
      } else if (m.kind === "recovered") {
        headers.Tags = "white_check_mark";
      }
      if (n.token !== "") headers.Authorization = `Bearer ${n.token}`;
      return { url: `${n.server}/${encodeURIComponent(n.topic)}`, contentType: "text/plain; charset=utf-8", body: body(m), headers };
    }
    case "webhook":
      return json(n.url, webhookPayload(m), n.headers);
  }
}

const MAX_ERROR_BODY = 512;

export interface SendOptions {
  attempts: number;
  backoffMs: number;
  timeoutMs: number;
  sleep: (ms: number) => Promise<void>;
}

export const defaultSendOptions: SendOptions = {
  attempts: 3,
  backoffMs: 1_000,
  timeoutMs: 15_000,
  sleep: (ms) => new Promise((resolve) => setTimeout(resolve, ms)),
};

async function sendOnce(fetchFn: FetchFn, req: Request, timeoutMs: number): Promise<void> {
  let resp: Response;
  try {
    resp = await fetchFn(req.url, {
      method: "POST",
      headers: { "Content-Type": req.contentType, "User-Agent": "infraping", ...req.headers },
      body: req.body,
      signal: AbortSignal.timeout(timeoutMs),
    });
  } catch (err) {
    // La URL no va en el error: en Telegram y en los webhooks lleva el secreto.
    throw new Error(`post failed: ${err instanceof Error ? err.name : "network error"}`);
  }
  if (resp.status < 200 || resp.status >= 300) {
    const text = (await resp.text()).slice(0, MAX_ERROR_BODY).trim();
    throw new Error(`status ${resp.status}: ${text}`);
  }
  await resp.body?.cancel();
}

// send reintenta con espera creciente: un servicio de avisos caído un momento no debe tragarse una caída.
export async function send(fetchFn: FetchFn, n: NotifierConfig, m: Message, opts: SendOptions = defaultSendOptions): Promise<void> {
  const req = buildRequest(n, m);
  let last: unknown;
  for (let attempt = 1; attempt <= opts.attempts; attempt++) {
    try {
      await sendOnce(fetchFn, req, opts.timeoutMs);
      return;
    } catch (err) {
      last = err;
      if (attempt < opts.attempts) await opts.sleep(opts.backoffMs * 2 ** (attempt - 1));
    }
  }
  throw new Error(`notifier ${n.name}: ${opts.attempts} attempts failed: ${(last as Error).message}`);
}

export function targets(all: NotifierConfig[], names: string[]): NotifierConfig[] {
  if (names.length === 0) return all;
  return all.filter((n) => names.includes(n.name));
}
