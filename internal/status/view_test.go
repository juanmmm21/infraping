package status

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/juanmmm21/infraping/internal/history"
)

func get(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	if closeErr := resp.Body.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(body)
}

func TestAssignIDs(t *testing.T) {
	got := assignIDs([]Entry{{Name: "Public API"}, {Name: "public-api"}, {Name: "Café ☕ 2"}, {Name: "???"}})
	want := []string{"public-api", "public-api-2", "caf-2", "check"}
	for i, e := range got {
		if e.ID != want[i] {
			t.Errorf("id %d = %q, want %q", i, e.ID, want[i])
		}
	}
}

func TestHumanDuration(t *testing.T) {
	cases := map[time.Duration]string{
		0: "0s", 45 * time.Second: "45s", 2 * time.Minute: "2m", 90 * time.Second: "1m 30s",
		3*time.Hour + 12*time.Minute: "3h 12m", 5 * time.Hour: "5h", 72 * time.Hour: "3 days",
	}
	for d, want := range cases {
		if got := humanDuration(d); got != want {
			t.Errorf("humanDuration(%s) = %q, want %q", d, got, want)
		}
	}
}

func outageStore() *history.Store {
	store := history.New()
	for i := range 100 {
		at := now.Add(-time.Duration(100-i) * time.Minute)
		ok := i < 90 || i >= 95
		store.Record("api", history.Sample{At: at, OK: ok, LatencyMS: float64(i + 1)})
	}
	store.SetStatus("api", "up", time.Time{}, "dial tcp 10.0.0.5:443: connection refused")
	store.OpenIncident("api", now.Add(-48*time.Hour))
	store.CloseIncident("api", now.Add(-47*time.Hour))
	store.OpenIncident("api", now.Add(-10*time.Minute))
	store.CloseIncident("api", now.Add(-5*time.Minute))
	return store
}

func TestBuildDetail(t *testing.T) {
	s := NewServer("Acme", []Entry{{Name: "API", Kind: "HTTP", Interval: time.Minute}}, outageStore(), quietLogger())
	s.now = func() time.Time { return now }
	// La entrada se llama «API» pero el historial es de «api»: se busca por nombre exacto.
	if d, ok := s.BuildDetail("api"); !ok || d.Checks24h != 0 {
		t.Fatalf("detail for a check without history = %+v, %v", d, ok)
	}
	if _, ok := s.BuildDetail("nope"); ok {
		t.Fatal("unknown id must not be found")
	}

	s = NewServer("Acme", []Entry{{Name: "api", Kind: "HTTP", Interval: time.Minute}}, outageStore(), quietLogger())
	s.now = func() time.Time { return now }
	d, ok := s.BuildDetail("api")
	if !ok {
		t.Fatal("detail not found")
	}
	if d.Checks24h != 100 || d.Failed24h != 5 || d.IntervalSeconds != 60 || d.Interval != "1m" {
		t.Errorf("counts = %d/%d interval %d %q", d.Checks24h, d.Failed24h, d.IntervalSeconds, d.Interval)
	}
	// 95 latencias correctas: 1..90 y 96..100.
	if d.P50MS != 48 || d.P95MS != 96 || d.MaxMS != 100 {
		t.Errorf("percentiles p50=%v p95=%v max=%v", d.P50MS, d.P95MS, d.MaxMS)
	}
	if len(d.Incidents) != 2 || d.Incidents[0].Duration != "5m" || d.Incidents[1].DurationSeconds != 3600 {
		t.Errorf("incidents = %+v", d.Incidents)
	}
	if len(d.Chart.Failures) != 1 || d.Chart.Empty || d.Chart.Line == "" {
		t.Errorf("chart = %+v", d.Chart)
	}
}

