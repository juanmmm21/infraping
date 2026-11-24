import { describe, expect, it } from "vitest";
import type { FetchFn } from "../src/checks.js";
import { body, buildRequest, formatDuration, send, targets, title } from "../src/notify.js";
import type { Message } from "../src/types.js";
import { noSleep, notifier } from "./helpers.js";

const down: Message = { kind: "down", check: "API", target: "https://api.test", reason: "status 502", at: Date.UTC(2026, 10, 24, 9, 5, 7), downtimeMs: 0 };
const up: Message = { ...down, kind: "recovered", reason: "status 200", downtimeMs: 754_000 };

describe("message text", () => {
  it("matches the binary", () => {
    expect(title(down)).toBe("🔴 API is DOWN");
    expect(title(up)).toBe("🟢 API is back UP");
    expect(body(down)).toBe("Target: https://api.test\nReason: status 502\nAt: 2026-11-24 09:05:07 UTC");
    expect(body(up)).toContain("Downtime: 12m 34s");
    expect(formatDuration(3 * 3_600_000 + 5 * 60_000)).toBe("3h 5m");
    expect(formatDuration(9_000)).toBe("9s");
  });
});

describe("requests", () => {
  it("builds each notifier's format", () => {
    const tg = buildRequest(notifier({ type: "telegram", token: "T", chatId: "42" }), down);
    expect(tg.url).toBe("https://api.telegram.org/botT/sendMessage");
    expect(JSON.parse(tg.body)).toMatchObject({ chat_id: "42", disable_web_page_preview: true });

    const dc = JSON.parse(buildRequest(notifier({ type: "discord" }), down).body);
    expect(dc.embeds[0]).toMatchObject({ title: "🔴 API is DOWN", color: 0xef4444 });

    expect(JSON.parse(buildRequest(notifier({ type: "slack" }), up).body).text).toMatch(/^\*🟢 API is back UP\*\n/);

    const nt = buildRequest(notifier({ type: "ntfy", server: "https://ntfy.test", topic: "a b", token: "k" }), down);
    expect(nt.url).toBe("https://ntfy.test/a%20b");
    expect(nt.headers).toMatchObject({ Priority: "high", Tags: "rotating_light", Authorization: "Bearer k" });

    const wh = buildRequest(notifier({ headers: { "X-Key": "1" } }), up);
    expect(JSON.parse(wh.body)).toMatchObject({ kind: "recovered", check: "API", downtime_seconds: 754, at: "2026-11-24T09:05:07.000Z" });
    expect(wh.headers).toEqual({ "X-Key": "1" });
  });

  it("picks the check's notifiers or all of them", () => {
    const all = [notifier({ name: "a" }), notifier({ name: "b" })];
    expect(targets(all, []).map((n) => n.name)).toEqual(["a", "b"]);
    expect(targets(all, ["b"]).map((n) => n.name)).toEqual(["b"]);
  });
});

describe("send", () => {
  it("retries until it succeeds", async () => {
    let calls = 0;
    const flaky: FetchFn = () => Promise.resolve(new Response("busy", { status: ++calls < 3 ? 503 : 200 }));
    await send(flaky, notifier(), down, noSleep);
    expect(calls).toBe(3);
  });

  it("gives up with an error that does not leak the URL", async () => {
    const broken: FetchFn = () => Promise.reject(new TypeError("fetch failed https://api.telegram.org/botSECRET"));
    const err = await send(broken, notifier({ type: "telegram", token: "SECRET", chatId: "1" }), down, noSleep).catch((e: Error) => e);
    expect(err).toBeInstanceOf(Error);
    expect((err as Error).message).toBe("notifier hook: 3 attempts failed: post failed: TypeError");
  });
});
