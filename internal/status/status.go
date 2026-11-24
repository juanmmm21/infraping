// Package status sirve la página pública de estado, su versión en JSON y un /healthz.
package status

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/juanmmm21/infraping/internal/history"
)

//go:embed page.html.tmpl
var assets embed.FS

var page = template.Must(template.New("page.html.tmpl").Funcs(template.FuncMap{
	"pct": formatPercent,
	"ms":  func(v float64) string { return fmt.Sprintf("%.0f ms", v) },
}).ParseFS(assets, "page.html.tmpl"))

// formatPercent no redondea a 100 % lo que no lo es: 99,996 % se ve como 99.99 %.
func formatPercent(v float64) string {
	if v >= 1 {
		return "100%"
	}
	return fmt.Sprintf("%.2f%%", float64(int(v*10000))/100)
}

// Entry is a check as the status page knows it. Only public checks are passed in.
type Entry struct {
	Name string
}

// DayBar is one cell of the 90-day strip.
type DayBar struct {
	Date   string  `json:"date"`
	Uptime float64 `json:"uptime"`
	Empty  bool    `json:"no_data"`
	Level  string  `json:"-"`
}

// CheckView is everything shown for one check. It deliberately omits targets and error
// messages: a public page should not reveal internal addresses or stack traces.
type CheckView struct {
	Name          string   `json:"name"`
	Status        string   `json:"status"`
	Uptime24h     float64  `json:"uptime_24h"`
	Uptime30d     float64  `json:"uptime_30d"`
	Uptime90d     float64  `json:"uptime_90d"`
	AvgLatencyMS  float64  `json:"avg_latency_ms"`
	LastCheckedAt string   `json:"last_checked_at,omitempty"`
	Days          []DayBar `json:"days"`
	Sparkline     string   `json:"-"`
}

// View is the whole page.
type View struct {
	Title       string      `json:"title"`
	Overall     string      `json:"overall"`
	GeneratedAt time.Time   `json:"generated_at"`
	Checks      []CheckView `json:"checks"`
}

const barDays = 90

// Server renders the page from the shared history store.
type Server struct {
	title   string
	entries []Entry
	store   *history.Store
	now     func() time.Time
	logger  *slog.Logger
}

// NewServer creates the status page handler.
func NewServer(title string, entries []Entry, store *history.Store, logger *slog.Logger) *Server {
	return &Server{title: title, entries: entries, store: store, now: time.Now, logger: logger}
}

// Handler returns the HTTP routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.handlePage)
	mux.HandleFunc("GET /api/status", s.handleJSON)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if _, err := w.Write([]byte("ok\n")); err != nil {
			s.logger.Debug("write healthz", "err", err)
		}
	})
	return mux
}

// Build arma la vista a partir del historial en este instante.
func (s *Server) Build() View {
	now := s.now()
	view := View{Title: s.title, GeneratedAt: now.UTC()}
	down, unknown := 0, 0
	for _, e := range s.entries {
		st, ok := s.store.Get(e.Name)
		if !ok {
			st = history.CheckState{Status: "unknown"}
		}
		cv := CheckView{
			Name:      e.Name,
			Status:    st.Status,
			Uptime24h: uptimeOfSamples(st.Samples),
			Uptime30d: st.Uptime(30, now),
			Uptime90d: st.Uptime(barDays, now),
			Days:      dayBars(st.Days, now),
			Sparkline: sparkline(st.Samples, 160, 32),
		}
		cv.AvgLatencyMS = avgLatency(st.Samples)
		if n := len(st.Samples); n > 0 {
			cv.LastCheckedAt = st.Samples[n-1].At.UTC().Format(time.RFC3339)
		}
		switch st.Status {
		case "down":
			down++
		case "unknown":
			unknown++
		}
		view.Checks = append(view.Checks, cv)
	}
	switch {
	case len(view.Checks) == 0:
		view.Overall = "No public checks"
	case down == len(view.Checks):
		view.Overall = "Major outage"
	case down > 0:
		view.Overall = "Partial outage"
	case unknown > 0:
		view.Overall = "Collecting data"
	default:
		view.Overall = "All systems operational"
	}
	return view
}

func (s *Server) handlePage(w http.ResponseWriter, _ *http.Request) {
	// Se pinta a un búfer primero: si la plantilla falla, se devuelve un 500 limpio y no media página.
	var buf bytes.Buffer
	if err := page.Execute(&buf, s.Build()); err != nil {
		s.logger.Error("render status page", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if _, err := buf.WriteTo(w); err != nil {
		s.logger.Debug("write status page", "err", err)
	}
}

func (s *Server) handleJSON(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if err := json.NewEncoder(w).Encode(s.Build()); err != nil {
		s.logger.Debug("write status json", "err", err)
	}
}

func uptimeOfSamples(samples []history.Sample) float64 {
	if len(samples) == 0 {
		return 1
	}
	ok := 0
	for _, sm := range samples {
		if sm.OK {
			ok++
		}
	}
	return float64(ok) / float64(len(samples))
}

func avgLatency(samples []history.Sample) float64 {
	total, n := 0.0, 0
	for _, sm := range samples {
		if sm.OK {
			total += sm.LatencyMS
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return total / float64(n)
}

// dayBars devuelve siempre 90 celdas, de la más antigua a hoy, con hueco donde no hay datos.
func dayBars(days []history.Day, now time.Time) []DayBar {
	byDate := make(map[string]history.Day, len(days))
	for _, d := range days {
		byDate[d.Date] = d
	}
	bars := make([]DayBar, 0, barDays)
	for i := barDays - 1; i >= 0; i-- {
		date := now.UTC().AddDate(0, 0, -i).Format("2006-01-02")
		d, ok := byDate[date]
		bar := DayBar{Date: date, Empty: !ok || d.Total == 0, Uptime: 1}
		if !bar.Empty {
			bar.Uptime = d.Uptime()
		}
		bar.Level = level(bar)
		bars = append(bars, bar)
	}
	return bars
}

func level(b DayBar) string {
	switch {
	case b.Empty:
		return "none"
	case b.Uptime >= 0.999:
		return "ok"
	case b.Uptime >= 0.95:
		return "warn"
	default:
		return "bad"
	}
}

// sparkline dibuja la latencia de las últimas 24 h como puntos de una polilínea SVG, ya escalados.
func sparkline(samples []history.Sample, width, height float64) string {
	var pts []history.Sample
	for _, sm := range samples {
		if sm.OK {
			pts = append(pts, sm)
		}
	}
	if len(pts) < 2 {
		return ""
	}
	// Con miles de muestras basta una de cada tantas: el dibujo no gana nada y la página pesa menos.
	const maxPoints = 120
	if len(pts) > maxPoints {
		step := float64(len(pts)) / maxPoints
		reduced := make([]history.Sample, 0, maxPoints)
		for i := range maxPoints {
			reduced = append(reduced, pts[int(float64(i)*step)])
		}
		pts = append(reduced, pts[len(pts)-1])
	}
	hi := 0.0
	for _, p := range pts {
		hi = max(hi, p.LatencyMS)
	}
	if hi == 0 {
		hi = 1
	}
	var b strings.Builder
	for i, p := range pts {
		x := float64(i) / float64(len(pts)-1) * width
		y := height - (p.LatencyMS/hi)*(height-2) - 1
		if i > 0 {
			b.WriteByte(' ')
		}
		fmt.Fprintf(&b, "%.1f,%.1f", x, y)
	}
	return b.String()
}
