package monitor

import (
	"testing"
	"time"

	"github.com/juanmmm21/infraping/internal/check"
)

var t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func at(min int, ok bool) check.Result {
	return check.Result{OK: ok, CheckedAt: t0.Add(time.Duration(min) * time.Minute)}
}

func TestFirstUpIsSilent(t *testing.T) {
	tr := NewTracker("api", 2, 1)
	if _, fired := tr.Observe(at(0, true)); fired {
		t.Fatal("first up must not notify")
	}
	if tr.Status() != StatusUp {
		t.Fatalf("status = %s", tr.Status())
	}
}

func TestSingleFailureIsIgnored(t *testing.T) {
	tr := NewTracker("api", 2, 1)
	tr.Observe(at(0, true))
	if _, fired := tr.Observe(at(1, false)); fired {
		t.Fatal("one failure below threshold must not notify")
	}
	if _, fired := tr.Observe(at(2, true)); fired {
		t.Fatal("recovering from a blip must not notify")
	}
	if tr.Status() != StatusUp || !tr.DownSince().IsZero() {
		t.Fatalf("status = %s since %s", tr.Status(), tr.DownSince())
	}
}

func TestDownAndRecovery(t *testing.T) {
	tr := NewTracker("api", 2, 2)
	tr.Observe(at(0, true))
	tr.Observe(at(1, false))
	ev, fired := tr.Observe(at(2, false))
	if !fired || ev.From != StatusUp || ev.To != StatusDown {
		t.Fatalf("want up->down, got %+v fired=%v", ev, fired)
	}
	if !tr.DownSince().Equal(t0.Add(time.Minute)) {
		t.Errorf("outage must start at the first failure, got %s", tr.DownSince())
	}
	if _, fired := tr.Observe(at(3, false)); fired {
		t.Fatal("staying down must not repeat the alert")
	}
	if _, fired := tr.Observe(at(4, true)); fired {
		t.Fatal("one success below recovery threshold must not notify")
	}
	ev, fired = tr.Observe(at(5, true))
	if !fired || ev.To != StatusUp || ev.Downtime != 4*time.Minute {
		t.Fatalf("want recovery after 4m, got %+v fired=%v", ev, fired)
	}
}

func TestDownFromUnknown(t *testing.T) {
	tr := NewTracker("api", 1, 1)
	ev, fired := tr.Observe(at(0, false))
	if !fired || ev.From != StatusUnknown || ev.To != StatusDown {
		t.Fatalf("want unknown->down, got %+v", ev)
	}
}

func TestRestoreDown(t *testing.T) {
	tr := NewTracker("api", 3, 1)
	tr.Restore(StatusDown, t0)
	if _, fired := tr.Observe(at(10, false)); fired {
		t.Fatal("restored outage must not alert again")
	}
	ev, fired := tr.Observe(at(20, true))
	if !fired || ev.Downtime != 20*time.Minute {
		t.Fatalf("want recovery with downtime from restored start, got %+v", ev)
	}
}
