import { describe, expect, it } from "vitest";
import { MAX_INCIDENTS, closeIncident, emptyState, isDue, observe, openIncident, record, retain, uptimeDays } from "../src/state.js";
import type { CheckResult } from "../src/types.js";
import { check } from "./helpers.js";

const T0 = Date.UTC(2026, 10, 24, 12, 0, 0);
const result = (ok: boolean, at: number): CheckResult => ({ ok, message: ok ? "ok" : "boom", latencyMs: 10, checkedAt: at });

describe("observe", () => {
  it("needs failure_threshold failures and dates the outage at the first one", () => {
    const st = emptyState();
    const c = check({ failureThreshold: 2, recoveryThreshold: 2 });
    expect(observe(st, c, result(true, T0))).toBeNull();
    expect(st.status).toBe("up");
    expect(observe(st, c, result(false, T0 + 60_000))).toBeNull();
    expect(observe(st, c, result(false, T0 + 120_000))).toEqual({ to: "down", downtimeMs: 0 });
    expect(st.downSince).toBe(T0 + 60_000);
    expect(observe(st, c, result(false, T0 + 180_000))).toBeNull();
    expect(observe(st, c, result(true, T0 + 240_000))).toBeNull();
    expect(observe(st, c, result(true, T0 + 300_000))).toEqual({ to: "up", downtimeMs: 240_000 });
    expect(st.status).toBe("up");
  });

  it("does not announce the first up", () => {
    expect(observe(emptyState(), check(), result(true, T0))).toBeNull();
  });

  it("can go down straight from unknown", () => {
    const st = emptyState();
    expect(observe(st, check({ failureThreshold: 1 }), result(false, T0))).toEqual({ to: "down", downtimeMs: 0 });
  });
});

describe("history", () => {
  it("keeps 24h of samples and per-day aggregates", () => {
    const st = emptyState();
    record(st, { at: T0, ok: true, latencyMs: 1 });
    record(st, { at: T0 + 3_600_000, ok: false, latencyMs: 1 });
    record(st, { at: T0 + 25 * 3_600_000, ok: true, latencyMs: 1 });
    expect(st.samples.map((s) => s.at)).toEqual([T0 + 3_600_000, T0 + 25 * 3_600_000]);
    expect(st.days).toEqual([{ date: "2026-11-24", total: 2, ok: 1 }, { date: "2026-11-25", total: 1, ok: 1 }]);
    expect(uptimeDays(st, 1, T0 + 25 * 3_600_000)).toBe(1);
    expect(uptimeDays(st, 2, T0 + 25 * 3_600_000)).toBeCloseTo(2 / 3);
  });

  it("caps days at 90", () => {
    const st = emptyState();
    for (let i = 0; i < 100; i++) record(st, { at: T0 + i * 86_400_000, ok: true, latencyMs: 1 });
    expect(st.days).toHaveLength(90);
    expect(st.days[0]?.date).toBe("2026-12-04");
  });

  it("retains only configured checks", () => {
    expect(Object.keys(retain({ a: emptyState(), b: emptyState() }, ["a", "c"]))).toEqual(["a"]);
  });
});

describe("isDue", () => {
  it("runs new checks and tolerates cron jitter", () => {
    const c = check({ intervalMs: 300_000 });
    expect(isDue(c, undefined, T0)).toBe(true);
    const st = { ...emptyState(), lastRun: T0 };
    expect(isDue(c, st, T0 + 240_000)).toBe(false);
    expect(isDue(c, st, T0 + 298_000)).toBe(true);
  });
});

describe("incidents", () => {
  it("opens once, closes and caps at 50", () => {
    const st = emptyState();
    closeIncident(st, T0);
    openIncident(st, T0);
    openIncident(st, T0 + 60_000);
    expect(st.incidents).toEqual([{ start: T0, end: 0 }]);
    closeIncident(st, T0 + 600_000);
    expect(st.incidents).toEqual([{ start: T0, end: T0 + 600_000 }]);
    for (let i = 1; i <= 60; i++) {
      openIncident(st, T0 + i * 3_600_000);
      closeIncident(st, T0 + i * 3_600_000 + 1);
    }
    expect(st.incidents).toHaveLength(MAX_INCIDENTS);
  });
});
