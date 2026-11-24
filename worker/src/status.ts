// Página de estado pública: mismo aspecto y mismo JSON que internal/status del binario.
// No enseña destinos ni mensajes de error, que pueden llevar direcciones internas.
import { DAY_WINDOW, dayKey, dayUptime, uptimeDays } from "./state.js";
import type { Config, Sample, State } from "./types.js";

export interface DayBar {
  date: string;
  uptime: number;
  no_data: boolean;
}

export interface CheckView {
  name: string;
  status: string;
  uptime_24h: number;
  uptime_30d: number;
  uptime_90d: number;
  avg_latency_ms: number;
  last_checked_at?: string;
  days: DayBar[];
}

export interface View {
  title: string;
  overall: string;
  generated_at: string;
  checks: CheckView[];
}

interface CheckExtras {
  sparkline: string;
}

function uptimeOfSamples(samples: Sample[]): number {
  if (samples.length === 0) return 1;
  return samples.filter((s) => s.ok).length / samples.length;
}

function avgLatency(samples: Sample[]): number {
  const ok = samples.filter((s) => s.ok);
  return ok.length === 0 ? 0 : ok.reduce((sum, s) => sum + s.latencyMs, 0) / ok.length;
}

// buildView arma la vista solo con las comprobaciones públicas, en el orden de la configuración.
export function buildView(cfg: Config, state: State, now: number): View {
  const checks: CheckView[] = [];
  let down = 0;
  let unknown = 0;
  for (const c of cfg.checks) {
    if (!c.public) continue;
    const st = state[c.name];
    const status = st?.status ?? "unknown";
    const samples = st?.samples ?? [];
    const byDate = new Map((st?.days ?? []).map((d) => [d.date, d]));
    const days: DayBar[] = [];
    for (let i = DAY_WINDOW - 1; i >= 0; i--) {
      const date = dayKey(now - i * 86_400_000);
      const d = byDate.get(date);
      const empty = d === undefined || d.total === 0;
      days.push({ date, uptime: empty ? 1 : dayUptime(d), no_data: empty });
    }
    const view: CheckView = {
      name: c.name,
      status,
      uptime_24h: uptimeOfSamples(samples),
      uptime_30d: st === undefined ? 1 : uptimeDays(st, 30, now),
      uptime_90d: st === undefined ? 1 : uptimeDays(st, DAY_WINDOW, now),
      avg_latency_ms: avgLatency(samples),
      days,
    };
    const last = samples.at(-1);
    if (last !== undefined) view.last_checked_at = new Date(last.at).toISOString().replace(/\.\d+Z$/, "Z");
    if (status === "down") down++;
    if (status === "unknown") unknown++;
    checks.push(view);
  }
  let overall: string;
  if (checks.length === 0) overall = "No public checks";
  else if (down === checks.length) overall = "Major outage";
  else if (down > 0) overall = "Partial outage";
  else if (unknown > 0) overall = "Collecting data";
  else overall = "All systems operational";
  return { title: cfg.title, overall, generated_at: new Date(now).toISOString(), checks };
}

// formatPercent no redondea a 100 % lo que no lo es: 99,996 % se ve como 99.99 %.
export function formatPercent(v: number): string {
  if (v >= 1) return "100%";
  return `${(Math.trunc(v * 10000) / 100).toFixed(2)}%`;
}

