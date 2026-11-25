import { describe, expect, it } from "vitest";
import { closeIncident, emptyState, openIncident, record } from "../src/state.js";
import {
  buildDetail, buildView, escapeHTML, formatAxisMS, formatPercent, humanDuration, latencyChart, niceCeil,
  percentile, publicChecks, renderDetail, renderNotFound, renderPage, samplesFor, slugify, sparkline, sparklines,
} from "../src/status.js";
import type { State } from "../src/types.js";
import { check, config } from "./helpers.js";

const NOW = Date.UTC(2026, 10, 24, 12, 0, 0);
const CSS = "/* css */";
const MIN = 60_000;

function outageState(): State {
  const st = { ...emptyState(), status: "up" as const };
  for (let i = 0; i < 100; i++) {
    record(st, { at: NOW - (100 - i) * MIN, ok: i < 90 || i >= 95, latencyMs: i + 1 });
  }
  openIncident(st, NOW - 48 * 60 * MIN);
  closeIncident(st, NOW - 47 * 60 * MIN);
  openIncident(st, NOW - 10 * MIN);
  closeIncident(st, NOW - 5 * MIN);
  return { api: st, db: { ...emptyState(), status: "down" } };
}

describe("ids", () => {
  it("slugify matches the binary", () => {
    expect(slugify("Public API")).toBe("public-api");
    expect(slugify("Café ☕ 2")).toBe("caf-2");
    expect(slugify("???")).toBe("check");
  });

  it("dedupes ids and skips private checks", () => {
    const cfg = config({ checks: [check({ name: "Public API" }), check({ name: "public-api" }), check({ name: "x", public: false })] });
    expect(publicChecks(cfg).map((p) => p.id)).toEqual(["public-api", "public-api-2"]);
  });
});

describe("buildView", () => {
  it("summarises, never leaks targets or errors, and lists recent incidents", () => {
    const cfg = config({
      title: "Acme <status>",
      checks: [check({ name: "api", url: "http://10.0.0.5/secret" }), check({ name: "db", type: "tcp", address: "10.0.0.9:5432", public: false })],
    });
    const view = buildView(cfg, outageState(), NOW);
    expect(view.checks.map((c) => c.id)).toEqual(["api"]);
    expect(view.summary).toEqual({ total: 1, up: 1, down: 0, unknown: 0 });
    expect(view.overall).toBe("All systems operational");
    expect(view.recent_incidents.map((i) => i.duration_seconds)).toEqual([300, 3600]);
    expect(view.checks[0]?.days).toHaveLength(90);

    const page = renderPage(view, sparklines(cfg, outageState()), CSS);
    for (const leak of ["10.0.0.5", "10.0.0.9", "secret"]) {
      expect(page).not.toContain(leak);
      expect(JSON.stringify(view)).not.toContain(leak);
    }
    expect(page).toContain("<title>Acme &#60;status&#62;</title>");
    expect(page).toContain('href="checks/api"');
    expect(page).toContain("<style>/* css */</style>");
  });

  it("chooses the overall state", () => {
    const one = (status: "up" | "down" | "unknown") => buildView(config(), { web: { ...emptyState(), status } }, NOW).overall;
    expect(one("down")).toBe("Major outage");
    expect(one("unknown")).toBe("Collecting data");
    expect(buildView(config({ checks: [check({ public: false })] }), {}, NOW).overall).toBe("No public checks");
  });
});

describe("buildDetail", () => {
  it("computes the detail numbers", () => {
    const cfg = config({ checks: [check({ name: "api", intervalMs: MIN })] });
    const d = buildDetail(cfg, outageState(), "api", NOW);
    expect(d).not.toBeNull();
    if (d === null) return;
    expect(d.checks_24h).toBe(100);
    expect(d.failed_24h).toBe(5);
    expect([d.p50_latency_ms, d.p95_latency_ms, d.max_latency_ms]).toEqual([48, 96, 100]);
    expect(d.interval_seconds).toBe(60);
    expect(d.incidents.map((i) => i.ongoing)).toEqual([false, false]);

    const html = renderDetail(d, samplesFor(cfg, outageState(), "api"), CSS);
    expect(html).toContain("<h1>api</h1>");
    expect(html).toContain("HTTP check every 1m");
    expect(html).toContain('class="fail"');
    expect(buildDetail(cfg, outageState(), "db", NOW)).toBeNull();
    expect(renderNotFound("Acme", NOW, CSS)).toContain("Service not found");
  });
});

describe("chart", () => {
  it("draws axes, cuts the line on failures and merges failure bands", () => {
    expect([niceCeil(0), niceCeil(87), niceCeil(130), niceCeil(420)]).toEqual([1, 100, 200, 500]);
    expect([formatAxisMS(250), formatAxisMS(1000), formatAxisMS(1500)]).toEqual(["250 ms", "1 s", "1.5 s"]);
    const empty = latencyChart([], NOW);
    expect(empty.empty).toBe(true);
    expect(empty.xTicks.at(-1)?.label).toBe("now");

    const samples = Array.from({ length: 60 }, (_, i) => ({ at: NOW - (60 - i) * MIN, ok: i < 20 || (i >= 25 && i !== 40), latencyMs: 100 }));
    const c = latencyChart(samples, NOW);
    expect(c.failures).toHaveLength(2);
    expect(c.line.match(/M/g)).toHaveLength(3);
    expect(c.area.match(/Z/g)).toHaveLength(3);
  });
});

describe("helpers", () => {
  it("formats like the binary", () => {
    expect([formatPercent(1), formatPercent(0.99996), formatPercent(0.5)]).toEqual(["100%", "99.99%", "50.00%"]);
    expect([0, 45_000, 120_000, 90_000, 3 * 3_600_000 + 12 * MIN, 72 * 3_600_000].map(humanDuration)).toEqual([
      "0s", "45s", "2m", "1m 30s", "3h 12m", "3 days",
    ]);
    expect(percentile([], 50)).toBe(0);
    expect(escapeHTML(`<a href="x">'&'</a>`)).toBe("&#60;a href=&#34;x&#34;&#62;&#39;&#38;&#39;&#60;/a&#62;");
  });

  it("draws at most 121 sparkline points and needs two", () => {
    expect(sparkline([{ at: 1, ok: true, latencyMs: 1 }])).toBe("");
    const many = Array.from({ length: 1000 }, (_, i) => ({ at: i, ok: true, latencyMs: i % 7 }));
    expect(sparkline(many).split(" ")).toHaveLength(121);
  });
});

describe("chart with little data", () => {
  it("keeps a readable axis and shows a single failure", () => {
    const c = latencyChart([{ at: NOW - MIN, ok: false, latencyMs: 0 }], NOW);
    expect(c.yTicks.map((t) => t.label)).toEqual(["0 ms", "25 ms", "50 ms", "75 ms", "100 ms"]);
    expect(c.failures).toHaveLength(1);
    expect(c.failures[0]?.w).toBeGreaterThanOrEqual(2);
  });
});
