// Carga el mismo YAML que el binario de Go. Lo que Workers no puede hacer se rechaza con un error
// claro en vez de ignorarse, para que nadie crea que está vigilando algo que no se vigila.
import { parse } from "yaml";
import type { CheckConfig, CheckType, Config, NotifierConfig, NotifierType } from "./types.js";

// El cron dispara como mucho una vez por minuto, así que un intervalo menor no tendría efecto.
export const MIN_INTERVAL_MS = 60_000;
const DEFAULT_INTERVAL_MS = 60_000;
const DEFAULT_TIMEOUT_MS = 10_000;
const DEFAULT_FAILURE_THRESHOLD = 2;
const DEFAULT_RECOVERY_THRESHOLD = 1;
const DEFAULT_TITLE = "Service status";
const DEFAULT_NTFY_SERVER = "https://ntfy.sh";

export class ConfigError extends Error {
  readonly problems: string[];
  constructor(problems: string[]) {
    super(`invalid configuration:\n- ${problems.join("\n- ")}`);
    this.name = "ConfigError";
    this.problems = problems;
  }
}

type Raw = Record<string, unknown>;

const UNIT_MS: Record<string, number> = { ms: 1, s: 1000, m: 60_000, h: 3_600_000 };

// parseDuration acepta el formato de Go que usa el YAML ("30s", "1m30s", "500ms").
export function parseDuration(raw: string): number {
  const re = /(\d+(?:\.\d+)?)(ms|s|m|h)/gy;
  let total = 0;
  let consumed = 0;
  for (let m = re.exec(raw); m !== null; m = re.exec(raw)) {
    const [whole, num, unit] = m;
    total += Number(num) * (UNIT_MS[unit ?? ""] ?? 0);
    consumed += whole.length;
  }
  if (raw === "" || consumed !== raw.length) {
    throw new Error(`invalid duration "${raw}"`);
  }
  return total;
}

// expandEnv sustituye ${VAR} solo en valores ya parseados, igual que el binario: en los comentarios no.
export function expandEnv(value: unknown, env: Record<string, unknown>, missing: Set<string>): unknown {
  if (typeof value === "string") {
    return value.replace(/\$\{([A-Za-z_][A-Za-z0-9_]*)\}/g, (_, name: string) => {
      const v = env[name];
      if (typeof v !== "string") {
        missing.add(name);
        return "";
      }
      return v;
    });
  }
  if (Array.isArray(value)) {
    return value.map((v) => expandEnv(v, env, missing));
  }
  if (value !== null && typeof value === "object") {
    const out: Raw = {};
    for (const [k, v] of Object.entries(value)) {
      out[k] = expandEnv(v, env, missing);
    }
    return out;
  }
  return value;
}

class Reader {
  constructor(
    private readonly label: string,
    private readonly raw: Raw,
    private readonly problems: string[],
  ) {}

  // unknown detecta campos que no existen, que suelen ser erratas que dejarían algo sin vigilar.
  unknown(allowed: readonly string[], unsupported: Record<string, string> = {}): void {
    for (const key of Object.keys(this.raw)) {
      if (key in unsupported) {
        this.problems.push(`${this.label}: ${key} ${unsupported[key]}`);
      } else if (!allowed.includes(key)) {
        this.problems.push(`${this.label}: unknown field "${key}"`);
      }
    }
  }

  str(key: string, fallback = ""): string {
    const v = this.raw[key];
    if (v === undefined || v === null) return fallback;
    if (typeof v === "string") return v;
    if (typeof v === "number" || typeof v === "boolean") return String(v);
    this.problems.push(`${this.label}: ${key} must be a string`);
    return fallback;
  }

  int(key: string, fallback: number): number {
    const v = this.raw[key];
    if (v === undefined || v === null) return fallback;
    if (typeof v === "number" && Number.isInteger(v)) return v;
    this.problems.push(`${this.label}: ${key} must be an integer`);
    return fallback;
  }

  bool(key: string, fallback: boolean): boolean {
    const v = this.raw[key];
    if (v === undefined || v === null) return fallback;
    if (typeof v === "boolean") return v;
    this.problems.push(`${this.label}: ${key} must be true or false`);
    return fallback;
  }

  duration(key: string, fallback: number): number {
    const v = this.raw[key];
    if (v === undefined || v === null) return fallback;
    if (typeof v !== "string") {
      this.problems.push(`${this.label}: ${key} must be a duration like "30s"`);
      return fallback;
    }
    try {
      return parseDuration(v);
    } catch (err) {
      this.problems.push(`${this.label}: ${key}: ${(err as Error).message}`);
      return fallback;
    }
  }

