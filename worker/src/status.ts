// Página de estado pública: mismo HTML, mismo CSS (se importa el del binario) y mismo JSON que
// internal/status. No enseña destinos ni mensajes de error, que pueden llevar direcciones internas.
import { DAY_WINDOW, dayKey, dayUptime, isOngoing, uptimeDays } from "./state.js";
import type { CheckConfig, CheckState, Config, Incident, Sample, State } from "./types.js";

const RECENT_INCIDENTS = 5;
const HOUR = 3_600_000;
const DAY = 24 * HOUR;

export interface DayBar {
  date: string;
  uptime: number;
  no_data: boolean;
}

export interface CheckView {
  id: string;
  name: string;
  kind?: string;
  status: string;
  uptime_24h: number;
  uptime_30d: number;
  uptime_90d: number;
  avg_latency_ms: number;
  last_checked_at?: string;
  days: DayBar[];
}

export interface IncidentView {
  check: string;
  check_id: string;
  start: string;
  end?: string;
  ongoing: boolean;
  duration_seconds: number;
}

export interface Summary {
  total: number;
  up: number;
  down: number;
  unknown: number;
}

export interface View {
  title: string;
  overall: string;
  generated_at: string;
  summary: Summary;
  checks: CheckView[];
  recent_incidents: IncidentView[];
}

export interface LatencyPoint {
  at: string;
  ok: boolean;
  latency_ms: number;
}

export interface Detail {
  title: string;
  generated_at: string;
  check: CheckView;
  interval_seconds?: number;
  uptime_7d: number;
  checks_24h: number;
  failed_24h: number;
  p50_latency_ms: number;
  p95_latency_ms: number;
  max_latency_ms: number;
  latency_24h: LatencyPoint[];
  incidents: IncidentView[];
}

interface PageCheck {
  id: string;
  config: CheckConfig;
}

// slugify convierte un nombre en un trozo de URL; igual que en el binario.
export function slugify(name: string): string {
  let out = "";
  let dash = false;
  for (const ch of name.toLowerCase()) {
    if (/^[a-z0-9]$/.test(ch)) {
      out += ch;
      dash = false;
    } else if (!dash && out.length > 0) {
      out += "-";
      dash = true;
    }
  }
  out = out.replace(/-+$/, "");
  return out === "" ? "check" : out;
}

// publicChecks da a cada comprobación pública un id único: dos nombres pueden dar el mismo slug.
export function publicChecks(cfg: Config): PageCheck[] {
  const used = new Set<string>();
  const out: PageCheck[] = [];
  for (const c of cfg.checks) {
    if (!c.public) continue;
    const base = slugify(c.name);
    let id = base;
    for (let n = 2; used.has(id); n++) id = `${base}-${n}`;
    used.add(id);
    out.push({ id, config: c });
  }
  return out;
}

const iso = (ms: number): string => new Date(ms).toISOString().replace(/\.\d+Z$/, "Z");

function uptimeOfSamples(samples: Sample[]): number {
  if (samples.length === 0) return 1;
  return samples.filter((s) => s.ok).length / samples.length;
}

function avgLatency(samples: Sample[]): number {
  const ok = samples.filter((s) => s.ok);
  return ok.length === 0 ? 0 : ok.reduce((sum, s) => sum + s.latencyMs, 0) / ok.length;
}

function stateOf(state: State, name: string): CheckState | undefined {
  return state[name];
}

function checkView(p: PageCheck, st: CheckState | undefined, now: number): CheckView {
  const samples = st?.samples ?? [];
  const byDate = new Map((st?.days ?? []).map((d) => [d.date, d]));
  const days: DayBar[] = [];
  for (let i = DAY_WINDOW - 1; i >= 0; i--) {
    const date = dayKey(now - i * DAY);
    const d = byDate.get(date);
    const empty = d === undefined || d.total === 0;
    days.push({ date, uptime: empty ? 1 : dayUptime(d), no_data: empty });
  }
  const view: CheckView = {
    id: p.id,
    name: p.config.name,
    kind: p.config.type.toUpperCase(),
    status: st?.status ?? "unknown",
    uptime_24h: uptimeOfSamples(samples),
    uptime_30d: st === undefined ? 1 : uptimeDays(st, 30, now),
    uptime_90d: st === undefined ? 1 : uptimeDays(st, DAY_WINDOW, now),
    avg_latency_ms: avgLatency(samples),
    days,
  };
  const last = samples.at(-1);
  if (last !== undefined) view.last_checked_at = iso(last.at);
  return view;
}

