package history

import (
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var base = time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)

func TestRecordTrimsSamplesAndAggregatesDays(t *testing.T) {
	s := New()
	s.Record("api", Sample{At: base, OK: true, LatencyMS: 10})
	s.Record("api", Sample{At: base.Add(time.Hour), OK: false})
	s.Record("api", Sample{At: base.Add(26 * time.Hour), OK: true, LatencyMS: 12})

	st, ok := s.Get("api")
	if !ok {
		t.Fatal("missing state")
	}
	if len(st.Samples) != 1 {
		t.Fatalf("want 1 sample in the 24h window, got %d", len(st.Samples))
	}
	if len(st.Days) != 2 || st.Days[0].Total != 2 || st.Days[0].OK != 1 {
		t.Fatalf("unexpected days %+v", st.Days)
	}
	if got := st.Days[0].Uptime(); got != 0.5 {
		t.Errorf("day uptime = %v", got)
	}
}

func TestDaysAreCapped(t *testing.T) {
	s := New()
	for i := range 100 {
		s.Record("x", Sample{At: base.AddDate(0, 0, i), OK: true})
	}
	st, _ := s.Get("x")
	if len(st.Days) != dayWindow {
		t.Fatalf("days = %d", len(st.Days))
	}
}

func TestUptimeWindow(t *testing.T) {
	s := New()
	s.Record("x", Sample{At: base.AddDate(0, 0, -10), OK: false})
	s.Record("x", Sample{At: base, OK: true})
	s.Record("x", Sample{At: base, OK: true})
	s.Record("x", Sample{At: base, OK: false})
	st, _ := s.Get("x")
	if got := st.Uptime(7, base); math.Abs(got-2.0/3.0) > 1e-9 {
		t.Errorf("7-day uptime = %v", got)
	}
	if got := st.Uptime(30, base); got != 0.5 {
		t.Errorf("30-day uptime = %v", got)
	}
	if got := (CheckState{}).Uptime(30, base); got != 1 {
		t.Errorf("empty uptime = %v", got)
	}
}

func TestGetReturnsCopy(t *testing.T) {
	s := New()
	s.Record("x", Sample{At: base, OK: true})
	st, _ := s.Get("x")
	st.Samples[0].OK = false
	again, _ := s.Get("x")
	if !again.Samples[0].OK {
		t.Fatal("Get must not expose internal slices")
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "state.json")
	s := New()
	s.Record("keep", Sample{At: base, OK: true, LatencyMS: 5})
	s.SetStatus("keep", "down", base, "timed out")
	s.Record("gone", Sample{At: base, OK: true})
	if err := s.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded := New()
	if err := loaded.Load(path, []string{"keep"}); err != nil {
		t.Fatalf("Load: %v", err)
	}
	st, ok := loaded.Get("keep")
	if !ok || st.Status != "down" || !st.DownSince.Equal(base) || len(st.Samples) != 1 {
		t.Fatalf("unexpected state %+v", st)
	}
	if _, ok := loaded.Get("gone"); ok {
		t.Error("checks removed from config must be dropped")
	}
}

func TestLoadMissingFileIsEmpty(t *testing.T) {
	if err := New().Load(filepath.Join(t.TempDir(), "nope.json"), nil); err != nil {
		t.Fatalf("Load: %v", err)
	}
}

func TestLoadRejectsGarbage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := New().Load(path, nil); err == nil {
		t.Fatal("want decode error")
	}
	if err := os.WriteFile(path, []byte(`{"version": 9, "checks": {}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := New().Load(path, nil); err == nil {
		t.Fatal("want version error")
	}
}

func TestRetainDropsRemovedChecks(t *testing.T) {
	s := New()
	s.Record("keep", Sample{At: base, OK: true})
	s.Record("gone", Sample{At: base, OK: false})
	s.Retain([]string{"keep", "new"})
	if _, ok := s.Get("keep"); !ok {
		t.Error("kept check lost its history")
	}
	if _, ok := s.Get("gone"); ok {
		t.Error("removed check still has history")
	}
	if _, ok := s.Get("new"); ok {
		t.Error("Retain must not create entries")
	}
}
