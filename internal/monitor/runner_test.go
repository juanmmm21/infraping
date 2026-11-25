package monitor

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/juanmmm21/infraping/internal/check"
	"github.com/juanmmm21/infraping/internal/config"
	"github.com/juanmmm21/infraping/internal/history"
	"github.com/juanmmm21/infraping/internal/notify"
)

type scripted struct {
	mu      sync.Mutex
	results []bool
	clock   time.Time
}

func (s *scripted) Check(context.Context) check.Result {
	s.mu.Lock()
	defer s.mu.Unlock()
	ok := true
	if len(s.results) > 0 {
		ok, s.results = s.results[0], s.results[1:]
	}
	s.clock = s.clock.Add(time.Minute)
	msg := "fine"
	if !ok {
		msg = "connection refused"
	}
	return check.Result{OK: ok, Message: msg, CheckedAt: s.clock, Latency: 25 * time.Millisecond}
}

type fakeSender struct {
	mu   sync.Mutex
	sent []notify.Message
	to   [][]string
}

func (f *fakeSender) Dispatch(_ context.Context, msg notify.Message, names []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, msg)
	f.to = append(f.to, names)
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestProbeCycle(t *testing.T) {
	cfg := config.CheckConfig{Name: "db", Type: config.CheckTCP, Address: "db:5432", FailureThreshold: 2, RecoveryThreshold: 1, Notify: []string{"phone"}}
	probe := &scripted{results: []bool{true, false, false, false, true}, clock: t0}
	store := history.New()
	sender := &fakeSender{}
	r := NewRunner(nil, store, sender, quiet())
	tracker := NewTracker(cfg.Name, cfg.FailureThreshold, cfg.RecoveryThreshold)
	target := Target{Config: cfg, Checker: probe}

	for range 5 {
		r.Probe(context.Background(), target, tracker)
	}
	if len(sender.sent) != 2 {
		t.Fatalf("want down + recovered, got %+v", sender.sent)
	}
	downMsg, upMsg := sender.sent[0], sender.sent[1]
	if downMsg.Kind != notify.KindDown || downMsg.Target != "db:5432" || downMsg.Reason != "connection refused" {
		t.Errorf("down message = %+v", downMsg)
	}
	if upMsg.Kind != notify.KindRecovered || upMsg.Downtime != 3*time.Minute {
		t.Errorf("recovered message = %+v", upMsg)
	}
	if sender.to[0][0] != "phone" {
		t.Errorf("targets = %v", sender.to[0])
	}
	st, _ := store.Get("db")
	if st.Status != "up" || len(st.Samples) != 5 || st.Samples[0].LatencyMS != 25 {
		t.Errorf("store = %+v", st)
	}
	// La caída empieza en el primer fallo (minuto 2) y acaba con la vuelta (minuto 5).
	want := []history.Incident{{Start: t0.Add(2 * time.Minute), End: t0.Add(5 * time.Minute)}}
	if len(st.Incidents) != 1 || st.Incidents[0] != want[0] {
		t.Errorf("incidents = %+v, want %+v", st.Incidents, want)
	}
}

func TestProbeIgnoresShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	store := history.New()
	r := NewRunner(nil, store, &fakeSender{}, quiet())
	cfg := config.CheckConfig{Name: "x"}
	r.Probe(ctx, Target{Config: cfg, Checker: &scripted{results: []bool{false}}}, NewTracker("x", 1, 1))
	if _, ok := store.Get("x"); ok {
		t.Fatal("a probe cut by shutdown must not be recorded")
	}
}

func TestRunRestoresOutageAndStops(t *testing.T) {
	store := history.New()
	store.SetStatus("api", "down", t0, "timed out")
	sender := &fakeSender{}
	cfg := config.CheckConfig{Name: "api", Type: config.CheckHTTP, URL: "https://x", Interval: config.Duration(time.Second), FailureThreshold: 1, RecoveryThreshold: 1}
	probe := &scripted{results: []bool{true}, clock: t0}
	r := NewRunner([]Target{{Config: cfg, Checker: probe}}, store, sender, quiet())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		r.Run(ctx)
		close(done)
	}()
	deadline := time.After(10 * time.Second)
	for {
		sender.mu.Lock()
		n := len(sender.sent)
		sender.mu.Unlock()
		if n > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("restored outage never reported recovery")
		case <-time.After(20 * time.Millisecond):
		}
	}
	cancel()
	<-done
	if sender.sent[0].Kind != notify.KindRecovered {
		t.Fatalf("want recovery, got %+v", sender.sent[0])
	}
	// El estado venía sin incidencias: la caída restaurada se apunta desde su comienzo y se cierra.
	st, _ := store.Get("api")
	if len(st.Incidents) != 1 || !st.Incidents[0].Start.Equal(t0) || st.Incidents[0].Ongoing() {
		t.Fatalf("incidents = %+v", st.Incidents)
	}
}

func TestPersistSavesOnShutdown(t *testing.T) {
	path := t.TempDir() + "/state.json"
	store := history.New()
	store.Record("a", history.Sample{At: t0, OK: true})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	Persist(ctx, store, path, time.Hour, quiet())
	loaded := history.New()
	if err := loaded.Load(path, []string{"a"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := loaded.Get("a"); !ok {
		t.Fatal("state not saved on shutdown")
	}
}

func TestTargetLabel(t *testing.T) {
	if TargetLabel(config.CheckConfig{Type: config.CheckDNS, Host: "a.b"}) != "a.b" ||
		TargetLabel(config.CheckConfig{Type: config.CheckTLS, Address: "a:443"}) != "a:443" {
		t.Fatal("labels")
	}
}
