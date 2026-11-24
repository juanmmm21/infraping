// Tipos compartidos del worker. La configuración es la misma del binario de Go, menos lo que el
// runtime de Workers no puede hacer (sondas tls y resolver DNS propio).

export type CheckType = "http" | "tcp" | "dns";
export type NotifierType = "telegram" | "discord" | "slack" | "ntfy" | "webhook";

export interface CheckConfig {
  name: string;
  type: CheckType;
  url: string;
  method: string;
  headers: Record<string, string>;
  expectStatus: number[];
  contains: string;
  address: string;
  host: string;
  expectIPs: string[];
  maxLatencyMs: number;
  intervalMs: number;
  timeoutMs: number;
  failureThreshold: number;
  recoveryThreshold: number;
  notify: string[];
  public: boolean;
}

export interface NotifierConfig {
  name: string;
  type: NotifierType;
  token: string;
  chatId: string;
  url: string;
  server: string;
  topic: string;
  headers: Record<string, string>;
}

export interface Config {
  title: string;
  notifiers: NotifierConfig[];
  checks: CheckConfig[];
}

export interface CheckResult {
  ok: boolean;
  message: string;
  latencyMs: number;
  checkedAt: number;
}

export type Status = "unknown" | "up" | "down";

export interface Sample {
  at: number;
  ok: boolean;
  latencyMs: number;
}

export interface Day {
  date: string;
  total: number;
  ok: number;
}

// CheckState es lo que se guarda de cada comprobación entre ejecuciones del cron.
export interface CheckState {
  status: Status;
  failStreak: number;
  okStreak: number;
  downSince: number;
  lastRun: number;
  samples: Sample[];
  days: Day[];
}

export type State = Record<string, CheckState>;

export type MessageKind = "down" | "recovered" | "test";

export interface Message {
  kind: MessageKind;
  check: string;
  target: string;
  reason: string;
  at: number;
  downtimeMs: number;
}