func TestDetailRoutes(t *testing.T) {
	store := outageStore()
	store.SetStatus("hidden", "down", now, "boom")
	s := NewServer("Acme", []Entry{{Name: "api", Kind: "HTTP", Interval: time.Minute}}, store, quietLogger())
	s.now = func() time.Time { return now }
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	code, html := get(t, srv.URL+"/checks/api")
	if code != http.StatusOK || !strings.Contains(html, "<h1>api</h1>") || !strings.Contains(html, "HTTP check every 1m") {
		t.Fatalf("detail page (%d):\n%s", code, html)
	}
	code, body := get(t, srv.URL+"/api/checks/api")
	var d Detail
	if err := json.Unmarshal([]byte(body), &d); err != nil || code != http.StatusOK {
		t.Fatalf("detail json (%d): %v", code, err)
	}
	if d.Check.ID != "api" || len(d.Latency) != 100 || len(d.Incidents) != 2 {
		t.Errorf("detail json = %+v", d.Check)
	}
	for _, page := range []string{html, body} {
		if strings.Contains(page, "10.0.0.5") || strings.Contains(page, "refused") {
			t.Error("detail leaks the error message")
		}
	}

	// Una comprobación privada no tiene página, aunque tenga historial.
	for _, path := range []string{"/checks/hidden", "/api/checks/hidden", "/checks/nope"} {
		if code, _ := get(t, srv.URL+path); code != http.StatusNotFound {
			t.Errorf("%s = %d, want 404", path, code)
		}
	}

	code, home := get(t, srv.URL+"/")
	if code != http.StatusOK || !strings.Contains(home, `href="checks/api"`) || !strings.Contains(home, "Recent incidents") {
		t.Errorf("home does not link the detail page:\n%s", home)
	}
}

func TestRecentIncidentsAcrossChecks(t *testing.T) {
	store := history.New()
	for i, name := range []string{"a", "b", "c"} {
		for j := range 3 {
			start := now.Add(-time.Duration(10*i+j) * time.Hour)
			store.OpenIncident(name, start)
			store.CloseIncident(name, start.Add(time.Minute))
		}
	}
	view := newTestServer(store, "a", "b", "c").Build()
	if len(view.RecentIncidents) != recentIncidents {
		t.Fatalf("got %d recent incidents", len(view.RecentIncidents))
	}
	if first := view.RecentIncidents[0]; first.Check != "a" || !first.Start.Equal(now) {
		t.Errorf("newest first, got %+v", first)
	}
	if view.Summary != (Summary{Total: 3, Unknown: 3}) {
		t.Errorf("summary = %+v", view.Summary)
	}
}

func TestChart(t *testing.T) {
	if niceCeil(0) != 1 || niceCeil(87) != 100 || niceCeil(130) != 200 || niceCeil(420) != 500 || niceCeil(1000) != 1000 {
		t.Error("niceCeil")
	}
	if formatMS(250) != "250 ms" || formatMS(1000) != "1 s" || formatMS(1500) != "1.5 s" {
		t.Errorf("formatMS: %s %s %s", formatMS(250), formatMS(1000), formatMS(1500))
	}
	if c := latencyChart(nil, now); !c.Empty || c.XTicks[len(c.XTicks)-1].Label != "now" || len(c.YTicks) != yTicks+1 {
		t.Errorf("empty chart = %+v", c)
	}

	var samples []history.Sample
	for i := range 60 {
		ok := i < 20 || (i >= 25 && i != 40)
		samples = append(samples, history.Sample{At: now.Add(-time.Duration(60-i) * time.Minute), OK: ok, LatencyMS: 100})
	}
	c := latencyChart(samples, now)
	// Dos tramos de fallos: uno de cinco sondas seguidas y otro de una sola.
	if len(c.Failures) != 2 {
		t.Fatalf("bands = %+v", c.Failures)
	}
	if c.Failures[0].W <= c.Failures[1].W || c.Failures[1].W <= 0 {
		t.Errorf("band widths = %+v", c.Failures)
	}
	// Cada tramo correcto empieza con un M: los fallos cortan la línea.
	if strings.Count(c.Line, "M") != 3 || strings.Count(c.Area, "Z") != 3 {
		t.Errorf("line %q area %q", c.Line, c.Area)
	}
	for _, tick := range c.XTicks[:len(c.XTicks)-1] {
		if tick.Pos > plotRight-3*(plotRight-plotLeft)/24 {
			t.Errorf("tick %q too close to now", tick.Label)
		}
	}
}

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestChartWithLittleData(t *testing.T) {
	c := latencyChart([]history.Sample{{At: now.Add(-time.Minute), OK: false}}, now)
	labels := make([]string, 0, len(c.YTicks))
	for _, tick := range c.YTicks {
		labels = append(labels, tick.Label)
	}
	if got := strings.Join(labels, ","); got != "0 ms,25 ms,50 ms,75 ms,100 ms" {
		t.Errorf("y axis = %s", got)
	}
	if len(c.Failures) != 1 || c.Failures[0].W < 2 {
		t.Errorf("a single failure must be visible: %+v", c.Failures)
	}
}