function incidentViews(p: PageCheck, incidents: Incident[], now: number): IncidentView[] {
  return incidents
    .map((i) => {
      const ongoing = isOngoing(i);
      const v: IncidentView = {
        check: p.config.name,
        check_id: p.id,
        start: iso(i.start),
        ongoing,
        duration_seconds: Math.floor(((ongoing ? now : i.end) - i.start) / 1000),
      };
      if (!ongoing) v.end = iso(i.end);
      return v;
    })
    .sort((a, b) => b.start.localeCompare(a.start));
}

export function overall(s: Summary): { text: string; level: string } {
  if (s.total === 0) return { text: "No public checks", level: "none" };
  if (s.down === s.total) return { text: "Major outage", level: "bad" };
  if (s.down > 0) return { text: "Partial outage", level: "warn" };
  if (s.unknown > 0) return { text: "Collecting data", level: "none" };
  return { text: "All systems operational", level: "ok" };
}

export function buildView(cfg: Config, state: State, now: number): View {
  const summary: Summary = { total: 0, up: 0, down: 0, unknown: 0 };
  const checks: CheckView[] = [];
  const incidents: IncidentView[] = [];
  for (const p of publicChecks(cfg)) {
    const st = stateOf(state, p.config.name);
    const cv = checkView(p, st, now);
    summary.total++;
    if (cv.status === "up") summary.up++;
    else if (cv.status === "down") summary.down++;
    else summary.unknown++;
    checks.push(cv);
    incidents.push(...incidentViews(p, st?.incidents ?? [], now));
  }
  incidents.sort((a, b) => b.start.localeCompare(a.start) || a.check_id.localeCompare(b.check_id));
  return {
    title: cfg.title,
    overall: overall(summary).text,
    generated_at: new Date(now).toISOString(),
    summary,
    checks,
    recent_incidents: incidents.slice(0, RECENT_INCIDENTS),
  };
}

// percentile usa el rango más cercano sobre valores ya ordenados.
export function percentile(sorted: number[], p: number): number {
  if (sorted.length === 0) return 0;
  const rank = Math.ceil((p / 100) * sorted.length) - 1;
  return sorted[Math.max(0, Math.min(rank, sorted.length - 1))] ?? 0;
}

export function buildDetail(cfg: Config, state: State, id: string, now: number): Detail | null {
  const p = publicChecks(cfg).find((x) => x.id === id);
  if (p === undefined) return null;
  const st = stateOf(state, p.config.name);
  const samples = st?.samples ?? [];
  const ok = samples.filter((s) => s.ok).map((s) => s.latencyMs).sort((a, b) => a - b);
  return {
    title: cfg.title,
    generated_at: new Date(now).toISOString(),
    check: checkView(p, st, now),
    interval_seconds: Math.round(p.config.intervalMs / 1000),
    uptime_7d: st === undefined ? 1 : uptimeDays(st, 7, now),
    checks_24h: samples.length,
    failed_24h: samples.filter((s) => !s.ok).length,
    p50_latency_ms: percentile(ok, 50),
    p95_latency_ms: percentile(ok, 95),
    max_latency_ms: ok.at(-1) ?? 0,
    latency_24h: samples.map((s) => ({ at: iso(s.at), ok: s.ok, latency_ms: s.latencyMs })),
    incidents: incidentViews(p, st?.incidents ?? [], now),
  };
}

// ---------- Presentación ----------

export function formatPercent(v: number): string {
  if (v >= 1) return "100%";
  return `${(Math.trunc(v * 10000) / 100).toFixed(2)}%`;
}

