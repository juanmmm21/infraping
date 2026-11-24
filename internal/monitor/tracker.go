// Package monitor decide cuándo una comprobación cae o vuelve, y lanza las sondas a su ritmo.
package monitor

import (
	"time"

	"github.com/juanmmm21/infraping/internal/check"
)

// Status is the confirmed state of a check.
type Status string

const (
	StatusUnknown Status = "unknown"
	StatusUp      Status = "up"
	StatusDown    Status = "down"
)

// Event is a confirmed transition worth telling someone about.
type Event struct {
	Check  string
	From   Status
	To     Status
	Result check.Result
	// Downtime is how long the check was down; only set when it comes back up.
	Downtime time.Duration
}

// Tracker convierte resultados sueltos en estados confirmados: un fallo aislado no despierta a nadie.
type Tracker struct {
	name              string
	failureThreshold  int
	recoveryThreshold int
	status            Status
	failStreak        int
	okStreak          int
	downSince         time.Time
}

// NewTracker creates a tracker in the unknown state.
func NewTracker(name string, failureThreshold, recoveryThreshold int) *Tracker {
	return &Tracker{
		name:              name,
		failureThreshold:  max(failureThreshold, 1),
		recoveryThreshold: max(recoveryThreshold, 1),
		status:            StatusUnknown,
	}
}

// Status returns the current confirmed state.
func (t *Tracker) Status() Status { return t.status }

// DownSince returns when the current outage started (zero if not down).
func (t *Tracker) DownSince() time.Time {
	if t.status != StatusDown {
		return time.Time{}
	}
	return t.downSince
}

// Observe registra un resultado y devuelve el evento si confirma un cambio que hay que avisar.
// El primer «up» tras arrancar no se avisa: nadie necesita saber que todo sigue bien.
func (t *Tracker) Observe(res check.Result) (Event, bool) {
	if res.OK {
		t.failStreak = 0
		t.okStreak++
		switch t.status {
		case StatusUnknown:
			t.status = StatusUp
			return Event{}, false
		case StatusDown:
			if t.okStreak < t.recoveryThreshold {
				return Event{}, false
			}
			ev := Event{Check: t.name, From: StatusDown, To: StatusUp, Result: res, Downtime: res.CheckedAt.Sub(t.downSince)}
			t.status = StatusUp
			t.downSince = time.Time{}
			return ev, true
		default:
			return Event{}, false
		}
	}

	t.okStreak = 0
	t.failStreak++
	if t.failStreak == 1 && t.status != StatusDown {
		// La caída se data en el primer fallo, no en el que la confirma.
		t.downSince = res.CheckedAt
	}
	if t.status == StatusDown || t.failStreak < t.failureThreshold {
		return Event{}, false
	}
	ev := Event{Check: t.name, From: t.status, To: StatusDown, Result: res}
	t.status = StatusDown
	return ev, true
}

// Restore devuelve un tracker al estado guardado antes de reiniciar, para no repetir ni perder avisos.
func (t *Tracker) Restore(status Status, downSince time.Time) {
	t.status = status
	if status == StatusDown {
		t.downSince = downSince
		t.failStreak = t.failureThreshold
	}
}
