// Package history guarda lo necesario para la página de estado: las últimas 24 h muestra a muestra
// y un resumen por día de los últimos 90, y lo persiste en un JSON para sobrevivir a reinicios.
package history

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

const (
	sampleWindow = 24 * time.Hour
	dayWindow    = 90
	dayLayout    = "2006-01-02"
)

// Sample is one probe result, reduced to what the status page draws.
type Sample struct {
	At        time.Time `json:"at"`
	OK        bool      `json:"ok"`
	LatencyMS float64   `json:"latency_ms"`
}

// Day aggregates every probe of one UTC day.
type Day struct {
	Date  string `json:"date"`
	Total int    `json:"total"`
	OK    int    `json:"ok"`
}

// Uptime returns the share of successful probes, from 0 to 1.
func (d Day) Uptime() float64 {
	if d.Total == 0 {
		return 1
	}
	return float64(d.OK) / float64(d.Total)
}

// CheckState is what is persisted about each check between restarts.
type CheckState struct {
	Status    string    `json:"status"`
	DownSince time.Time `json:"down_since,omitzero"`
	Message   string    `json:"message"`
	Samples   []Sample  `json:"samples"`
	Days      []Day     `json:"days"`
}

// Store is safe for concurrent use.
type Store struct {
	mu     sync.RWMutex
	checks map[string]*CheckState
}

// New returns an empty store.
func New() *Store {
	return &Store{checks: make(map[string]*CheckState)}
}

func (s *Store) state(name string) *CheckState {
	st, ok := s.checks[name]
	if !ok {
		st = &CheckState{Status: "unknown"}
		s.checks[name] = st
	}
	return st
}

// Record añade una muestra y recorta lo que se ha salido de las ventanas.
func (s *Store) Record(name string, sample Sample) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.state(name)
	st.Samples = append(st.Samples, sample)
	cutoff := sample.At.Add(-sampleWindow)
	first := sort.Search(len(st.Samples), func(i int) bool { return !st.Samples[i].At.Before(cutoff) })
	st.Samples = st.Samples[first:]

	date := sample.At.UTC().Format(dayLayout)
	if n := len(st.Days); n == 0 || st.Days[n-1].Date != date {
		st.Days = append(st.Days, Day{Date: date})
	}
	day := &st.Days[len(st.Days)-1]
	day.Total++
	if sample.OK {
		day.OK++
	}
	if len(st.Days) > dayWindow {
		st.Days = st.Days[len(st.Days)-dayWindow:]
	}
}

// SetStatus guarda el estado confirmado y el último mensaje de la sonda.
func (s *Store) SetStatus(name, status string, downSince time.Time, message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.state(name)
	st.Status = status
	st.DownSince = downSince
	st.Message = message
}

// Get returns a deep copy of one check's state.
func (s *Store) Get(name string) (CheckState, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	st, ok := s.checks[name]
	if !ok {
		return CheckState{}, false
	}
	return copyState(st), true
}

func copyState(st *CheckState) CheckState {
	out := *st
	out.Samples = append([]Sample(nil), st.Samples...)
	out.Days = append([]Day(nil), st.Days...)
	return out
}

// Uptime devuelve el porcentaje de sondas correctas en los últimos n días (n incluye hoy).
func (st CheckState) Uptime(days int, now time.Time) float64 {
	cutoff := now.UTC().AddDate(0, 0, -(days - 1)).Format(dayLayout)
	total, ok := 0, 0
	for _, d := range st.Days {
		if d.Date >= cutoff {
			total += d.Total
			ok += d.OK
		}
	}
	if total == 0 {
		return 1
	}
	return float64(ok) / float64(total)
}

type fileFormat struct {
	Version int                    `json:"version"`
	Checks  map[string]*CheckState `json:"checks"`
}

const fileVersion = 1

// Save escribe a un temporal y lo renombra: un corte a mitad nunca deja el fichero a medias.
func (s *Store) Save(path string) error {
	s.mu.RLock()
	data, err := json.Marshal(fileFormat{Version: fileVersion, Checks: s.checks})
	s.mu.RUnlock()
	if err != nil {
		return fmt.Errorf("encode state: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".infraping-state-*")
	if err != nil {
		return fmt.Errorf("create temp state: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		return errors.Join(fmt.Errorf("write state: %w", err), tmp.Close(), os.Remove(tmpName))
	}
	if err := tmp.Close(); err != nil {
		return errors.Join(fmt.Errorf("close state: %w", err), os.Remove(tmpName))
	}
	if err := os.Rename(tmpName, path); err != nil {
		return errors.Join(fmt.Errorf("replace state: %w", err), os.Remove(tmpName))
	}
	return nil
}

// Load lee un estado guardado; si no existe, el almacén se queda vacío sin error.
// Solo conserva las comprobaciones que siguen en la configuración.
func (s *Store) Load(path string, keep []string) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read state: %w", err)
	}
	var f fileFormat
	if err := json.Unmarshal(data, &f); err != nil {
		return fmt.Errorf("decode state %s: %w", path, err)
	}
	if f.Version != fileVersion {
		return fmt.Errorf("state %s has version %d, want %d", path, f.Version, fileVersion)
	}
	wanted := make(map[string]bool, len(keep))
	for _, name := range keep {
		wanted[name] = true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for name, st := range f.Checks {
		if wanted[name] && st != nil {
			s.checks[name] = st
		}
	}
	return nil
}