export function humanDuration(ms: number): string {
  const s = Math.round(ms / 1000);
  if (s <= 0) return "0s";
  if (s < 60) return `${s}s`;
  if (s < 3600) return s % 60 === 0 ? `${s / 60}m` : `${Math.floor(s / 60)}m ${s % 60}s`;
  if (s < 48 * 3600) {
    const m = Math.floor(s / 60) % 60;
    return m === 0 ? `${Math.floor(s / 3600)}h` : `${Math.floor(s / 3600)}h ${m}m`;
  }
  return `${Math.floor(s / 86400)} days`;
}

export function escapeHTML(s: string): string {
  return s.replace(/[&<>"']/g, (ch) => `&#${ch.charCodeAt(0)};`);
}

const stamp = (isoString: string): string => `${isoString.slice(0, 16).replace("T", " ")} UTC`;
const msText = (v: number): string => `${Math.round(v)} ms`;

function level(b: DayBar): string {
  if (b.no_data) return "none";
  if (b.uptime >= 0.999) return "ok";
  if (b.uptime >= 0.95) return "warn";
  return "bad";
}

function grade(v: number): string {
  if (v >= 0.999) return "good";
  if (v >= 0.98) return "meh";
  return "poor";
}

function statusLabel(status: string): string {
  if (status === "up") return "Operational";
  if (status === "down") return "Down";
  return "No data yet";
}

// sparkline dibuja la latencia de 24 h como puntos SVG ya escalados, con 120 puntos como mucho.
export function sparkline(samples: Sample[], width = 120, height = 28): string {
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
    .map((p, i) => `${((i / (pts.length - 1)) * width).toFixed(1)},${(height - (p.latencyMs / hi) * (height - 2) - 1).toFixed(1)}`)
    .join(" ");
}

// Geometría del gráfico de latencia: la misma que internal/status/chart.go.
const CW = 720;
const CH = 220;
const PL = 48;
const PR = CW - 12;
const PT = 12;
const PB = CH - 28;
const Y_TICKS = 4;
const MIN_AXIS_MS = 100;

export function niceCeil(v: number): number {
  if (v <= 0) return 1;
  const exp = 10 ** Math.floor(Math.log10(v));
  for (const m of [1, 2, 5, 10]) if (m * exp >= v) return m * exp;
  return 10 * exp;
}

export function formatAxisMS(v: number): string {
  if (v >= 1000) return `${(v / 1000).toFixed(1).replace(/\.0$/, "")} s`;
  return `${Math.round(v)} ms`;
}

export interface Chart {
  line: string;
  area: string;
  failures: Array<{ x: number; w: number }>;
  xTicks: Array<{ pos: number; label: string }>;
  yTicks: Array<{ pos: number; label: string }>;
  empty: boolean;
}

// latencyChart proyecta las muestras; cada fallo corta la línea y suma a una banda roja.
export function latencyChart(samples: Sample[], now: number): Chart {
  const start = now - DAY;
  const x = (t: number): number => PL + Math.max(0, Math.min(1, (t - start) / DAY)) * (PR - PL);
  const ok = samples.filter((s) => s.ok);
  // Con poca o ninguna latencia el eje no baja de 100 ms: si no, saldrían marcas repetidas («1 ms»).
  const top = niceCeil(Math.max(MIN_AXIS_MS, Math.max(0, ...ok.map((s) => s.latencyMs)) * 1.1));
  const y = (ms: number): number => PB - (ms / top) * (PB - PT);
  const chart: Chart = { line: "", area: "", failures: [], xTicks: [], yTicks: [], empty: ok.length < 2 };
  for (let i = 0; i <= Y_TICKS; i++) {
    const v = (top * i) / Y_TICKS;
    chart.yTicks.push({ pos: y(v), label: formatAxisMS(v) });
  }
  // Una marca cada 6 horas en punto (UTC), sin pisar a «now».
  for (let t = Math.floor(start / (6 * HOUR)) * 6 * HOUR + 6 * HOUR; t < now - 3 * HOUR; t += 6 * HOUR) {
    chart.xTicks.push({ pos: x(t), label: new Date(t).toISOString().slice(11, 16) });
  }
  chart.xTicks.push({ pos: PR, label: "now" });

  let half = 1;
  const first = samples[0];
  const last = samples.at(-1);
  if (samples.length > 1 && first !== undefined && last !== undefined) {
    half = Math.max(1, ((last.at - first.at) / (samples.length - 1) / DAY) * (PR - PL) / 2);
  }
  const line: string[] = [];
  const area: string[] = [];
  let segStart = -1;
  let lastX = 0;
  const closeSeg = (): void => {
    if (segStart >= 0) area.push(`L${lastX.toFixed(1)},${PB.toFixed(1)} L${segStart.toFixed(1)},${PB.toFixed(1)} Z`);
    segStart = -1;
  };
  for (const s of samples) {
    if (s.at < start) continue;
    const px = x(s.at);
    if (!s.ok) {
      // Pegada a un borde, la banda se mete hacia dentro en vez de recortarse.
      const left = Math.max(PL, Math.min(px - half, PR - 2 * half));
      const right = left + 2 * half;
      const prev = chart.failures.at(-1);
      if (prev !== undefined && prev.x + prev.w >= left - 0.5) prev.w = right - prev.x;
      else chart.failures.push({ x: left, w: right - left });
      closeSeg();
      continue;
    }
    const py = y(s.latencyMs);
    if (segStart < 0) {
      line.push(`M${px.toFixed(1)},${py.toFixed(1)}`);
      area.push(`M${px.toFixed(1)},${PB.toFixed(1)} L${px.toFixed(1)},${py.toFixed(1)}`);
      segStart = px;
    } else {
      line.push(`L${px.toFixed(1)},${py.toFixed(1)}`);
      area.push(`L${px.toFixed(1)},${py.toFixed(1)}`);
    }
    lastX = px;
  }
  closeSeg();
  chart.line = line.join(" ");
  chart.area = area.join(" ");
  return chart;
}

const ICON =
  "data:image/svg+xml,%3Csvg xmlns=%22http://www.w3.org/2000/svg%22 viewBox=%220 0 16 16%22%3E%3Ccircle cx=%228%22 cy=%228%22 r=%226%22 fill=%22%2316a34a%22/%3E%3C/svg%3E";

const LOCAL_TIME_SCRIPT = `<script>
// Las horas llegan en UTC desde el servidor; aquí se pasan a la hora local de quien mira.
document.querySelectorAll("time[datetime]").forEach(function (el) {
  var d = new Date(el.getAttribute("datetime"));
  if (isNaN(d)) return;
  var opts = el.dataset.date === "day" ? { day: "numeric", month: "short", year: "numeric" }
    : { day: "numeric", month: "short", hour: "2-digit", minute: "2-digit" };
  el.textContent = d.toLocaleString(undefined, opts);
  el.title = d.toString();
});
</script>`;

function head(title: string, css: string): string {
  return `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta http-equiv="refresh" content="60">
<meta name="color-scheme" content="light dark">
<title>${escapeHTML(title)}</title>
<link rel="icon" href="${ICON}">
<style>${css}</style>
</head>
<body>
<main>
`;
}

function foot(generatedAt: string): string {
  return `
  <footer>Updated <time datetime="${iso(Date.parse(generatedAt))}">${stamp(generatedAt)}</time> · refreshes every minute · Powered by <a href="https://github.com/juanmmm21/infraping">InfraPing</a></footer>
</main>
${LOCAL_TIME_SCRIPT}
</body>
</html>
`;
}

function bars(days: DayBar[], uptime: number, big: boolean): string {
  const cells = days
    .map((d) => `<span class="${level(d)}" data-tip="${d.date}: ${d.no_data ? "no data" : `${formatPercent(d.uptime)} uptime`}"></span>`)
    .join("");
  return `<div class="bars${big ? " big" : ""}" aria-hidden="true">${cells}</div>
  <div class="scale"><span class="from"><span>90 days ago</span></span><b>${formatPercent(uptime)} over 90 days</b><span>Today</span></div>`;
}

const pill = (status: string): string => `<span class="pill ${escapeHTML(status)}">${statusLabel(status)}</span>`;

function incidentItem(i: IncidentView, link: string | null): string {
  const duration = humanDuration(i.duration_seconds * 1000);
  const who = link === null ? "" : `<a href="${link}">${escapeHTML(i.check)}</a><span class="meta">·</span>`;
  return `<li${i.ongoing ? ' class="ongoing"' : ""}>
    <div class="what"><span class="dot"></span>${who}${i.ongoing ? "Ongoing outage" : "Outage resolved"}</div>
    <div class="meta"><time datetime="${i.start}">${stamp(i.start)}</time> · ${i.ongoing ? `down for ${duration}` : `lasted ${duration}`}</div>
  </li>`;
}

const HERO_ICONS: Record<string, string> = {
  ok: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3" stroke-linecap="round" stroke-linejoin="round"><path d="M5 12.5l4.5 4.5L19 7.5"/></svg>',
  none: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3" stroke-linecap="round"><path d="M12 7v5l3 2"/></svg>',
  alert: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3" stroke-linecap="round"><path d="M12 7v6M12 17h.01"/></svg>',
};

export function renderPage(view: View, sparks: Map<string, string>, css: string): string {
  const lvl = overall(view.summary).level;
  const s = view.summary;
  const sub = s.down > 0 ? `${s.down} of ${s.total} services down` : s.unknown > 0 ? "Waiting for the first results" : "Every service answered its last check";
  const services = view.checks
    .map((c) => {
      const spark = sparks.get(c.id) ?? "";
      const avg = c.avg_latency_ms > 0 ? ` · avg ${msText(c.avg_latency_ms)}` : "";
      return `<a class="service" href="checks/${c.id}" aria-label="${escapeHTML(c.name)}: ${statusLabel(c.status)}, ${formatPercent(c.uptime_90d)} uptime over 90 days. Open details.">
      <div class="row">
        <div class="name"><span>${escapeHTML(c.name)}</span>${c.kind ? `<span class="kind">${c.kind}</span>` : ""}<span class="chev" aria-hidden="true">›</span></div>
        ${pill(c.status)}
      </div>
      ${bars(c.days, c.uptime_90d, false)}
      <div class="foot">
        <span class="meta">24h ${formatPercent(c.uptime_24h)} · 30d ${formatPercent(c.uptime_30d)}${avg}</span>
        ${spark === "" ? "" : `<svg class="spark" viewBox="0 0 120 28" preserveAspectRatio="none" aria-hidden="true"><polyline points="${spark}"/></svg>`}
      </div>
    </a>`;
    })
    .join("\n");
  const incidents =
    view.recent_incidents.length === 0
      ? '<p class="empty">No incidents recorded.</p>'
      : `<ul class="incidents">${view.recent_incidents.map((i) => incidentItem(i, `checks/${i.check_id}`)).join("")}</ul>`;
  return `${head(view.title, css)}  <header class="top">
    <h1>${escapeHTML(view.title)}</h1>
    <span class="updated">${s.up} of ${s.total} services operational</span>
  </header>

  <section class="hero ${lvl}" aria-live="polite">
    <div class="hero-icon" aria-hidden="true">${HERO_ICONS[lvl === "ok" || lvl === "none" ? lvl : "alert"]}</div>
    <div>
      <strong>${escapeHTML(view.overall)}</strong>
      <span>${sub}</span>
    </div>
  </section>
  ${view.checks.length > 0 ? `<h2>Services</h2>\n  <section class="card list">${services}</section>` : ""}

  <h2>Recent incidents</h2>
  <section class="card">${incidents}</section>
${foot(view.generated_at)}`;
}

function stat(k: string, v: number): string {
  return `<div class="card stat"><div class="k">${k}</div><div class="v ${grade(v)}">${formatPercent(v)}</div></div>`;
}

export function renderDetail(d: Detail, samples: Sample[], css: string): string {
  const c = d.check;
  const now = Date.parse(d.generated_at);
  const chart = latencyChart(samples, now);
  const f = (v: number): string => v.toFixed(1);
  const svg = `<svg class="chart" viewBox="0 0 ${CW} ${CH}" role="img" aria-label="Response time over the last 24 hours">
      ${chart.yTicks.map((t) => `<line class="grid" x1="${f(PL)}" x2="${f(PR)}" y1="${f(t.pos)}" y2="${f(t.pos)}"/><text class="axis" x="${f(PL - 8)}" y="${f(t.pos + 4)}" text-anchor="end">${t.label}</text>`).join("")}
      ${chart.xTicks.map((t) => `<text class="axis" x="${f(t.pos)}" y="${f(PB + 18)}" text-anchor="${t.label === "now" ? "end" : "middle"}">${t.label}</text>`).join("")}
      ${chart.failures.map((b) => `<rect class="fail" x="${f(b.x)}" width="${f(b.w)}" y="${f(PT)}" height="${f(PB - PT)}"/>`).join("")}
      ${chart.empty ? `<text class="nodata" x="${f((PL + PR) / 2)}" y="${f((PT + PB) / 2)}" text-anchor="middle">Not enough data yet</text>` : `<path class="area" d="${chart.area}"/><path class="line" d="${chart.line}"/>`}
    </svg>`;
  const opt = (v: number): string => (v > 0 ? msText(v) : "–");
  const interval = d.interval_seconds ? ` every ${humanDuration(d.interval_seconds * 1000)}` : "";
  const last = c.last_checked_at ? ` · last checked <time datetime="${c.last_checked_at}">${c.last_checked_at}</time>` : "";
  const incidents =
    d.incidents.length === 0
      ? '<p class="empty">No incidents recorded for this service.</p>'
      : `<ul class="incidents">${d.incidents.map((i) => incidentItem(i, null)).join("")}</ul>`;
  return `${head(`${c.name} · ${d.title}`, css)}  <a class="back" href="../">‹ ${escapeHTML(d.title)}</a>

  <div class="detail-head">
    <h1>${escapeHTML(c.name)}</h1>
    ${pill(c.status)}
  </div>
  <p class="sub">${c.kind ? `${c.kind} check` : "Check"}${interval}${last}</p>

  <h2>Uptime</h2>
  <section class="stats">${stat("Last 24 hours", c.uptime_24h)}${stat("Last 7 days", d.uptime_7d)}${stat("Last 30 days", c.uptime_30d)}${stat("Last 90 days", c.uptime_90d)}</section>

  <h2>Response time</h2>
  <section class="stats">
    <div class="card stat"><div class="k">Median</div><div class="v">${opt(d.p50_latency_ms)}</div></div>
    <div class="card stat"><div class="k">95th percentile</div><div class="v">${opt(d.p95_latency_ms)}</div></div>
    <div class="card stat"><div class="k">Slowest</div><div class="v">${opt(d.max_latency_ms)}</div></div>
    <div class="card stat"><div class="k">Failed checks · 24h</div><div class="v${d.failed_24h > 0 ? " poor" : ""}">${d.failed_24h} <span class="meta">of ${d.checks_24h}</span></div></div>
  </section>

  <section class="card panel" style="margin-top:12px">
    <div class="panel-head">
      <h3>Last 24 hours</h3>
      <div class="legend"><span><i></i>response time</span><span><i class="f"></i>failed check</span></div>
    </div>
    ${svg}
  </section>

  <h2>Last 90 days</h2>
  <section class="card panel">${bars(c.days, c.uptime_90d, true)}</section>

  <h2>Incidents</h2>
  <section class="card">${incidents}</section>
${foot(d.generated_at)}`;
}

export function renderNotFound(title: string, now: number, css: string): string {
  return `${head(title, css)}  <a class="back" href="../">‹ ${escapeHTML(title)}</a>
  <section class="card panel">
    <h1 style="margin:0 0 6px;font-size:22px">Service not found</h1>
    <p class="meta" style="margin:0">It may have been renamed or removed from this status page.</p>
  </section>
${foot(new Date(now).toISOString())}`;
}

export function sparklines(cfg: Config, state: State): Map<string, string> {
  const out = new Map<string, string>();
  for (const p of publicChecks(cfg)) out.set(p.id, sparkline(state[p.config.name]?.samples ?? []));
  return out;
}

export function samplesFor(cfg: Config, state: State, id: string): Sample[] {
  const p = publicChecks(cfg).find((x) => x.id === id);
  return p === undefined ? [] : (state[p.config.name]?.samples ?? []);
}
