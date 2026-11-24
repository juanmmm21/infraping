import type { ConnectFn, FetchFn, ProbeDeps } from "../src/checks.js";
import type { CheckConfig, Config, NotifierConfig } from "../src/types.js";

export function check(over: Partial<CheckConfig> = {}): CheckConfig {
  return {
    name: "web", type: "http", url: "https://example.test/", method: "GET", headers: {}, expectStatus: [],
    contains: "", address: "", host: "", expectIPs: [], maxLatencyMs: 0, intervalMs: 60_000, timeoutMs: 1_000,
    failureThreshold: 2, recoveryThreshold: 1, notify: [], public: true, ...over,
  };
}

export function notifier(over: Partial<NotifierConfig> = {}): NotifierConfig {
  return { name: "hook", type: "webhook", token: "", chatId: "", url: "https://hooks.test/x", server: "", topic: "", headers: {}, ...over };
}

export function config(over: Partial<Config> = {}): Config {
  return { title: "Status", notifiers: [notifier()], checks: [check()], ...over };
}

export interface Clock {
  now: () => number;
  advance: (ms: number) => void;
}

export function clock(start = Date.UTC(2026, 10, 24, 12, 0, 0)): Clock {
  let t = start;
  return { now: () => t, advance: (ms) => { t += ms; } };
}

export const refusingConnect: ConnectFn = () => ({
  opened: Promise.reject(new Error("connection refused")),
  close: () => Promise.resolve(),
});

export function deps(fetchFn: FetchFn, c: Clock = clock(), connectFn: ConnectFn = refusingConnect): ProbeDeps {
  return { fetch: fetchFn, connect: connectFn, now: c.now };
}

export const noSleep = { attempts: 3, backoffMs: 0, timeoutMs: 1_000, sleep: () => Promise.resolve() };
