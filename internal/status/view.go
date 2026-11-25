package status

import (
	"cmp"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/juanmmm21/infraping/internal/history"
)

const (
	barDays = 90
	// En la portada basta con lo último; el detalle de cada servicio tiene su lista entera.
	recentIncidents = 5
)

// Entry is a check as the status page knows it. Only public checks are passed in.
type Entry struct {
	Name     string
	Kind     string
	Interval time.Duration
}

// page es una entrada ya con su identificador para la URL.
type pageEntry struct {
	Entry
	ID string
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
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Kind          string   `json:"kind,omitempty"`
	Status        string   `json:"status"`
	Uptime24h     float64  `json:"uptime_24h"`
	Uptime30d     float64  `json:"uptime_30d"`
	Uptime90d     float64  `json:"uptime_90d"`
	AvgLatencyMS  float64  `json:"avg_latency_ms"`
	LastCheckedAt string   `json:"last_checked_at,omitempty"`
	Days          []DayBar `json:"days"`
	Sparkline     string   `json:"-"`
}

// IncidentView is one outage as the page shows it: when and for how long, never why.
type IncidentView struct {
	Check           string     `json:"check"`
	CheckID         string     `json:"check_id"`
	Start           time.Time  `json:"start"`
	End             *time.Time `json:"end,omitempty"`
	Ongoing         bool       `json:"ongoing"`
	DurationSeconds int64      `json:"duration_seconds"`
	Duration        string     `json:"-"`
}

// Summary counts checks by state.
type Summary struct {
	Total   int `json:"total"`
	Up      int `json:"up"`
	Down    int `json:"down"`
	Unknown int `json:"unknown"`
}

// View is the whole front page.
type View struct {
	Title           string         `json:"title"`
	Overall         string         `json:"overall"`
	Level           string         `json:"-"`
	GeneratedAt     time.Time      `json:"generated_at"`
	Summary         Summary        `json:"summary"`
	Checks          []CheckView    `json:"checks"`
	RecentIncidents []IncidentView `json:"recent_incidents"`
}

// Detail is the page of a single check.
type Detail struct {
	Title           string         `json:"title"`
	GeneratedAt     time.Time      `json:"generated_at"`
	Check           CheckView      `json:"check"`
	IntervalSeconds int64          `json:"interval_seconds,omitempty"`
	Uptime7d        float64        `json:"uptime_7d"`
	Checks24h       int            `json:"checks_24h"`
	Failed24h       int            `json:"failed_24h"`
	P50MS           float64        `json:"p50_latency_ms"`
	P95MS           float64        `json:"p95_latency_ms"`
	MaxMS           float64        `json:"max_latency_ms"`
	Latency         []LatencyPoint `json:"latency_24h"`
	Incidents       []IncidentView `json:"incidents"`
	Chart           Chart          `json:"-"`
	Interval        string         `json:"-"`
}

// LatencyPoint is one probe of the last 24 hours.
type LatencyPoint struct {
	At        time.Time `json:"at"`
	OK        bool      `json:"ok"`
	LatencyMS float64   `json:"latency_ms"`
}

// slugify convierte un nombre en un trozo de URL legible; lo raro se queda en guiones.
func slugify(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		if r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r)) {
			b.WriteRune(r)
			dash = false
			continue
		}
		if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	slug := strings.TrimRight(b.String(), "-")
	if slug == "" {
		return "check"
	}
	return slug
}

// assignIDs da a cada comprobación un identificador único: dos nombres pueden dar el mismo slug.
func assignIDs(entries []Entry) []pageEntry {
	used := make(map[string]bool, len(entries))
	out := make([]pageEntry, 0, len(entries))
	for _, e := range entries {
		base := slugify(e.Name)
		id := base
		for n := 2; used[id]; n++ {
			id = fmt.Sprintf("%s-%d", base, n)
		}
		used[id] = true
		out = append(out, pageEntry{Entry: e, ID: id})
	}
	return out
}

