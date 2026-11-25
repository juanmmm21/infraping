import { connect } from "cloudflare:sockets";
import styleSheet from "../../internal/status/templates/style.css";
import configText from "../infraping.yaml";
import type { ProbeDeps } from "./checks.js";
import { loadConfig } from "./config.js";
import { consoleLogger, tick } from "./runner.js";
import { buildDetail, buildView, renderDetail, renderNotFound, renderPage, samplesFor, sparklines } from "./status.js";
import { D1Store } from "./store.js";
import type { Config } from "./types.js";

interface Env {
  DB: D1Database;
  [secret: string]: unknown;
}

// La configuración se valida una vez por instancia; los secretos llegan como variables del worker.
let cached: Config | undefined;
function config(env: Env): Config {
  cached ??= loadConfig(configText, env);
  return cached;
}

const deps: ProbeDeps = {
  fetch: (input, init) => fetch(input, init),
  connect: (address) => connect(address),
  now: () => Date.now(),
};

export default {
  async scheduled(_controller: ScheduledController, env: Env): Promise<void> {
    const result = await tick(config(env), new D1Store(env.DB), deps, consoleLogger);
    consoleLogger.info("tick", { ran: result.ran.length, alerts: result.alerts.length });
    if (result.failedNotifications > 0) {
      // Que la invocación salga como fallida en el panel de Cloudflare, no solo en el log.
      throw new Error(`${result.failedNotifications} notification(s) failed`);
    }
  },

  async fetch(request: Request, env: Env): Promise<Response> {
    const url = new URL(request.url);
    if (request.method !== "GET") return new Response("method not allowed\n", { status: 405 });
    if (url.pathname === "/healthz") return new Response("ok\n", { headers: { "Content-Type": "text/plain; charset=utf-8" } });

    const html = (body: string, status = 200): Response =>
      new Response(body, { status, headers: { "Content-Type": "text/html; charset=utf-8", "Cache-Control": "no-store" } });
    const json = (body: unknown, status = 200): Response =>
      Response.json(body, { status, headers: { "Cache-Control": "no-store", "Access-Control-Allow-Origin": "*" } });

    const detailID = /^\/(api\/)?checks\/([^/]+)$/.exec(url.pathname);
    if (url.pathname !== "/" && url.pathname !== "/api/status" && detailID === null) {
      return new Response("not found\n", { status: 404 });
    }

    const cfg = config(env);
    const state = await new D1Store(env.DB).load();
    const now = Date.now();
    if (detailID !== null) {
      const id = decodeURIComponent(detailID[2] ?? "");
      const detail = buildDetail(cfg, state, id, now);
      if (detailID[1] !== undefined) return detail === null ? json({ error: "unknown check" }, 404) : json(detail);
      return detail === null
        ? html(renderNotFound(cfg.title, now, styleSheet), 404)
        : html(renderDetail(detail, samplesFor(cfg, state, id), styleSheet));
    }
    const view = buildView(cfg, state, now);
    if (url.pathname === "/api/status") return json(view);
    return html(renderPage(view, sparklines(cfg, state), styleSheet));
  },
} satisfies ExportedHandler<Env>;