export function escapeHTML(s: string): string {
  return s.replace(/[&<>"']/g, (ch) => `&#${ch.charCodeAt(0)};`);
}

function level(b: DayBar): string {
  if (b.no_data) return "none";
  if (b.uptime >= 0.999) return "ok";
  if (b.uptime >= 0.95) return "warn";
  return "bad";
}

// sparkline dibuja la latencia de 24 h como puntos SVG ya escalados, con 120 puntos como mucho.
export function sparkline(samples: Sample[], width = 160, height = 32): string {
  let pts = samples.filter((s) => s.ok);
  if (pts.length < 2) return "";
  const maxPoints = 120;
  if (pts.length > maxPoints) {
    const step = pts.length / maxPoints;
    const reduced: Sample[] = [];
    for (let i = 0; i < maxPoints; i++) reduced.push(pts[Math.floor(i * step)] as Sample);
    reduced.push(pts[pts.length - 1] as Sample);
    pts = reduced;
  }
  const hi = Math.max(...pts.map((p) => p.latencyMs)) || 1;
  return pts
    .map((p, i) => {
      const x = (i / (pts.length - 1)) * width;
      const y = height - (p.latencyMs / hi) * (height - 2) - 1;
      return `${x.toFixed(1)},${y.toFixed(1)}`;
    })
    .join(" ");
}

// El CSS es copia del de internal/status/page.html.tmpl: las dos páginas tienen que verse igual.
const CSS = `
  :root {
    --bg: #f7f7f5; --card: #ffffff; --text: #17181a; --muted: #6b6f76; --line: #e6e6e3;
    --ok: #1f9d55; --warn: #d69e2e; --bad: #d64545; --none: #dcdcd8; --spark: #4c6ef5;
  }
  @media (prefers-color-scheme: dark) {
    :root {
      --bg: #0f1012; --card: #17181b; --text: #ececec; --muted: #8b8f96; --line: #26282c;
      --none: #2c2e33; --spark: #7c93ff;
    }
  }
  * { box-sizing: border-box; }
  body {
    margin: 0; background: var(--bg); color: var(--text);
    font: 15px/1.5 ui-sans-serif, system-ui, -apple-system, "Segoe UI", Roboto, sans-serif;
  }
  main { max-width: 760px; margin: 0 auto; padding: 48px 20px 64px; }
  h1 { font-size: 28px; letter-spacing: -0.02em; margin: 0 0 24px; }
  .banner {
    display: flex; align-items: center; gap: 12px; padding: 18px 20px; border-radius: 14px;
    background: var(--card); border: 1px solid var(--line); font-weight: 600; margin-bottom: 28px;
  }
  .dot { width: 10px; height: 10px; border-radius: 50%; flex: none; background: var(--none); }
  .dot.up { background: var(--ok); box-shadow: 0 0 0 4px color-mix(in srgb, var(--ok) 18%, transparent); }
  .dot.down { background: var(--bad); box-shadow: 0 0 0 4px color-mix(in srgb, var(--bad) 18%, transparent); }
  .checks { background: var(--card); border: 1px solid var(--line); border-radius: 14px; overflow: hidden; }
  .check { padding: 20px; border-top: 1px solid var(--line); }
  .check:first-child { border-top: 0; }
  .row { display: flex; align-items: center; gap: 10px; justify-content: space-between; flex-wrap: wrap; }
  .name { display: flex; align-items: center; gap: 10px; font-weight: 600; }
  .meta { color: var(--muted); font-size: 13px; font-variant-numeric: tabular-nums; }
  .bars { display: flex; gap: 2px; margin: 14px 0 6px; height: 30px; }
  .bars span { flex: 1; border-radius: 2px; background: var(--none); }
  .bars .ok { background: var(--ok); }
  .bars .warn { background: var(--warn); }
  .bars .bad { background: var(--bad); }
  .scale { display: flex; justify-content: space-between; color: var(--muted); font-size: 12px; }
  .spark { margin-top: 12px; display: flex; align-items: center; gap: 12px; }
  .spark svg { width: 160px; height: 32px; }
  .spark polyline { fill: none; stroke: var(--spark); stroke-width: 1.5; stroke-linejoin: round; }
  footer { margin-top: 28px; color: var(--muted); font-size: 12px; text-align: center; }
  footer a { color: inherit; }
`;
const ICON = "data:image/svg+xml,%3Csvg xmlns=%22http://www.w3.org/2000/svg%22 viewBox=%220 0 16 16%22%3E%3Ccircle cx=%228%22 cy=%228%22 r=%226%22 fill=%22%231f9d55%22/%3E%3C/svg%3E";

export function renderPage(view: View, extras: Map<string, CheckExtras>): string {
  const bannerDot =
    view.overall === "All systems operational" ? "up" : view.overall === "Collecting data" ? "" : "down";
  const checks = view.checks
    .map((c) => {
      const bars = c.days
        .map((d) => `<span class="${level(d)}" title="${d.date}: ${d.no_data ? "no data" : formatPercent(d.uptime)}"></span>`)
        .join("");
      const spark = extras.get(c.name)?.sparkline ?? "";
      const svg = spark === ""
        ? ""
        : `<svg viewBox="0 0 160 32" preserveAspectRatio="none" aria-hidden="true"><polyline points="${spark}"/></svg>`;
      const avg = c.avg_latency_ms > 0 ? ` · avg ${Math.round(c.avg_latency_ms)} ms` : "";
      return `<article class="check">
      <div class="row">
        <div class="name"><span class="dot ${escapeHTML(c.status)}"></span>${escapeHTML(c.name)}</div>
        <div class="meta">${formatPercent(c.uptime_90d)} uptime · 90 days</div>
      </div>
      <div class="bars" role="img" aria-label="Daily uptime for the last 90 days">${bars}</div>
      <div class="scale"><span>90 days ago</span><span>Today</span></div>
      <div class="spark">${svg}<span class="meta">24h ${formatPercent(c.uptime_24h)} · 30d ${formatPercent(c.uptime_30d)}${avg}</span></div>
    </article>`;
    })
    .join("\n");
  const updated = view.generated_at.slice(0, 16).replace("T", " ") + " UTC";
  return `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta http-equiv="refresh" content="60">
<title>${escapeHTML(view.title)}</title>
<link rel="icon" href="${ICON}">
<style>${CSS}</style>
</head>
<body>
<main>
  <h1>${escapeHTML(view.title)}</h1>
  <div class="banner"><span class="dot ${bannerDot}"></span>${escapeHTML(view.overall)}</div>
  ${view.checks.length > 0 ? `<section class="checks">${checks}</section>` : ""}
  <footer>Updated ${updated} · Powered by <a href="https://github.com/juanmmm21/infraping">InfraPing</a></footer>
</main>
</body>
</html>`;
}

export function sparklines(cfg: Config, state: State): Map<string, CheckExtras> {
  const out = new Map<string, CheckExtras>();
  for (const c of cfg.checks) {
    if (c.public) out.set(c.name, { sparkline: sparkline(state[c.name]?.samples ?? []) });
  }
  return out;
}
