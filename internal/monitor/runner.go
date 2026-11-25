package monitor

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/juanmmm21/infraping/internal/check"
	"github.com/juanmmm21/infraping/internal/config"
	"github.com/juanmmm21/infraping/internal/history"
	"github.com/juanmmm21/infraping/internal/notify"
)

// Sender is the part of notify.Dispatcher the runner needs.
type Sender interface {
	Dispatch(ctx context.Context, msg notify.Message, names []string)
}

// Target is one configured check ready to run.
type Target struct {
	Config  config.CheckConfig
	Checker check.Checker
}

// Runner lanza cada comprobación a su ritmo y convierte sus resultados en historial y avisos.
type Runner struct {
	targets []Target
	store   *history.Store
	sender  Sender
	logger  *slog.Logger
}

// NewRunner wires checks, history and notifications together.
func NewRunner(targets []Target, store *history.Store, sender Sender, logger *slog.Logger) *Runner {
	return &Runner{targets: targets, store: store, sender: sender, logger: logger}
}

// TargetLabel es lo que se enseña como «qué se vigila» en avisos y en la página de estado.
func TargetLabel(cfg config.CheckConfig) string {
	switch cfg.Type {
	case config.CheckHTTP:
		return cfg.URL
	case config.CheckDNS:
		return cfg.Host
	default:
		return cfg.Address
	}
}

// Run bloquea hasta que ctx se cancela; cada comprobación vive en su propia goroutine.
func (r *Runner) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for _, t := range r.targets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.loop(ctx, t)
		}()
	}
	wg.Wait()
}

func (r *Runner) loop(ctx context.Context, t Target) {
	cfg := t.Config
	tracker := NewTracker(cfg.Name, cfg.FailureThreshold, cfg.RecoveryThreshold)
	if saved, ok := r.store.Get(cfg.Name); ok && saved.Status == string(StatusDown) {
		tracker.Restore(StatusDown, saved.DownSince)
		// Un estado guardado por una versión sin incidencias no la tendría apuntada.
		r.store.OpenIncident(cfg.Name, saved.DownSince)
	}

	// Un pequeño desfase aleatorio al arrancar evita que todas las sondas salgan en el mismo instante.
	interval := cfg.Interval.Std()
	jitter := time.Duration(rand.Int64N(int64(min(interval, 5*time.Second)) + 1))
	select {
	case <-ctx.Done():
		return
	case <-time.After(jitter):
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		r.Probe(ctx, t, tracker)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Probe ejecuta una sonda y aplica su resultado; se exporta para probar el ciclo sin esperar al reloj.
func (r *Runner) Probe(ctx context.Context, t Target, tracker *Tracker) {
	res := t.Checker.Check(ctx)
	if ctx.Err() != nil {
		// Al apagar, la sonda cortada a medias no es una caída del servicio.
		return
	}
	cfg := t.Config
	r.store.Record(cfg.Name, history.Sample{At: res.CheckedAt, OK: res.OK, LatencyMS: float64(res.Latency.Microseconds()) / 1000})
	ev, fired := tracker.Observe(res)
	r.store.SetStatus(cfg.Name, string(tracker.Status()), tracker.DownSince(), res.Message)
	if !res.OK {
		r.logger.Warn("check failed", "check", cfg.Name, "reason", res.Message, "status", tracker.Status())
	}
	if !fired {
		return
	}
	msg := notify.Message{Check: cfg.Name, Target: TargetLabel(cfg), Reason: res.Message, At: res.CheckedAt}
	if ev.To == StatusDown {
		r.store.OpenIncident(cfg.Name, tracker.DownSince())
		msg.Kind = notify.KindDown
		r.logger.Error("check down", "check", cfg.Name, "reason", res.Message)
	} else {
		r.store.CloseIncident(cfg.Name, res.CheckedAt)
		msg.Kind = notify.KindRecovered
		msg.Downtime = ev.Downtime
		r.logger.Info("check recovered", "check", cfg.Name, "downtime", ev.Downtime)
	}
	// Un aviso ya decidido se entrega aunque el proceso empiece a apagarse justo después.
	r.sender.Dispatch(context.WithoutCancel(ctx), msg, cfg.Notify)
}

// Persist guarda el historial cada cierto tiempo hasta que ctx se cancela, y una última vez al salir.
func Persist(ctx context.Context, store *history.Store, path string, every time.Duration, logger *slog.Logger) {
	if path == "" {
		return
	}
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			if err := store.Save(path); err != nil {
				logger.Error("save state on shutdown", "path", path, "err", err)
			}
			return
		case <-ticker.C:
			if err := store.Save(path); err != nil {
				logger.Error("save state", "path", path, "err", err)
			}
		}
	}
}
