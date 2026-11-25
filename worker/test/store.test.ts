import { describe, expect, it } from "vitest";
import { emptyState } from "../src/state.js";
import { decodeState, encodeState } from "../src/store.js";

describe("state encoding", () => {
  it("round-trips at second and millisecond resolution", () => {
    const st = { ...emptyState(), status: "down" as const, downSince: 1_764_000_000_000, failStreak: 3 };
    st.samples = [
      { at: 1_764_000_000_000, ok: true, latencyMs: 41.6 },
      { at: 1_764_000_060_000, ok: false, latencyMs: 0 },
    ];
    st.days = [{ date: "2026-11-24", total: 2, ok: 1 }];
    const back = decodeState(encodeState({ web: st }));
    expect(back.web).toEqual({ ...st, samples: [{ at: 1_764_000_000_000, ok: true, latencyMs: 42 }, { at: 1_764_000_060_000, ok: false, latencyMs: 0 }] });
  });

  it("rejects corrupted columns", () => {
    const broken = JSON.stringify({ web: { ...emptyState(), samples: { t: [1, 2], o: "1", l: [1, 2] } } });
    expect(() => decodeState(broken)).toThrow("sample columns differ in length");
  });
});

describe("older saved state", () => {
  it("gets an empty incident list", () => {
    const old = JSON.stringify({ web: { status: "up", failStreak: 0, okStreak: 1, downSince: 0, lastRun: 1, samples: { t: [], o: "", l: [] }, days: [] } });
    expect(decodeState(old).web?.incidents).toEqual([]);
  });
});
