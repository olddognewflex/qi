package commands

import (
	"testing"
	"time"
)

func TestParseDailyDate(t *testing.T) {
	fallback := time.Date(2026, 6, 2, 9, 30, 0, 0, time.Local)

	t.Run("no args returns fallback", func(t *testing.T) {
		got, err := parseDailyDate(nil, fallback)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !got.Equal(fallback) {
			t.Fatalf("got %v, want fallback %v", got, fallback)
		}
	})

	t.Run("valid date parses in local zone", func(t *testing.T) {
		got, err := parseDailyDate([]string{"2026-05-28"}, fallback)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := time.Date(2026, 5, 28, 0, 0, 0, 0, time.Local)
		if !got.Equal(want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	})

	for _, bad := range []string{"", "2026-13-01", "05-28-2026", "2026/05/28", "today", "2026-5-8"} {
		t.Run("invalid date "+bad, func(t *testing.T) {
			if _, err := parseDailyDate([]string{bad}, fallback); err == nil {
				t.Fatalf("expected error for %q, got nil", bad)
			}
		})
	}
}
