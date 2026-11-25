package status

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/juanmmm21/infraping/internal/history"
)

// Geometría del gráfico de latencia, en unidades del viewBox del SVG.
const (
	chartWidth  = 720
	chartHeight = 220
	plotLeft    = 48
	plotRight   = chartWidth - 12
	plotTop     = 12
	plotBottom  = chartHeight - 28
	chartSpan   = 24 * time.Hour
	yTicks      = 4
)

// Tick is one axis label.
type Tick struct {
	Pos   float64
	Label string
}

// Band is a run of consecutive failed checks, as a horizontal span of the plot.
type Band struct {
	X, W float64
}

// Chart is the latency chart of the last 24 hours, already projected to SVG coordinates.
type Chart struct {
	Width, Height int
	Left, Right   float64
	Top, Bottom   float64
	Line          string
	Area          string
	Failures      []Band
	XTicks        []Tick
	YTicks        []Tick
	Empty         bool
}

// niceCeil sube al siguiente 1, 2 o 5 por potencia de diez: los ejes se leen mejor así.
func niceCeil(v float64) float64 {
	if v <= 0 {
		return 1
	}
	exp := math.Pow(10, math.Floor(math.Log10(v)))
	for _, m := range []float64{1, 2, 5, 10} {
		if m*exp >= v {
			return m * exp
		}
	}
	return 10 * exp
}

func formatMS(v float64) string {
	if v >= 1000 {
		return strings.TrimSuffix(fmt.Sprintf("%.1f", v/1000), ".0") + " s"
	}
	return fmt.Sprintf("%.0f ms", v)
}

// latencyChart proyecta las muestras a coordenadas. Cada fallo corta la línea y deja una marca
// roja, para que una caída no se dibuje como si la latencia hubiera bajado a cero.
func latencyChart(samples []history.Sample, now time.Time) Chart {
	c := Chart{
		Width: chartWidth, Height: chartHeight,
		Left: plotLeft, Right: plotRight, Top: plotTop, Bottom: plotBottom,
	}
	start := now.Add(-chartSpan)
	x := func(t time.Time) float64 {
		frac := float64(t.Sub(start)) / float64(chartSpan)
		return plotLeft + max(0, min(1, frac))*(plotRight-plotLeft)
	}

	hi := 0.0
	ok := 0
	for _, sm := range samples {
		if sm.OK {
			hi = max(hi, sm.LatencyMS)
			ok++
		}
	}
	top := niceCeil(hi * 1.1)
	y := func(ms float64) float64 { return plotBottom - ms/top*(plotBottom-plotTop) }

	for i := 0; i <= yTicks; i++ {
		v := top * float64(i) / yTicks
		c.YTicks = append(c.YTicks, Tick{Pos: y(v), Label: formatMS(v)})
	}
	// Una marca cada 6 horas en punto (UTC), más «now» al final.
	first := start.UTC().Truncate(6 * time.Hour).Add(6 * time.Hour)
	// Sin marcas en las 3 últimas horas: pisarían a «now» en pantallas estrechas.
	for t := first; t.Before(now.Add(-3 * time.Hour)); t = t.Add(6 * time.Hour) {
		c.XTicks = append(c.XTicks, Tick{Pos: x(t), Label: t.UTC().Format("15:04")})
	}
	c.XTicks = append(c.XTicks, Tick{Pos: plotRight, Label: "now"})

	if ok < 2 {
		c.Empty = true
	}
	// Media muestra a cada lado, para que una caída de una sola sonda también se vea.
	half := 0.0
	if n := len(samples); n > 1 {
		span := samples[n-1].At.Sub(samples[0].At) / time.Duration(n-1)
		half = max(1, float64(span)/float64(chartSpan)*(plotRight-plotLeft)/2)
	}
	addFailure := func(px float64) {
		left, right := max(plotLeft, px-half), min(plotRight, px+half)
		if n := len(c.Failures); n > 0 && c.Failures[n-1].X+c.Failures[n-1].W >= left-0.5 {
			c.Failures[n-1].W = right - c.Failures[n-1].X
			return
		}
		c.Failures = append(c.Failures, Band{X: left, W: right - left})
	}
	var line, area strings.Builder
	segStart, lastX := -1.0, 0.0
	closeSeg := func() {
		if segStart >= 0 {
			fmt.Fprintf(&area, " L%.1f,%.1f L%.1f,%.1f Z", lastX, float64(plotBottom), segStart, float64(plotBottom))
		}
		segStart = -1
	}
	for _, sm := range samples {
		if sm.At.Before(start) {
			continue
		}
		px := x(sm.At)
		if !sm.OK {
			addFailure(px)
			closeSeg()
			continue
		}
		py := y(sm.LatencyMS)
		if segStart < 0 {
			fmt.Fprintf(&line, "M%.1f,%.1f", px, py)
			fmt.Fprintf(&area, " M%.1f,%.1f L%.1f,%.1f", px, float64(plotBottom), px, py)
			segStart = px
		} else {
			fmt.Fprintf(&line, " L%.1f,%.1f", px, py)
			fmt.Fprintf(&area, " L%.1f,%.1f", px, py)
		}
		lastX = px
	}
	closeSeg()
	c.Line = line.String()
	c.Area = strings.TrimSpace(area.String())
	return c
}