  strings(key: string): string[] {
    const v = this.raw[key];
    if (v === undefined || v === null) return [];
    if (Array.isArray(v) && v.every((x) => typeof x === "string")) return v;
    this.problems.push(`${this.label}: ${key} must be a list of strings`);
    return [];
  }

  ints(key: string): number[] {
    const v = this.raw[key];
    if (v === undefined || v === null) return [];
    if (Array.isArray(v) && v.every((x) => typeof x === "number" && Number.isInteger(x))) return v;
    this.problems.push(`${this.label}: ${key} must be a list of integers`);
    return [];
  }

  map(key: string): Record<string, string> {
    const v = this.raw[key];
    if (v === undefined || v === null) return {};
    if (isObject(v) && Object.values(v).every((x) => typeof x === "string")) {
      return v as Record<string, string>;
    }
    this.problems.push(`${this.label}: ${key} must be a map of strings`);
    return {};
  }

  object(key: string): Raw {
    const v = this.raw[key];
    if (v === undefined || v === null) return {};
    if (isObject(v)) return v;
    this.problems.push(`${this.label}: ${key} must be a map`);
    return {};
  }

  list(key: string): Raw[] {
    const v = this.raw[key];
    if (v === undefined || v === null) return [];
    if (Array.isArray(v) && v.every(isObject)) return v;
    this.problems.push(`${this.label}: ${key} must be a list of maps`);
    return [];
  }
}

function isObject(v: unknown): v is Raw {
  return v !== null && typeof v === "object" && !Array.isArray(v);
}

function validURL(raw: string): boolean {
  try {
    const u = new URL(raw);
    return (u.protocol === "http:" || u.protocol === "https:") && u.host !== "";
  } catch {
    // URL lanza con cualquier cadena mal formada: eso es justo lo que se quiere detectar.
    return false;
  }
}

function validAddress(raw: string): boolean {
  const i = raw.lastIndexOf(":");
  if (i <= 0) return false;
  const port = Number(raw.slice(i + 1));
  return Number.isInteger(port) && port > 0 && port < 65536;
}

const NOTIFIER_TYPES: readonly NotifierType[] = ["telegram", "discord", "slack", "ntfy", "webhook"];
const CHECK_TYPES: readonly CheckType[] = ["http", "tcp", "dns"];

function readNotifier(raw: Raw, index: number, problems: string[]): NotifierConfig {
  const r = new Reader(`notifiers[${index}]`, raw, problems);
  r.unknown(["name", "type", "token", "chat_id", "url", "server", "topic", "headers"]);
  const n: NotifierConfig = {
    name: r.str("name"),
    type: r.str("type") as NotifierType,
    token: r.str("token"),
    chatId: r.str("chat_id"),
    url: r.str("url"),
    server: r.str("server", DEFAULT_NTFY_SERVER).replace(/\/+$/, ""),
    topic: r.str("topic"),
    headers: r.map("headers"),
  };
  const label = `notifiers[${index}] (${n.name})`;
  if (n.name === "") problems.push(`notifiers[${index}]: name is required`);
  switch (n.type) {
    case "telegram":
      if (n.token === "" || n.chatId === "") problems.push(`${label}: telegram needs token and chat_id`);
      break;
    case "discord":
    case "slack":
    case "webhook":
      if (!validURL(n.url)) problems.push(`${label}: url must be absolute http or https`);
      break;
    case "ntfy":
      if (n.topic === "") problems.push(`${label}: ntfy needs a topic`);
      if (!validURL(n.server)) problems.push(`${label}: server must be absolute http or https`);
      break;
    default:
      if (!NOTIFIER_TYPES.includes(n.type)) {
        problems.push(`${label}: unknown type "${n.type}" (want telegram, discord, slack, ntfy or webhook)`);
      }
  }
  return n;
}

