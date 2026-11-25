// El estado entero cabe en una fila: una sola escritura por minuto queda muy por debajo del
// límite gratuito de D1.
import type { CheckState, Incident, Sample, State } from "./types.js";

// Las muestras se guardan en columnas (segundos, "1"/"0", ms enteros): el plan gratuito da 10 ms de
// CPU por invocación y parsear 24 h de objetos sueltos costaba 3,5 veces más.
interface StoredSamples {
  t: number[];
  o: string;
  l: number[];
}

type StoredCheck = Omit<CheckState, "samples" | "incidents"> & { samples: StoredSamples; incidents?: Incident[] };

export function encodeState(state: State): string {
  const out: Record<string, StoredCheck> = {};
  for (const [name, st] of Object.entries(state)) {
    out[name] = {
      ...st,
      samples: {
        t: st.samples.map((x) => Math.round(x.at / 1000)),
        o: st.samples.map((x) => (x.ok ? "1" : "0")).join(""),
        l: st.samples.map((x) => Math.round(x.latencyMs)),
      },
    };
  }
  return JSON.stringify(out);
}

export function decodeState(text: string): State {
  const raw = JSON.parse(text) as Record<string, StoredCheck>;
  const out: State = {};
  for (const [name, st] of Object.entries(raw)) {
    const { t, o, l } = st.samples;
    if (t.length !== o.length || t.length !== l.length) throw new Error(`state of ${name}: sample columns differ in length`);
    const samples: Sample[] = t.map((sec, i) => ({ at: sec * 1000, ok: o[i] === "1", latencyMs: l[i] ?? 0 }));
    // Un estado guardado antes de existir las incidencias no las trae.
    out[name] = { ...st, samples, incidents: st.incidents ?? [] };
  }
  return out;
}

export interface StateStore {
  load(): Promise<State>;
  save(state: State): Promise<void>;
}

const STATE_KEY = "state";

export class D1Store implements StateStore {
  private ready = false;

  constructor(private readonly db: D1Database) {}

  private async init(): Promise<void> {
    if (this.ready) return;
    await this.db
      .prepare("CREATE TABLE IF NOT EXISTS infraping_state (key TEXT PRIMARY KEY, value TEXT NOT NULL)")
      .run();
    this.ready = true;
  }

  async load(): Promise<State> {
    await this.init();
    const row = await this.db
      .prepare("SELECT value FROM infraping_state WHERE key = ?")
      .bind(STATE_KEY)
      .first<{ value: string }>();
    if (row === null) return {};
    try {
      return decodeState(row.value);
    } catch (err) {
      throw new Error(`decode saved state: ${(err as Error).message}`, { cause: err });
    }
  }

  async save(state: State): Promise<void> {
    await this.init();
    await this.db
      .prepare(
        "INSERT INTO infraping_state (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value",
      )
      .bind(STATE_KEY, encodeState(state))
      .run();
  }
}

export class MemoryStore implements StateStore {
  saved: State = {};

  // Pasa por el mismo formato que D1 para que los tests prueben también la codificación.
  load(): Promise<State> {
    return Promise.resolve(decodeState(encodeState(this.saved)));
  }

  save(state: State): Promise<void> {
    this.saved = decodeState(encodeState(state));
    return Promise.resolve();
  }
}
