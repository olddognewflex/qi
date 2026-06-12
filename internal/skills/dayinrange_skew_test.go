package skills

import (
	"testing"
	"time"
)

// TestDayInRange_TimezoneSkew reproduces the bug: CompletedAt is UTC midnight
// (from ParseTaskLine) while the window bounds come from time.Now() (Local).
// A task completed ON the start day must be included regardless of zone.
func TestDayInRange_TimezoneSkew(t *testing.T) {
	west := time.FixedZone("UTC-7", -7*3600)
	// Window start/end as the skill builds them: Local midnights.
	end := time.Date(2026, 6, 11, 0, 0, 0, 0, west)
	start := end.AddDate(0, 0, -6) // 2026-06-05
	// Task completed on the start day, stored as UTC midnight (as ParseTaskLine yields).
	done := time.Date(2026, 6, 5, 0, 0, 0, 0, time.UTC)
	if !dayInRange(done, start, end) {
		t.Fatalf("task completed on start day (UTC midnight) wrongly excluded across tz offset")
	}
}
