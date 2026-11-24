import { describe, expect, it } from "vitest";
import { DOH_URL, type FetchFn, runCheck } from "../src/checks.js";
import { check, clock, deps } from "./helpers.js";

const respond = (body: string, status = 200): FetchFn => () => Promise.resolve(new Response(body, { status }));

describe("http", () => {
  it("passes on 2xx and checks the body", async () => {
    expect(await runCheck(check({ contains: "hello" }), deps(respond("well hello there")))).toMatchObject({ ok: true, message: "status 200" });
    expect(await runCheck(check({ contains: "bye" }), deps(respond("hello")))).toMatchObject({ ok: false, message: 'body does not contain "bye"' });
  });

  it("honours expect_status", async () => {
    expect(await runCheck(check(), deps(respond("", 502)))).toMatchObject({ ok: false, message: "unexpected status 502" });
    expect(await runCheck(check({ expectStatus: [401] }), deps(respond("", 401)))).toMatchObject({ ok: true });
  });

  it("sends method and headers", async () => {
    let seen: RequestInit | undefined;
    const fetchFn: FetchFn = (_url, init) => {
      seen = init;
      return Promise.resolve(new Response(""));
    };
    await runCheck(check({ method: "HEAD", headers: { "X-Probe": "1" } }), deps(fetchFn));
    expect(seen?.method).toBe("HEAD");
    expect(seen?.headers).toMatchObject({ "User-Agent": "infraping", "X-Probe": "1" });
  });

  it("turns network errors and timeouts into failures", async () => {
    const broken: FetchFn = () => Promise.reject(new Error("network down"));
    expect(await runCheck(check(), deps(broken))).toMatchObject({ ok: false, message: "network down" });
    const hanging: FetchFn = () => new Promise(() => undefined);
    expect(await runCheck(check({ timeoutMs: 20 }), deps(hanging))).toMatchObject({ ok: false, message: "timed out" });
  });

  it("fails slow responses", async () => {
    const c = clock();
    const slow: FetchFn = () => {
      c.advance(900);
      return Promise.resolve(new Response(""));
    };
    expect(await runCheck(check({ maxLatencyMs: 500 }), deps(slow, c))).toMatchObject({
      ok: false, message: "slow response: 900ms (limit 500ms)", latencyMs: 900,
    });
  });
});

describe("tcp", () => {
  it("reports open and closed ports", async () => {
    const tcp = check({ type: "tcp", address: "[::1]:5432" });
    let dialed: unknown;
    const open = deps(respond(""), clock(), (addr) => {
      dialed = addr;
      return { opened: Promise.resolve({}), close: () => Promise.resolve() };
    });
    expect(await runCheck(tcp, open)).toMatchObject({ ok: true, message: "port open" });
    expect(dialed).toEqual({ hostname: "::1", port: 5432 });
    expect(await runCheck(tcp, deps(respond("")))).toMatchObject({ ok: false, message: "connect [::1]:5432: connection refused" });
  });
});

describe("dns", () => {
  const doh = (answers: Record<string, unknown>): FetchFn => (url) => {
    expect(url.startsWith(DOH_URL)).toBe(true);
    const type = new URL(url).searchParams.get("type") ?? "";
    return Promise.resolve(Response.json(answers[type] ?? { Status: 0, Answer: [] }));
  };

  it("resolves and checks expected IPs", async () => {
    const fetchFn = doh({ A: { Status: 0, Answer: [{ type: 5, data: "alias." }, { type: 1, data: "192.0.2.1" }] }, AAAA: { Status: 0, Answer: [{ type: 28, data: "2001:db8::1" }] } });
    expect(await runCheck(check({ type: "dns", host: "x.test" }), deps(fetchFn))).toMatchObject({ ok: true, message: "resolves to 192.0.2.1, 2001:db8::1" });
    expect(await runCheck(check({ type: "dns", host: "x.test", expectIPs: ["192.0.2.9"] }), deps(fetchFn))).toMatchObject({ ok: false, message: "x.test does not resolve to 192.0.2.9" });
  });

  it("reports NXDOMAIN and empty answers", async () => {
    expect(await runCheck(check({ type: "dns", host: "gone.test" }), deps(doh({ A: { Status: 3 } })))).toMatchObject({ ok: false, message: "DNS: gone.test not found" });
    expect(await runCheck(check({ type: "dns", host: "x.test" }), deps(doh({})))).toMatchObject({ ok: false, message: "DNS: x.test has no addresses" });
  });
});