function readCheck(raw: Raw, index: number, defaults: Reader, problems: string[]): CheckConfig {
  const r = new Reader(`checks[${index}]`, raw, problems);
  r.unknown(
    [
      "name", "type", "url", "method", "headers", "expect_status", "contains", "address", "host",
      "expect_ips", "max_latency", "interval", "timeout", "failure_threshold", "recovery_threshold",
      "notify", "public",
    ],
    {
      resolver: "is not supported on Workers: DNS checks use Cloudflare's DNS-over-HTTPS",
      skip_tls_verify: "is not supported on Workers: fetch always verifies certificates",
      min_days_left: "is not supported on Workers: certificate checks need the Go binary",
    },
  );
  const c: CheckConfig = {
    name: r.str("name"),
    type: r.str("type") as CheckType,
    url: r.str("url"),
    method: r.str("method", "GET").toUpperCase(),
    headers: r.map("headers"),
    expectStatus: r.ints("expect_status"),
    contains: r.str("contains"),
    address: r.str("address"),
    host: r.str("host"),
    expectIPs: r.strings("expect_ips"),
    maxLatencyMs: r.duration("max_latency", 0),
    intervalMs: r.duration("interval", defaults.duration("interval", DEFAULT_INTERVAL_MS)),
    timeoutMs: r.duration("timeout", defaults.duration("timeout", DEFAULT_TIMEOUT_MS)),
    failureThreshold: r.int("failure_threshold", defaults.int("failure_threshold", DEFAULT_FAILURE_THRESHOLD)),
    recoveryThreshold: r.int("recovery_threshold", defaults.int("recovery_threshold", DEFAULT_RECOVERY_THRESHOLD)),
    notify: r.strings("notify"),
    public: r.bool("public", true),
  };
  const label = `checks[${index}] (${c.name})`;
  if (c.name === "") problems.push(`checks[${index}]: name is required`);
  if (c.intervalMs < MIN_INTERVAL_MS) problems.push(`${label}: interval must be at least 1m on Workers`);
  if (c.timeoutMs <= 0) problems.push(`${label}: timeout must be positive`);
  if (c.failureThreshold < 1 || c.recoveryThreshold < 1) problems.push(`${label}: thresholds must be at least 1`);
  switch (c.type) {
    case "http":
      if (!validURL(c.url)) problems.push(`${label}: url must be absolute http or https`);
      for (const code of c.expectStatus) {
        if (code < 100 || code > 599) problems.push(`${label}: invalid expect_status ${code}`);
      }
      break;
    case "tcp":
      if (!validAddress(c.address)) problems.push(`${label}: address must be host:port`);
      break;
    case "dns":
      if (c.host === "") problems.push(`${label}: host is required`);
      break;
    default:
      if ((c.type as string) === "tls") {
        problems.push(`${label}: tls checks are not supported on Workers (they need the Go binary)`);
      } else if (!CHECK_TYPES.includes(c.type)) {
        problems.push(`${label}: unknown type "${c.type}" (want http, tcp or dns)`);
      }
  }
  return c;
}

// loadConfig parsea, expande ${VAR} con las variables y secretos del worker y valida todo de una vez.
export function loadConfig(text: string, env: Record<string, unknown>): Config {
  let doc: unknown;
  try {
    doc = parse(text);
  } catch (err) {
    throw new ConfigError([`parse config: ${(err as Error).message}`]);
  }
  if (!isObject(doc)) throw new ConfigError(["the configuration must be a map"]);

  const missing = new Set<string>();
  const expanded = expandEnv(doc, env, missing) as Raw;
  if (missing.size > 0) {
    throw new ConfigError([`config references unset environment variables: ${[...missing].sort().join(", ")}`]);
  }

  const problems: string[] = [];
  const root = new Reader("config", expanded, problems);
  // state_file y listen son del binario; aquí se aceptan para poder compartir el mismo fichero.
  root.unknown(["defaults", "state_file", "status_page", "notifiers", "checks"]);
  const defaults = new Reader("defaults", root.object("defaults"), problems);
  defaults.unknown(["interval", "timeout", "failure_threshold", "recovery_threshold"]);
  const page = new Reader("status_page", root.object("status_page"), problems);
  page.unknown(["enabled", "listen", "title"]);

  const notifiers = root.list("notifiers").map((n, i) => readNotifier(n, i, problems));
  const checks = root.list("checks").map((c, i) => readCheck(c, i, defaults, problems));

  if (checks.length === 0) problems.push("no checks defined");
  const seen = new Set<string>();
  for (const n of notifiers) {
    if (n.name !== "" && seen.has(n.name)) problems.push(`notifier ${n.name}: duplicate notifier name`);
    seen.add(n.name);
  }
  const checkNames = new Set<string>();
  for (const c of checks) {
    if (c.name !== "" && checkNames.has(c.name)) problems.push(`check ${c.name}: duplicate check name`);
    checkNames.add(c.name);
    for (const target of c.notify) {
      if (!seen.has(target)) problems.push(`check ${c.name}: unknown notifier "${target}"`);
    }
  }
  if (problems.length > 0) throw new ConfigError(problems);
  return { title: page.str("title", DEFAULT_TITLE), notifiers, checks };
}
