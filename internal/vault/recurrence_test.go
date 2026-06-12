package vault

import (
	"testing"
	"time"
)

func date(y int, m time.Month, d int) *time.Time {
	t := time.Date(y, m, d, 0, 0, 0, 0, time.Local)
	return &t
}

func fmtDate(t *time.Time) string {
	if t == nil {
		return "<nil>"
	}
	return t.Format("2006-01-02")
}

func TestNextRecurrence_Units(t *testing.T) {
	cases := []struct {
		rule string
		due  *time.Time
		want string
	}{
		{"every day", date(2026, 6, 11), "2026-06-12"},
		{"every week", date(2026, 6, 11), "2026-06-18"},
		{"every month", date(2026, 6, 11), "2026-07-11"},
		{"every year", date(2026, 6, 11), "2027-06-11"},
		{"every 2 weeks", date(2026, 6, 11), "2026-06-25"},
		{"every 3 days", date(2026, 6, 11), "2026-06-14"},
	}
	for _, c := range cases {
		gotDue, _, ok := NextRecurrence(c.rule, c.due, nil, time.Now())
		if !ok {
			t.Fatalf("%q: expected ok", c.rule)
		}
		if fmtDate(gotDue) != c.want {
			t.Fatalf("%q: got %s want %s", c.rule, fmtDate(gotDue), c.want)
		}
	}
}

func TestNextRecurrence_CaseInsensitive(t *testing.T) {
	gotDue, _, ok := NextRecurrence("Every Week", date(2026, 6, 11), nil, time.Now())
	if !ok || fmtDate(gotDue) != "2026-06-18" {
		t.Fatalf("case-insensitive failed: ok=%v due=%s", ok, fmtDate(gotDue))
	}
}

func TestNextRecurrence_WhenDone(t *testing.T) {
	// Old due is 2026-06-11 but task is completed late on 2026-06-20.
	// "when done" bases the next occurrence off the completion date, not the due.
	due := date(2026, 6, 11)
	completed := time.Date(2026, 6, 20, 14, 30, 0, 0, time.Local)
	gotDue, _, ok := NextRecurrence("every week when done", due, nil, completed)
	if !ok {
		t.Fatal("expected ok")
	}
	// newRef = dateOnly(2026-06-20) + 1 week = 2026-06-27; delta = 16 days.
	// due 2026-06-11 + 16 days = 2026-06-27.
	if fmtDate(gotDue) != "2026-06-27" {
		t.Fatalf("when done: got %s want 2026-06-27", fmtDate(gotDue))
	}
}

func TestNextRecurrence_BothDatesShiftBySameDelta(t *testing.T) {
	scheduled := date(2026, 6, 9)
	due := date(2026, 6, 11)
	gotDue, gotSched, ok := NextRecurrence("every week", due, scheduled, time.Now())
	if !ok {
		t.Fatal("expected ok")
	}
	if fmtDate(gotDue) != "2026-06-18" {
		t.Fatalf("due: got %s want 2026-06-18", fmtDate(gotDue))
	}
	if fmtDate(gotSched) != "2026-06-16" {
		t.Fatalf("scheduled: got %s want 2026-06-16", fmtDate(gotSched))
	}
	// Spacing preserved: both shifted by 7 days.
	if gotDue.Sub(*gotSched) != due.Sub(*scheduled) {
		t.Fatalf("scheduled/due spacing not preserved")
	}
}

func TestNextRecurrence_DueOnly(t *testing.T) {
	gotDue, gotSched, ok := NextRecurrence("every day", date(2026, 6, 11), nil, time.Now())
	if !ok {
		t.Fatal("expected ok")
	}
	if fmtDate(gotDue) != "2026-06-12" {
		t.Fatalf("due: got %s", fmtDate(gotDue))
	}
	if gotSched != nil {
		t.Fatalf("scheduled should stay nil, got %s", fmtDate(gotSched))
	}
}

func TestNextRecurrence_ScheduledOnly(t *testing.T) {
	gotDue, gotSched, ok := NextRecurrence("every day", nil, date(2026, 6, 11), time.Now())
	if !ok {
		t.Fatal("expected ok")
	}
	if gotDue != nil {
		t.Fatalf("due should stay nil, got %s", fmtDate(gotDue))
	}
	if fmtDate(gotSched) != "2026-06-12" {
		t.Fatalf("scheduled: got %s", fmtDate(gotSched))
	}
}

func TestNextRecurrence_BothNil(t *testing.T) {
	_, _, ok := NextRecurrence("every day", nil, nil, time.Now())
	if ok {
		t.Fatal("expected ok=false when both dates are nil")
	}
}

func TestNextRecurrence_Unsupported(t *testing.T) {
	for _, rule := range []string{"every weekday", "every week on monday", "garbage", "", "weekly", "every 0 days"} {
		_, _, ok := NextRecurrence(rule, date(2026, 6, 11), nil, time.Now())
		if ok {
			t.Fatalf("rule %q should be unsupported (ok=false)", rule)
		}
	}
}

// TestNextRecurrence_MonthBoundary documents Go's AddDate normalization: a Jan 31
// + 1 month overflows to Mar 3 (Feb has 28 days, the 3 extra days roll forward).
// We assert the actual AddDate behaviour rather than calendar-clamped behaviour.
func TestNextRecurrence_MonthBoundary(t *testing.T) {
	gotDue, _, ok := NextRecurrence("every month", date(2026, 1, 31), nil, time.Now())
	if !ok {
		t.Fatal("expected ok")
	}
	// AddDate(0, 1, 0) on 2026-01-31 → 2026-03-03 (Go normalizes the overflow).
	if fmtDate(gotDue) != "2026-03-03" {
		t.Fatalf("month boundary: got %s want 2026-03-03 (AddDate normalization)", fmtDate(gotDue))
	}
}
