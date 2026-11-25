// Package status sirve la página pública de estado, el detalle de cada servicio, su versión en
// JSON y un /healthz.
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
	"sync"
	"time"

	"github.com/juanmmm21/infraping/internal/history"
)

//go:embed templates/*.tmpl templates/style.css
var assets embed.FS

var pages = template.Must(template.New("").Funcs(template.FuncMap{
	"pct":      formatPercent,
	"ms":       func(v float64) string { return fmt.Sprintf("%.0f ms", v) },
	"css":      func() template.CSS { return template.CSS(styleSheet) },
	"stamp":    func(t time.Time) string { return t.UTC().Format("2006-01-02 15:04 UTC") },
	"iso":      func(t time.Time) string { return t.UTC().Format(time.RFC3339) },
	"label":    statusLabel,
	"barLabel": barLabel,
	"f1":       func(v float64) string { return fmt.Sprintf("%.1f", v) },
	"add":      func(a, b float64) float64 { return a + b },
	"sub":      func(a, b float64) float64 { return a - b },
	"half":     func(v float64) float64 { return v / 2 },
	"grade":    grade,
	"dict":     dict,
}).ParseFS(assets, "templates/*.tmpl"))

var styleSheet = mustRead("templates/style.css")

func mustRead(name string) string {
	data, err := assets.ReadFile(name)
	if err != nil {
		// Solo puede fallar si el embed no incluye el fichero, que es un error de compilación encubierto.
		panic(fmt.Sprintf("embedded asset %s: %v", name, err))
	}
	return string(data)
}

// formatPercent no redondea a 100 % lo que no lo es: 99,996 % se ve como 99.99 %.
func formatPercent(v float64) string {
	if v >= 1 {
		return "100%"
	}
	return fmt.Sprintf("%.2f%%", float64(int(v*10000))/100)
}

func statusLabel(status string) string {
	switch status {
	case "up":
		return "Operational"
	case "down":
		return "Down"
	default:
		return "No data yet"
	}
}

// grade colorea un porcentaje de disponibilidad: verde casi perfecto, ámbar regular, rojo malo.
func grade(v float64) string {
	switch {
	case v >= 0.999:
		return "good"
	case v >= 0.98:
		return "meh"
	default:
		return "poor"
	}
}

// dict deja pasar varios valores a una subplantilla, que en html/template solo recibe uno.
func dict(pairs ...any) (map[string]any, error) {
	if len(pairs)%2 != 0 {
		return nil, fmt.Errorf("dict needs key/value pairs, got %d values", len(pairs))
	}
	out := make(map[string]any, len(pairs)/2)
	for i := 0; i < len(pairs); i += 2 {
		key, ok := pairs[i].(string)
		if !ok {
			return nil, fmt.Errorf("dict key %v is not a string", pairs[i])
		}
		out[key] = pairs[i+1]
	}
	return out, nil
}

func barLabel(b DayBar) string {
	if b.Empty {
		return b.Date + ": no data"
	}
	return b.Date + ": " + formatPercent(b.Uptime) + " uptime"
}

// Server renders the pages from the shared history store.
type Server struct {
	// mu protege title y entries, que cambian al recargar la configuración.
	mu      sync.RWMutex
	title   string
	entries []pageEntry
	store   *history.Store
	now     func() time.Time
	logger  *slog.Logger
}

// NewServer creates the status page handler.
func NewServer(title string, entries []Entry, store *history.Store, logger *slog.Logger) *Server {
	return &Server{title: title, entries: assignIDs(entries), store: store, now: time.Now, logger: logger}
}

// Update cambia el título y las comprobaciones públicas; se usa al recargar la configuración.
func (s *Server) Update(title string, entries []Entry) {
	ids := assignIDs(entries)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.title = title
	s.entries = ids
}

func (s *Server) snapshot() (string, []pageEntry) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.title, s.entries
}

// Handler returns the HTTP routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.handlePage)
	mux.HandleFunc("GET /checks/{id}", s.handleDetail)
	mux.HandleFunc("GET /api/status", s.handleJSON)
	mux.HandleFunc("GET /api/checks/{id}", s.handleDetailJSON)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if _, err := w.Write([]byte("ok\n")); err != nil {
			s.logger.Debug("write healthz", "err", err)
		}
	})
	return mux
}

func (s *Server) state(name string) history.CheckState {
	st, ok := s.store.Get(name)
	if !ok {
		return history.CheckState{Status: "unknown"}
	}
	return st
}

// Build arma la portada a partir del historial en este instante.
func (s *Server) Build() View {
	title, entries := s.snapshot()
	now := s.now()
	view := View{Title: title, GeneratedAt: now.UTC(), Checks: []CheckView{}}
	groups := make([][]IncidentView, 0, len(entries))
	for _, e := range entries {
		st := s.state(e.Name)
		cv := buildCheckView(e, st, now)
		view.Summary.Total++
		switch cv.Status {
		case "up":
			view.Summary.Up++
		case "down":
			view.Summary.Down++
		default:
			view.Summary.Unknown++
		}
		view.Checks = append(view.Checks, cv)
		groups = append(groups, incidentViews(e, st.Incidents, now))
	}
	view.Overall, view.Level = overall(view.Summary)
	view.RecentIncidents = latest(groups, recentIncidents)
	return view
}

// BuildDetail arma la página de una comprobación pública; ok es falso si no existe o es privada.
func (s *Server) BuildDetail(id string) (Detail, bool) {
	title, entries := s.snapshot()
	for _, e := range entries {
		if e.ID == id {
			return buildDetail(title, e, s.state(e.Name), s.now()), true
		}
	}
	return Detail{}, false
}

// render pinta a un búfer primero: si la plantilla falla, se devuelve un 500 limpio y no media página.
func (s *Server) render(w http.ResponseWriter, status int, name string, data any) {
	var buf bytes.Buffer
	if err := pages.ExecuteTemplate(&buf, name, data); err != nil {
		s.logger.Error("render status page", "page", name, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if _, err := buf.WriteTo(w); err != nil {
		s.logger.Debug("write status page", "err", err)
	}
}

func (s *Server) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		s.logger.Debug("write status json", "err", err)
	}
}

func (s *Server) handlePage(w http.ResponseWriter, _ *http.Request) {
	s.render(w, http.StatusOK, "index.html.tmpl", s.Build())
}

func (s *Server) handleDetail(w http.ResponseWriter, r *http.Request) {
	detail, ok := s.BuildDetail(r.PathValue("id"))
	if !ok {
		title, _ := s.snapshot()
		s.render(w, http.StatusNotFound, "notfound.html.tmpl", map[string]any{"Title": title, "Now": s.now()})
		return
	}
	s.render(w, http.StatusOK, "detail.html.tmpl", detail)
}

func (s *Server) handleJSON(w http.ResponseWriter, _ *http.Request) {
	s.writeJSON(w, http.StatusOK, s.Build())
}

func (s *Server) handleDetailJSON(w http.ResponseWriter, r *http.Request) {
	detail, ok := s.BuildDetail(r.PathValue("id"))
	if !ok {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown check"})
		return
	}
	s.writeJSON(w, http.StatusOK, detail)
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
