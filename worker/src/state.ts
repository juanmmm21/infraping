// Máquina de estados e historial, con la misma semántica que internal/monitor e internal/history
// del binario, pero sobre datos planos porque cada ejecución del cron empieza de cero.
import type { CheckConfig, CheckResult, CheckState, Day, Sample, State, Status } from "./types.js";

const SAMPLE_WINDOW_MS = 24 * 3_600_000;
export const DAY_WINDOW = 90;
// El cron no es exacto al segundo: sin este margen, un check de 60 s se saltaría una de cada dos pasadas.
const SCHEDULE_SLACK_MS = 5_000;

export interface Transition {
  to: Exclude<Status, "unknown">;
  downtimeMs: number;
}

export function emptyState(): CheckState {
  return { status: "unknown", failStreak: 0, okStreak: 0, downSince: 0, lastRun: 0, samples: [], days: [] };
}

export function isDue(c: CheckConfig, st: CheckState | undefined, now: number): boolean {
  return st === undefined || now - st.lastRun >= c.intervalMs - SCHEDULE_SLACK_MS;
}

// observe aplica un resultado y devuelve la transición confirmada que hay que avisar, si la hay.
// El primer «up» no se avisa; la caída se data en el primer fallo, no en el que la confirma.
export function observe(st: CheckState, c: CheckConfig, res: CheckResult): Transition | null {
  if (res.ok) {
    st.failStreak = 0;
    st.okStreak++;
    if (st.status === "unknown") {
      st.status = "up";
      return null;
    }
    if (st.status === "down" && st.okStreak >= c.recoveryThreshold) {
      const downtimeMs = res.checkedAt - st.downSince;
      st.status = "up";
      st.downSince = 0;
      return { to: "up", downtimeMs };
    }
    return null;
  }
  st.okStreak = 0;
  st.failStreak++;
  if (st.failStreak === 1 && st.status !== "down") st.downSince = res.checkedAt;
  if (st.status === "down" || st.failStreak < c.failureThreshold) return null;
  st.status = "down";
  return { to: "down", downtimeMs: 0 };
}

export function dayKey(at: number): string {
  return new Date(at).toISOString().slice(0, 10);
}

export function record(st: CheckState, sample: Sample): void {
  st.samples.push(sample);
  const cutoff = sample.at - SAMPLE_WINDOW_MS;
  st.samples = st.samples.filter((s) => s.at >= cutoff);
  const date = dayKey(sample.at);
  let day = st.days.at(-1);
  if (day === undefined || day.date !== date) {
    day = { date, total: 0, ok: 0 };
    st.days.push(day);
  }
  day.total++;
  if (sample.ok) day.ok++;
  if (st.days.length > DAY_WINDOW) st.days = st.days.slice(-DAY_WINDOW);
}

export function dayUptime(d: Day): number {
  return d.total === 0 ? 1 : d.ok / d.total;
}

// uptimeDays suma muestras de los últimos n días naturales (UTC), contando hoy.
export function uptimeDays(st: CheckState, days: number, now: number): number {
  const from = dayKey(now - (days - 1) * 86_400_000);
  let total = 0;
  let ok = 0;
  for (const d of st.days) {
    if (d.date >= from) {
      total += d.total;
      ok += d.ok;
    }
  }
  return total === 0 ? 1 : ok / total;
}

// retain quita el historial de las comprobaciones que ya no están en la configuración.
export function retain(state: State, names: readonly string[]): State {
  const out: State = {};
  for (const name of names) {
    const st = state[name];
    if (st !== undefined) out[name] = st;
  }
  return out;
}
