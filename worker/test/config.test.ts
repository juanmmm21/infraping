import { describe, expect, it } from "vitest";
import binaryExample from "../../examples/infraping.example.yaml?raw";
import { ConfigError, loadConfig, parseDuration } from "../src/config.js";
import workerExample from "../infraping.example.yaml?raw";

function problems(text: string, env: Record<string, string> = {}): string[] {
  try {
    loadConfig(text, env);
  } catch (err) {
    if (err instanceof ConfigError) return err.problems;
    throw err;
  }
  return [];
}

describe("parseDuration", () => {
  it("accepts Go durations", () => {
    expect(parseDuration("30s")).toBe(30_000);
    expect(parseDuration("1m30s")).toBe(90_000);
    expect(parseDuration("500ms")).toBe(500);
    expect(parseDuration("2h")).toBe(7_200_000);
  });
  it("rejects garbage", () => {
    for (const bad of ["", "10", "5 m", "1d", "s"]) expect(() => parseDuration(bad)).toThrow();
  });
});

describe("loadConfig", () => {
  it("applies defaults and expands env only in values", () => {
    const cfg = loadConfig(
      `# \${NOT_SET} in a comment is fine
defaults: {interval: 2m, failure_threshold: 3}
notifiers:
  - {name: phone, type: telegram, token: "\${TOKEN}", chat_id: "1"}
checks:
  - {name: web, type: http, url: https://example.test}
  - {name: db, type: tcp, address: "db.example.test:5432", interval: 5m, public: false}
`,
      { TOKEN: "secret" },
    );
    expect(cfg.title).toBe("Service status");
    expect(cfg.notifiers[0]?.token).toBe("secret");
    expect(cfg.checks[0]).toMatchObject({ intervalMs: 120_000, timeoutMs: 10_000, failureThreshold: 3, recoveryThreshold: 1, method: "GET", public: true });
    expect(cfg.checks[1]).toMatchObject({ intervalMs: 300_000, public: false });
  });

  it("accepts the shared example file", () => {
    const text = workerExample;
    const cfg = loadConfig(text, { TELEGRAM_BOT_TOKEN: "t", TELEGRAM_CHAT_ID: "1", NTFY_TOPIC: "n" });
    expect(cfg.checks.length).toBeGreaterThan(0);
  });

  it("reports unset variables", () => {
    expect(problems("checks: [{name: a, type: http, url: '${A}'}]")).toEqual([
      "config references unset environment variables: A",
    ]);
  });

  it("collects every problem at once", () => {
    const got = problems(`status_page: {colour: red}
notifiers:
  - {name: n, type: pager}
checks:
  - {name: a, type: tls, address: "h:443"}
  - {name: a, type: dns, host: x.test, resolver: "1.1.1.1:53"}
  - {name: b, type: http, url: ftp://x, interval: 30s, notify: [ghost]}
  - {name: c, type: tcp, address: nope, typo: 1}
`);
    expect(got).toEqual(
      expect.arrayContaining([
        'status_page: unknown field "colour"',
        'notifiers[0] (n): unknown type "pager" (want telegram, discord, slack, ntfy or webhook)',
        "checks[0] (a): tls checks are not supported on Workers (they need the Go binary)",
        "checks[1]: resolver is not supported on Workers: DNS checks use Cloudflare's DNS-over-HTTPS",
        "check a: duplicate check name",
        "checks[2] (b): url must be absolute http or https",
        "checks[2] (b): interval must be at least 1m on Workers",
        'check b: unknown notifier "ghost"',
        "checks[3] (c): address must be host:port",
        'checks[3]: unknown field "typo"',
      ]),
    );
  });

  it("needs at least one check", () => {
    expect(problems("notifiers: []")).toContain("no checks defined");
  });
});

describe("binary example", () => {
  it("is rejected only for what Workers cannot do", () => {
    const text = binaryExample;
    const got = problems(text, { TELEGRAM_BOT_TOKEN: "t", TELEGRAM_CHAT_ID: "1", NTFY_TOPIC: "n" });
    expect(got.sort()).toEqual([
      "checks[1] (API health): interval must be at least 1m on Workers",
      "checks[3] (TLS certificate): tls checks are not supported on Workers (they need the Go binary)",
      "checks[3]: min_days_left is not supported on Workers: certificate checks need the Go binary",
      "checks[4]: resolver is not supported on Workers: DNS checks use Cloudflare's DNS-over-HTTPS",
    ].sort());
  });
});
