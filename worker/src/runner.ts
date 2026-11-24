// Una pasada del cron: carga el estado, lanza las comprobaciones que tocan, avisa y guarda.
import { type ProbeDeps, runCheck, targetLabel } from "./checks.js";
import { defaultSendOptions, send, type SendOptions, targets } from "./notify.js";
import { emptyState, isDue, observe, record, retain } from "./state.js";
import type { StateStore } from "./store.js";
import type { Config, Message } from "./types.js";

export interface Logger {
  info(msg: string, fields?: Record<string, unknown>): void;
  error(msg: string, fields?: Record<string, unknown>): void;
}

export const consoleLogger: Logger = {
  info: (msg, fields) => console.log(JSON.stringify({ level: "info", msg, ...fields })),
  error: (msg, fields) => console.error(JSON.stringify({ level: "error", msg, ...fields })),
};

export interface TickResult {
  ran: string[];
  alerts: Message[];
  failedNotifications: number;
}

export async function tick(
  cfg: Config,
  store: StateStore,
  deps: ProbeDeps,
  logger: Logger,
  sendOptions: SendOptions = defaultSendOptions,
): Promise<TickResult> {
  const now = deps.now();
  const state = retain(await store.load(), cfg.checks.map((c) => c.name));
  const due = cfg.checks.filter((c) => isDue(c, state[c.name], now));
  const results = await Promise.all(due.map((c) => runCheck(c, deps)));

  const alerts: Array<{ msg: Message; notify: string[] }> = [];
  due.forEach((c, i) => {
    const res = results[i];
    if (res === undefined) return;
    const st = state[c.name] ?? emptyState();
    state[c.name] = st;
    st.lastRun = now;
    record(st, { at: res.checkedAt, ok: res.ok, latencyMs: res.latencyMs });
    const transition = observe(st, c, res);
    if (!res.ok) logger.info("check failed", { check: c.name, reason: res.message, status: st.status });
    if (transition === null) return;
    alerts.push({
      notify: c.notify,
      msg: {
        kind: transition.to === "down" ? "down" : "recovered",
        check: c.name,
        target: targetLabel(c),
        reason: res.message,
        at: res.checkedAt,
        downtimeMs: transition.downtimeMs,
      },
    });
  });

  // Se guarda antes de avisar: si el worker se corta enviando, la siguiente pasada no repite el aviso.
  await store.save(state);

  const deliveries = alerts.flatMap(({ msg, notify }) =>
    targets(cfg.notifiers, notify).map(async (n) => {
      try {
        await send(deps.fetch, n, msg, sendOptions);
        logger.info("notification sent", { notifier: n.name, check: msg.check, kind: msg.kind });
        return true;
      } catch (err) {
        logger.error("notification failed", { notifier: n.name, check: msg.check, kind: msg.kind, err: (err as Error).message });
        return false;
      }
    }),
  );
  const outcomes = await Promise.all(deliveries);
  return { ran: due.map((c) => c.name), alerts: alerts.map((a) => a.msg), failedNotifications: outcomes.filter((ok) => !ok).length };
}
