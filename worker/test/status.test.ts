import { describe, expect, it } from "vitest";
import { emptyState, record } from "../src/state.js";
import { buildView, escapeHTML, formatPercent, renderPage, sparkline, sparklines } from "../src/status.js";
import type { State } from "../src/types.js";
import { check, config } from "./helpers.js";

const NOW = Date.UTC(2026, 10, 24, 12, 0, 0);

function stateWith(status: "up" | "down", ok: boolean): State {
  const st = { ...emptyState(), status };
  record(st, { at: NOW - 120_000, ok: true, latencyMs: 40 });
  record(st, { at: NOW - 60_000, ok, latencyMs: 60 });
  return { web: st, db: { ...emptyState(), status: "down" } };
}

describe("buildView", () => {
  it("lists only public checks and never their targets or errors", () => {
    const cfg = config({
      title: "Acme <status>",
      checks: [check({ url: "http://10.0.0.5/secret" }), check({ name: "db", type: "tcp", address: "10.0.0.9:5432", public: false })],
    });
    const view = buildView(cfg, stateWith("up", false), NOW);
    expect(view.checks.map((c) => c.name)).toEqual(["web"]);
    expect(view.overall).toBe("All systems operational");
    expect(view.checks[0]).toMatchObject({ uptime_24h: 0.5, avg_latency_ms: 40, last_checked_at: "2026-11-24T11:59:00Z" });
    expect(view.checks[0]?.days).toHaveLength(90);
    expect(view.checks[0]?.days.at(-1)).toEqual({ date: "2026-11-24", uptime: 0.5, no_data: false });
    expect(view.checks[0]?.days[0]?.no_data).toBe(true);

    const page = renderPage(view, sparklines(cfg, stateWith("up", true)));
    const json = JSON.stringify(view);
    for (const leak of ["10.0.0.5", "10.0.0.9", "secret"]) {
      expect(page).not.toContain(leak);
      expect(json).not.toContain(leak);
    }
    expect(page).toContain("<title>Acme &#60;status&#62;</title>");
    expect(page).toContain("<polyline points=");
  });

  it("summarises the overall state", () => {
    const one = (status: "up" | "down" | "unknown") => buildView(config(), { web: { ...emptyState(), status } }, NOW).overall;
    expect(one("down")).toBe("Major outage");
    expect(one("unknown")).toBe("Collecting data");
    expect(buildView(config({ checks: [check(), check({ name: "b" })] }), { web: { ...emptyState(), status: "down" }, b: { ...emptyState(), status: "up" } }, NOW).overall).toBe("Partial outage");
    expect(buildView(config({ checks: [check({ public: false })] }), {}, NOW).overall).toBe("No public checks");
  });
});

describe("helpers", () => {
  it("never rounds up to 100%", () => {
    expect(formatPercent(1)).toBe("100%");
    expect(formatPercent(0.99996)).toBe("99.99%");
    expect(formatPercent(0.5)).toBe("50.00%");
  });

  it("escapes HTML", () => {
    expect(escapeHTML(`<a href="x">'&'</a>`)).toBe("&#60;a href=&#34;x&#34;&#62;&#39;&#38;&#39;&#60;/a&#62;");
  });

  it("draws at most 121 points and needs two", () => {
    expect(sparkline([{ at: 1, ok: true, latencyMs: 1 }])).toBe("");
    const many = Array.from({ length: 1000 }, (_, i) => ({ at: i, ok: true, latencyMs: i % 7 }));
    expect(sparkline(many).split(" ")).toHaveLength(121);
  });
});
