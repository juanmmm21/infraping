import { describe, expect, it } from "vitest";
import type { FetchFn } from "../src/checks.js";
import { type Logger, tick } from "../src/runner.js";
import { MemoryStore } from "../src/store.js";
import { check, clock, config, deps, noSleep, notifier } from "./helpers.js";

const quiet: Logger = { info: () => undefined, error: () => undefined };

// fakeNet simula el servicio vigilado (que se puede tirar y levantar) y el webhook de avisos.
function fakeNet() {
  const net = { serviceUp: true, alerts: [] as Array<Record<string, unknown>>, hookFails: false };
  const fetchFn: FetchFn = async (url, init) => {
    if (url.startsWith("https://hooks.test")) {
      if (net.hookFails) return new Response("nope", { status: 500 });
      net.alerts.push(JSON.parse(String(init?.body)) as Record<string, unknown>);
      return new Response("ok");
    }
    return new Response("", { status: net.serviceUp ? 200 : 503 });
  };
  return { net, fetchFn };
}

describe("tick", () => {
  it("alerts once on outage and once on recovery, across invocations", async () => {
    const { net, fetchFn } = fakeNet();
    const c = clock();
    const store = new MemoryStore();
    const cfg = config();
    const run = () => tick(cfg, store, deps(fetchFn, c), quiet, noSleep);

    await run();
    net.serviceUp = false;
    for (let i = 0; i < 4; i++) {
      c.advance(60_000);
      await run();
    }
    expect(net.alerts.map((a) => a.kind)).toEqual(["down"]);
    expect(store.saved.web?.status).toBe("down");

    net.serviceUp = true;
    c.advance(60_000);
    const res = await run();
    expect(res.alerts).toHaveLength(1);
    expect(net.alerts.map((a) => a.kind)).toEqual(["down", "recovered"]);
    expect(net.alerts[1]?.downtime_seconds).toBe(240);
    // La caída queda apuntada con su comienzo en el primer fallo y su final en la vuelta.
    expect(store.saved.web?.incidents).toHaveLength(1);
    const inc = store.saved.web?.incidents[0];
    expect(inc !== undefined && inc.end - inc.start).toBe(240_000);
  });

  it("respects each check's interval", async () => {
    const { fetchFn } = fakeNet();
    const c = clock();
    const store = new MemoryStore();
    const cfg = config({ checks: [check({ name: "fast" }), check({ name: "slow", intervalMs: 300_000 })] });
    const ran: string[][] = [];
    for (let i = 0; i < 6; i++) {
      ran.push((await tick(cfg, store, deps(fetchFn, c), quiet, noSleep)).ran);
      c.advance(60_000);
    }
    expect(ran.filter((r) => r.includes("fast"))).toHaveLength(6);
    expect(ran.filter((r) => r.includes("slow"))).toHaveLength(2);
  });

  it("saves state before notifying and reports failed deliveries", async () => {
    const { net, fetchFn } = fakeNet();
    net.serviceUp = false;
    net.hookFails = true;
    const store = new MemoryStore();
    const cfg = config({ checks: [check({ failureThreshold: 1 })] });
    const res = await tick(cfg, store, deps(fetchFn), quiet, noSleep);
    expect(res.failedNotifications).toBe(1);
    expect(store.saved.web?.status).toBe("down");
  });

  it("only alerts the check's notifiers and drops removed checks", async () => {
    const { net, fetchFn } = fakeNet();
    net.serviceUp = false;
    const store = new MemoryStore();
    store.saved = { gone: { status: "down", failStreak: 3, okStreak: 0, downSince: 1, lastRun: 1, samples: [], days: [], incidents: [] } };
    const cfg = config({
      notifiers: [notifier({ name: "a" }), notifier({ name: "b", url: "https://elsewhere.test/" })],
      checks: [check({ failureThreshold: 1, notify: ["a"] })],
    });
    await tick(cfg, store, deps(fetchFn), quiet, noSleep);
    expect(net.alerts).toHaveLength(1);
    expect(Object.keys(store.saved)).toEqual(["web"]);
  });
});
