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

var now = time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

func newTestServer(store *history.Store, names ...string) *Server {
	entries := make([]Entry, 0, len(names))
	for _, n := range names {
		entries = append(entries, Entry{Name: n})
	}
	s := NewServer("Acme status", entries, store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	s.now = func() time.Time { return now }
	return s
}

func TestBuildOverall(t *testing.T) {
	store := history.New()
	store.Record("web", history.Sample{At: now.Add(-time.Minute), OK: true, LatencyMS: 40})
	store.Record("web", history.Sample{At: now, OK: true, LatencyMS: 60})
	store.SetStatus("web", "up", time.Time{}, "status 200")

	view := newTestServer(store, "web").Build()
	if view.Overall != "All systems operational" {
		t.Fatalf("overall = %q", view.Overall)
	}
	cv := view.Checks[0]
	if cv.AvgLatencyMS != 50 || cv.Uptime24h != 1 || len(cv.Days) != barDays || cv.Sparkline == "" {
		t.Errorf("view = %+v", cv)
	}
	if last := cv.Days[barDays-1]; last.Date != "2026-06-01" || last.Level != "ok" {
		t.Errorf("today bar = %+v", last)
	}
	if cv.Days[0].Level != "none" {
		t.Errorf("old days without data must be empty: %+v", cv.Days[0])
	}

	store.SetStatus("api", "down", now, "boom")
	if got := newTestServer(store, "web", "api").Build().Overall; got != "Partial outage" {
		t.Errorf("overall = %q", got)
	}
	if got := newTestServer(store, "api").Build().Overall; got != "Major outage" {
		t.Errorf("overall = %q", got)
	}
	if got := newTestServer(store, "web", "new").Build().Overall; got != "Collecting data" {
		t.Errorf("overall = %q", got)
	}
}

func TestPageHidesPrivateDetails(t *testing.T) {
	store := history.New()
	store.Record("db", history.Sample{At: now, OK: false})
	store.SetStatus("db", "down", now, "connect 10.0.0.5:5432: connection refused")
	store.SetStatus("secret", "up", time.Time{}, "")

	srv := httptest.NewServer(newTestServer(store, "db", "<script>x</script>").Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/")
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
	html := string(body)
	if resp.StatusCode != http.StatusOK || !strings.Contains(html, "Acme status") || !strings.Contains(html, "Partial outage") {
		t.Fatalf("unexpected page (%d):\n%s", resp.StatusCode, html)
	}
	if strings.Contains(html, "10.0.0.5") || strings.Contains(html, "<script>x") {
		t.Error("page leaks the error message or does not escape names")
	}
	if strings.Contains(html, "secret") {
		t.Error("checks not passed as entries must stay hidden")
	}
}

func TestJSONAndHealth(t *testing.T) {
	store := history.New()
	store.Record("web", history.Sample{At: now, OK: true, LatencyMS: 12})
	srv := httptest.NewServer(newTestServer(store, "web").Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/status")
	if err != nil {
		t.Fatal(err)
	}
	var view View
	decErr := json.NewDecoder(resp.Body).Decode(&view)
	if err := resp.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if decErr != nil {
		t.Fatal(decErr)
	}
	if len(view.Checks) != 1 || view.Checks[0].Name != "web" || view.Checks[0].LastCheckedAt == "" {
		t.Errorf("json view = %+v", view)
	}

	resp, err = http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("healthz = %d", resp.StatusCode)
	}

	resp, err = http.Get(srv.URL + "/nope")
	if err != nil {
		t.Fatal(err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown path = %d", resp.StatusCode)
	}
}

func TestFormatPercentAndSparkline(t *testing.T) {
	if formatPercent(0.99996) != "99.99%" || formatPercent(1) != "100%" || formatPercent(0.5) != "50.00%" {
		t.Errorf("formatPercent: %s %s %s", formatPercent(0.99996), formatPercent(1), formatPercent(0.5))
	}
	var samples []history.Sample
	for i := range 1000 {
		samples = append(samples, history.Sample{At: now.Add(time.Duration(i) * time.Second), OK: true, LatencyMS: float64(i % 50)})
	}
	points := strings.Fields(sparkline(samples, 160, 32))
	if len(points) != 121 || points[0] != "0.0,31.0" {
		t.Errorf("sparkline: %d points, first %q", len(points), points[0])
	}
	if sparkline(samples[:1], 160, 32) != "" {
		t.Error("one sample is not a line")
	}
}

func TestUpdateChangesTitleAndEntries(t *testing.T) {
	store := history.New()
	store.Record("web", history.Sample{At: now.Add(-time.Minute), OK: true})
	s := newTestServer(store, "web")
	s.Update("New title", []Entry{{Name: "web"}, {Name: "api"}})
	view := s.Build()
	if view.Title != "New title" {
		t.Errorf("title = %q", view.Title)
	}
	if len(view.Checks) != 2 || view.Checks[1].Name != "api" || view.Checks[1].Status != "unknown" {
		t.Errorf("checks = %+v", view.Checks)
	}
	if view.Overall != "Collecting data" {
		t.Errorf("overall = %q", view.Overall)
	}
}