func buildCheckView(e pageEntry, st history.CheckState, now time.Time) CheckView {
	cv := CheckView{
		ID:           e.ID,
		Name:         e.Name,
		Kind:         e.Kind,
		Status:       st.Status,
		Uptime24h:    uptimeOfSamples(st.Samples),
		Uptime30d:    st.Uptime(30, now),
		Uptime90d:    st.Uptime(barDays, now),
		AvgLatencyMS: avgLatency(st.Samples),
		Days:         dayBars(st.Days, now),
		Sparkline:    sparkline(st.Samples, 120, 28),
	}
	if n := len(st.Samples); n > 0 {
		cv.LastCheckedAt = st.Samples[n-1].At.UTC().Format(time.RFC3339)
	}
	return cv
}

func incidentViews(e pageEntry, incidents []history.Incident, now time.Time) []IncidentView {
	out := make([]IncidentView, 0, len(incidents))
	for _, inc := range incidents {
		d := inc.Duration(now)
		iv := IncidentView{
			Check: e.Name, CheckID: e.ID, Start: inc.Start.UTC(), Ongoing: inc.Ongoing(),
			DurationSeconds: int64(d.Seconds()), Duration: humanDuration(d),
		}
		if !inc.Ongoing() {
			end := inc.End.UTC()
			iv.End = &end
		}
		out = append(out, iv)
	}
	// Lo más reciente primero, que es lo que interesa al abrir la página.
	slices.SortFunc(out, func(a, b IncidentView) int { return b.Start.Compare(a.Start) })
	return out
}

func overall(s Summary) (text, level string) {
	switch {
	case s.Total == 0:
		return "No public checks", "none"
	case s.Down == s.Total:
		return "Major outage", "bad"
	case s.Down > 0:
		return "Partial outage", "warn"
	case s.Unknown > 0:
		return "Collecting data", "none"
	default:
		return "All systems operational", "ok"
	}
}

// percentile usa el rango más cercano sobre valores ya ordenados.
func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	rank := int(math.Ceil(p/100*float64(len(sorted)))) - 1
	return sorted[max(0, min(rank, len(sorted)-1))]
}

func buildDetail(title string, e pageEntry, st history.CheckState, now time.Time) Detail {
	d := Detail{
		Title:       title,
		GeneratedAt: now.UTC(),
		Check:       buildCheckView(e, st, now),
		Uptime7d:    st.Uptime(7, now),
		Incidents:   incidentViews(e, st.Incidents, now),
		Interval:    humanDuration(e.Interval),
	}
	if e.Interval > 0 {
		d.IntervalSeconds = int64(e.Interval.Seconds())
	}
	var okLatencies []float64
	for _, sm := range st.Samples {
		d.Checks24h++
		d.Latency = append(d.Latency, LatencyPoint{At: sm.At.UTC(), OK: sm.OK, LatencyMS: sm.LatencyMS})
		if !sm.OK {
			d.Failed24h++
			continue
		}
		okLatencies = append(okLatencies, sm.LatencyMS)
	}
	slices.Sort(okLatencies)
	d.P50MS = percentile(okLatencies, 50)
	d.P95MS = percentile(okLatencies, 95)
	if n := len(okLatencies); n > 0 {
		d.MaxMS = okLatencies[n-1]
	}
	d.Chart = latencyChart(st.Samples, now)
	return d
}

// humanDuration es la duración en palabras cortas: «3h 12m», «45s», «2 days».
func humanDuration(d time.Duration) string {
	d = d.Round(time.Second)
	switch {
	case d <= 0:
		return "0s"
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		if s := int(d.Seconds()) % 60; s != 0 {
			return fmt.Sprintf("%dm %ds", int(d.Minutes()), s)
		}
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		if m := int(d.Minutes()) % 60; m != 0 {
			return fmt.Sprintf("%dh %dm", int(d.Hours()), m)
		}
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%d days", int(d.Hours())/24)
	}
}

// latest junta las incidencias de todas las comprobaciones y se queda con las n más recientes.
func latest(groups [][]IncidentView, n int) []IncidentView {
	var all []IncidentView
	for _, g := range groups {
		all = append(all, g...)
	}
	slices.SortFunc(all, func(a, b IncidentView) int {
		return cmp.Or(b.Start.Compare(a.Start), strings.Compare(a.CheckID, b.CheckID))
	})
	if len(all) > n {
		all = all[:n]
	}
	return all
}
