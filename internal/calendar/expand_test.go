package calendar

import (
	"sort"
	"strings"
	"testing"
	"time"

	ical "github.com/emersion/go-ical"
	"qi/internal/domain"
)

func utc(y int, m time.Month, d, h, min int) time.Time {
	return time.Date(y, m, d, h, min, 0, 0, time.UTC)
}

// decodeICS parses an inline calendar object. Test fixtures are written with plain
// newlines for readability; iCalendar is CRLF-delimited.
func decodeICS(t *testing.T, s string) *ical.Calendar {
	t.Helper()
	cal, err := ical.NewDecoder(strings.NewReader(strings.ReplaceAll(s, "\n", "\r\n"))).Decode()
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return cal
}

// wantEvent is an expected occurrence, keyed on the instant (RFC3339 in UTC) so the
// assertion is independent of the machine's local zone.
type wantEvent struct {
	start string
	title string
}

func TestExpandCalendarEvents(t *testing.T) {
	tests := []struct {
		name string
		ics  string
		from time.Time
		to   time.Time
		want []wantEvent
	}{
		{
			// Regression guard: grouping by UID must not merge components that have no
			// UID at all, or every UID-less event after the first disappears.
			name: "uid-less components never merge",
			ics: `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//qi//test//EN
BEGIN:VEVENT
DTSTAMP:20260401T090000Z
DTSTART:20260406T090000Z
DTEND:20260406T100000Z
SUMMARY:First no-uid
END:VEVENT
BEGIN:VEVENT
DTSTAMP:20260401T090000Z
DTSTART:20260406T130000Z
DTEND:20260406T140000Z
SUMMARY:Second no-uid
END:VEVENT
END:VCALENDAR`,
			from: utc(2026, 4, 6, 0, 0),
			to:   utc(2026, 4, 7, 0, 0),
			want: []wantEvent{
				{"2026-04-06T09:00:00Z", "First no-uid"},
				{"2026-04-06T13:00:00Z", "Second no-uid"},
			},
		},
		{
			// A second base for one UID is not spec-legal, but dropping it loses an
			// event; keep it as its own logical event.
			name: "second base for one uid is kept",
			ics: `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//qi//test//EN
BEGIN:VEVENT
UID:dup-1
DTSTAMP:20260401T090000Z
DTSTART:20260406T090000Z
DTEND:20260406T100000Z
SUMMARY:First base
END:VEVENT
BEGIN:VEVENT
UID:dup-1
DTSTAMP:20260401T090000Z
DTSTART:20260406T150000Z
DTEND:20260406T160000Z
SUMMARY:Second base
END:VEVENT
END:VCALENDAR`,
			from: utc(2026, 4, 6, 0, 0),
			to:   utc(2026, 4, 7, 0, 0),
			want: []wantEvent{
				{"2026-04-06T09:00:00Z", "First base"},
				{"2026-04-06T15:00:00Z", "Second base"},
			},
		},
		{
			// ov1 moves the 04-06 slot onto 04-07T10:00; ov2 then names the 04-07 slot.
			// Matching on wall-clock start would let ov2 delete ov1's relocated event.
			name: "chained overrides keep every instance",
			ics: `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//qi//test//EN
BEGIN:VEVENT
UID:chain-1
DTSTAMP:20260401T090000Z
DTSTART:20260406T100000Z
DTEND:20260406T110000Z
RRULE:FREQ=DAILY;COUNT=3
SUMMARY:Chain base
END:VEVENT
BEGIN:VEVENT
UID:chain-1
RECURRENCE-ID:20260406T100000Z
DTSTAMP:20260401T090000Z
DTSTART:20260407T100000Z
DTEND:20260407T110000Z
SUMMARY:Day1 moved onto Day2 slot
END:VEVENT
BEGIN:VEVENT
UID:chain-1
RECURRENCE-ID:20260407T100000Z
DTSTAMP:20260401T090000Z
DTSTART:20260408T150000Z
DTEND:20260408T160000Z
SUMMARY:Day2 moved to Day3 15:00
END:VEVENT
END:VCALENDAR`,
			from: utc(2026, 4, 6, 0, 0),
			to:   utc(2026, 4, 10, 0, 0),
			want: []wantEvent{
				{"2026-04-07T10:00:00Z", "Day1 moved onto Day2 slot"},
				{"2026-04-08T10:00:00Z", "Chain base"},
				{"2026-04-08T15:00:00Z", "Day2 moved to Day3 15:00"},
			},
		},
		{
			// Apple/iCloud's "change this and all future events".
			name: "range=thisandfuture shifts the named slot and every later one",
			ics: `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//qi//test//EN
BEGIN:VEVENT
UID:taf-1
DTSTAMP:20260401T090000Z
DTSTART:20260406T140000Z
DTEND:20260406T150000Z
RRULE:FREQ=DAILY;COUNT=4
SUMMARY:Standup
END:VEVENT
BEGIN:VEVENT
UID:taf-1
RECURRENCE-ID;RANGE=THISANDFUTURE:20260407T140000Z
DTSTAMP:20260401T090000Z
DTSTART:20260407T160000Z
DTEND:20260407T170000Z
SUMMARY:Standup (moved)
END:VEVENT
END:VCALENDAR`,
			from: utc(2026, 4, 6, 0, 0),
			to:   utc(2026, 4, 10, 0, 0),
			want: []wantEvent{
				{"2026-04-06T14:00:00Z", "Standup"},
				{"2026-04-07T16:00:00Z", "Standup (moved)"},
				{"2026-04-08T16:00:00Z", "Standup (moved)"},
				{"2026-04-09T16:00:00Z", "Standup (moved)"},
			},
		},
		{
			name: "cancelled range=thisandfuture truncates the series",
			ics: `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//qi//test//EN
BEGIN:VEVENT
UID:taf-cancel
DTSTAMP:20260401T090000Z
DTSTART:20260406T140000Z
DTEND:20260406T150000Z
RRULE:FREQ=DAILY;COUNT=4
SUMMARY:Ending series
END:VEVENT
BEGIN:VEVENT
UID:taf-cancel
RECURRENCE-ID;RANGE=THISANDFUTURE:20260408T140000Z
DTSTAMP:20260401T090000Z
DTSTART:20260408T140000Z
DTEND:20260408T150000Z
STATUS:CANCELLED
SUMMARY:Ending series
END:VEVENT
END:VCALENDAR`,
			from: utc(2026, 4, 6, 0, 0),
			to:   utc(2026, 4, 10, 0, 0),
			want: []wantEvent{
				{"2026-04-06T14:00:00Z", "Ending series"},
				{"2026-04-07T14:00:00Z", "Ending series"},
			},
		},
		{
			// Precedence: a single-instance override names a slot inside a
			// THISANDFUTURE run and must win for that one instance.
			name: "single override wins inside a thisandfuture run",
			ics: `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//qi//test//EN
BEGIN:VEVENT
UID:prec-1
DTSTAMP:20260401T090000Z
DTSTART:20260406T140000Z
DTEND:20260406T150000Z
RRULE:FREQ=DAILY;COUNT=4
SUMMARY:Series
END:VEVENT
BEGIN:VEVENT
UID:prec-1
RECURRENCE-ID;RANGE=THISANDFUTURE:20260407T140000Z
DTSTAMP:20260401T090000Z
DTSTART:20260407T160000Z
DTEND:20260407T170000Z
SUMMARY:Series (16:00 onwards)
END:VEVENT
BEGIN:VEVENT
UID:prec-1
RECURRENCE-ID:20260408T140000Z
DTSTAMP:20260401T090000Z
DTSTART:20260408T200000Z
DTEND:20260408T210000Z
SUMMARY:Series (this one at 20:00)
END:VEVENT
END:VCALENDAR`,
			from: utc(2026, 4, 6, 0, 0),
			to:   utc(2026, 4, 10, 0, 0),
			want: []wantEvent{
				{"2026-04-06T14:00:00Z", "Series"},
				{"2026-04-07T16:00:00Z", "Series (16:00 onwards)"},
				{"2026-04-08T20:00:00Z", "Series (this one at 20:00)"},
				{"2026-04-09T16:00:00Z", "Series (16:00 onwards)"},
			},
		},
		{
			// TZID keeps a 09:00 Dublin wall clock across the 2026-10-25 DST end, so the
			// absolute instant shifts 08:00Z -> 09:00Z. Exercises real zone arithmetic;
			// the expectations are absolute, hence local-zone independent.
			name: "tzid base across a dst transition with a tzid override",
			ics: `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//qi//test//EN
BEGIN:VEVENT
UID:dst-1
DTSTAMP:20261001T090000Z
DTSTART;TZID=Europe/Dublin:20261024T090000
DTEND;TZID=Europe/Dublin:20261024T100000
RRULE:FREQ=DAILY;COUNT=4
SUMMARY:DST base
END:VEVENT
BEGIN:VEVENT
UID:dst-1
RECURRENCE-ID;TZID=Europe/Dublin:20261026T090000
DTSTAMP:20261001T090000Z
DTSTART;TZID=Europe/Dublin:20261026T113000
DTEND;TZID=Europe/Dublin:20261026T123000
SUMMARY:DST override
END:VEVENT
END:VCALENDAR`,
			from: utc(2026, 10, 23, 0, 0),
			to:   utc(2026, 10, 29, 0, 0),
			want: []wantEvent{
				{"2026-10-24T08:00:00Z", "DST base"}, // IST, UTC+1
				{"2026-10-25T09:00:00Z", "DST base"}, // GMT, UTC+0 (clocks went back)
				{"2026-10-26T11:30:00Z", "DST override"},
				{"2026-10-27T09:00:00Z", "DST base"},
			},
		},
		{
			// A UTC base with a TZID-form RECURRENCE-ID: the two spell the same instant
			// differently, and slot matching must still pair them.
			name: "override matches a slot across mixed zone representations",
			ics: `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//qi//test//EN
BEGIN:VEVENT
UID:mix-1
DTSTAMP:20260401T090000Z
DTSTART:20260406T130000Z
DTEND:20260406T140000Z
RRULE:FREQ=DAILY;COUNT=3
SUMMARY:Mixed base
END:VEVENT
BEGIN:VEVENT
UID:mix-1
RECURRENCE-ID;TZID=Europe/Dublin:20260407T140000
DTSTAMP:20260401T090000Z
DTSTART:20260407T153000Z
DTEND:20260407T163000Z
SUMMARY:Mixed override
END:VEVENT
END:VCALENDAR`,
			from: utc(2026, 4, 6, 0, 0),
			to:   utc(2026, 4, 10, 0, 0),
			want: []wantEvent{
				{"2026-04-06T13:00:00Z", "Mixed base"},
				{"2026-04-07T15:30:00Z", "Mixed override"},
				{"2026-04-08T13:00:00Z", "Mixed base"},
			},
		},
		{
			name: "exdate removes an instance",
			ics: `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//qi//test//EN
BEGIN:VEVENT
UID:ex-1
DTSTAMP:20260401T090000Z
DTSTART:20260406T140000Z
DTEND:20260406T150000Z
RRULE:FREQ=DAILY;COUNT=4
EXDATE:20260407T140000Z
SUMMARY:Exdate base
END:VEVENT
END:VCALENDAR`,
			from: utc(2026, 4, 6, 0, 0),
			to:   utc(2026, 4, 10, 0, 0),
			want: []wantEvent{
				{"2026-04-06T14:00:00Z", "Exdate base"},
				{"2026-04-08T14:00:00Z", "Exdate base"},
				{"2026-04-09T14:00:00Z", "Exdate base"},
			},
		},
		{
			// The base component lives in another calendar object; the override still
			// has to render on its own.
			name: "orphan override renders standalone",
			ics: `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//qi//test//EN
BEGIN:VEVENT
UID:orphan-1
RECURRENCE-ID:20260407T140000Z
DTSTAMP:20260401T090000Z
DTSTART:20260407T163000Z
DTEND:20260407T173000Z
SUMMARY:Orphan override
END:VEVENT
END:VCALENDAR`,
			from: utc(2026, 4, 6, 0, 0),
			to:   utc(2026, 4, 10, 0, 0),
			want: []wantEvent{
				{"2026-04-07T16:30:00Z", "Orphan override"},
			},
		},
		{
			// Same, but RANGE=THISANDFUTURE: with no base to reshape it must still
			// render as itself rather than vanishing.
			name: "orphan thisandfuture override renders standalone",
			ics: `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//qi//test//EN
BEGIN:VEVENT
UID:orphan-taf
RECURRENCE-ID;RANGE=THISANDFUTURE:20260407T140000Z
DTSTAMP:20260401T090000Z
DTSTART:20260407T160000Z
DTEND:20260407T170000Z
SUMMARY:Orphan thisandfuture
END:VEVENT
END:VCALENDAR`,
			from: utc(2026, 4, 6, 0, 0),
			to:   utc(2026, 4, 10, 0, 0),
			want: []wantEvent{
				{"2026-04-07T16:00:00Z", "Orphan thisandfuture"},
			},
		},
		{
			name: "multiple single overrides on one event",
			ics: `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//qi//test//EN
BEGIN:VEVENT
UID:multi-1
DTSTAMP:20260401T090000Z
DTSTART:20260406T140000Z
DTEND:20260406T150000Z
RRULE:FREQ=DAILY;COUNT=4
SUMMARY:Multi base
END:VEVENT
BEGIN:VEVENT
UID:multi-1
RECURRENCE-ID:20260407T140000Z
DTSTAMP:20260401T090000Z
DTSTART:20260407T090000Z
DTEND:20260407T100000Z
SUMMARY:Moved early
END:VEVENT
BEGIN:VEVENT
UID:multi-1
RECURRENCE-ID:20260408T140000Z
DTSTAMP:20260401T090000Z
DTSTART:20260408T140000Z
DTEND:20260408T150000Z
STATUS:CANCELLED
SUMMARY:Multi base
END:VEVENT
BEGIN:VEVENT
UID:multi-1
RECURRENCE-ID:20260409T140000Z
DTSTAMP:20260401T090000Z
DTSTART:20260409T183000Z
DTEND:20260409T193000Z
SUMMARY:Retitled and moved late
END:VEVENT
END:VCALENDAR`,
			from: utc(2026, 4, 6, 0, 0),
			to:   utc(2026, 4, 10, 0, 0),
			want: []wantEvent{
				{"2026-04-06T14:00:00Z", "Multi base"},
				{"2026-04-07T09:00:00Z", "Moved early"},
				{"2026-04-09T18:30:00Z", "Retitled and moved late"},
			},
		},
		{
			// The named slot is outside the window but the override moved the instance
			// into it: it must still surface.
			name: "override moved into the query window",
			ics: `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//qi//test//EN
BEGIN:VEVENT
UID:into-1
DTSTAMP:20260401T090000Z
DTSTART:20260406T140000Z
DTEND:20260406T150000Z
RRULE:FREQ=DAILY;COUNT=30
SUMMARY:Into base
END:VEVENT
BEGIN:VEVENT
UID:into-1
RECURRENCE-ID:20260420T140000Z
DTSTAMP:20260401T090000Z
DTSTART:20260407T100000Z
DTEND:20260407T110000Z
SUMMARY:Moved into window
END:VEVENT
END:VCALENDAR`,
			from: utc(2026, 4, 6, 0, 0),
			to:   utc(2026, 4, 10, 0, 0),
			want: []wantEvent{
				{"2026-04-06T14:00:00Z", "Into base"},
				{"2026-04-07T10:00:00Z", "Moved into window"},
				{"2026-04-07T14:00:00Z", "Into base"},
				{"2026-04-08T14:00:00Z", "Into base"},
				{"2026-04-09T14:00:00Z", "Into base"},
			},
		},
		{
			// Inverse: the slot is in the window, the override took it out.
			name: "override moved out of the query window",
			ics: `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//qi//test//EN
BEGIN:VEVENT
UID:out-1
DTSTAMP:20260401T090000Z
DTSTART:20260406T140000Z
DTEND:20260406T150000Z
RRULE:FREQ=DAILY;COUNT=4
SUMMARY:Out base
END:VEVENT
BEGIN:VEVENT
UID:out-1
RECURRENCE-ID:20260407T140000Z
DTSTAMP:20260401T090000Z
DTSTART:20260525T140000Z
DTEND:20260525T150000Z
SUMMARY:Moved far out
END:VEVENT
END:VCALENDAR`,
			from: utc(2026, 4, 6, 0, 0),
			to:   utc(2026, 4, 10, 0, 0),
			want: []wantEvent{
				{"2026-04-06T14:00:00Z", "Out base"},
				{"2026-04-08T14:00:00Z", "Out base"},
				{"2026-04-09T14:00:00Z", "Out base"},
			},
		},
		// All-day (VALUE=DATE) is floating and binds to local midnight, so it cannot be
		// asserted against an absolute instant — see
		// TestExpandCalendarEvents_AllDayLocalMidnight.
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := expandCalendarEvents(decodeICS(t, tt.ics), "test", tt.from, tt.to)
			assertEvents(t, got, tt.want)
		})
	}
}

// assertEvents compares an expansion against expectations, order-insensitively (the
// provider makes no ordering promise; AgendaService sorts).
func assertEvents(t *testing.T, got []domain.Event, want []wantEvent) {
	t.Helper()

	gotKeys := make([]string, 0, len(got))
	for _, e := range got {
		gotKeys = append(gotKeys, e.Start.UTC().Format(time.RFC3339)+"  "+e.Title)
	}
	wantKeys := make([]string, 0, len(want))
	for _, w := range want {
		wantKeys = append(wantKeys, w.start+"  "+w.title)
	}
	sort.Strings(gotKeys)
	sort.Strings(wantKeys)

	if len(gotKeys) != len(wantKeys) {
		t.Errorf("got %d events, want %d", len(gotKeys), len(wantKeys))
	}
	for i := 0; i < len(gotKeys) || i < len(wantKeys); i++ {
		switch {
		case i >= len(gotKeys):
			t.Errorf("missing: %s", wantKeys[i])
		case i >= len(wantKeys):
			t.Errorf("unexpected: %s", gotKeys[i])
		case gotKeys[i] != wantKeys[i]:
			t.Errorf("got %q, want %q", gotKeys[i], wantKeys[i])
		}
	}
}

// TestExpandCalendarEvents_AllDayLocalMidnight covers VALUE=DATE separately: all-day
// values are floating, so the correct instant depends on the machine's zone and the
// assertion has to be in local wall-clock terms.
func TestExpandCalendarEvents_AllDayLocalMidnight(t *testing.T) {
	ics := `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//qi//test//EN
BEGIN:VEVENT
UID:allday-1
DTSTAMP:20260401T090000Z
DTSTART;VALUE=DATE:20260406
DTEND;VALUE=DATE:20260407
RRULE:FREQ=DAILY;COUNT=3
SUMMARY:All day thing
END:VEVENT
BEGIN:VEVENT
UID:allday-1
RECURRENCE-ID;VALUE=DATE:20260407
DTSTAMP:20260401T090000Z
DTSTART;VALUE=DATE:20260407
DTEND;VALUE=DATE:20260408
SUMMARY:All day thing (edited)
END:VEVENT
END:VCALENDAR`

	got := expandCalendarEvents(decodeICS(t, ics), "test",
		time.Date(2026, 4, 4, 0, 0, 0, 0, time.Local),
		time.Date(2026, 4, 11, 0, 0, 0, 0, time.Local))

	type dayTitle struct {
		day   string
		title string
	}
	var keys []dayTitle
	for _, e := range got {
		if h, m := e.Start.Hour(), e.Start.Minute(); h != 0 || m != 0 {
			t.Errorf("all-day event %q starts at %02d:%02d local, want local midnight", e.Title, h, m)
		}
		keys = append(keys, dayTitle{e.Start.Format("2006-01-02"), e.Title})
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].day < keys[j].day })

	want := []dayTitle{
		{"2026-04-06", "All day thing"},
		{"2026-04-07", "All day thing (edited)"}, // override wins, no duplicate
		{"2026-04-08", "All day thing"},
	}
	if len(keys) != len(want) {
		t.Fatalf("got %+v, want %+v", keys, want)
	}
	for i := range want {
		if keys[i] != want[i] {
			t.Errorf("event[%d] = %+v, want %+v", i, keys[i], want[i])
		}
	}
}

// TestExpandCalendarEvents_ThisAndFutureIgnoresOverrideRRule pins the documented
// limitation: an override that carries its own RRULE has that rule ignored — the base
// rule's slots are shifted instead. If this ever starts failing because the override's
// rule is honoured, that is an improvement: update the doc comment on
// applyThisAndFuture and this test together.
func TestExpandCalendarEvents_ThisAndFutureIgnoresOverrideRRule(t *testing.T) {
	// Base recurs daily; the override says "from 04-07, go weekly at 16:00".
	ics := `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//qi//test//EN
BEGIN:VEVENT
UID:rule-1
DTSTAMP:20260401T090000Z
DTSTART:20260406T140000Z
DTEND:20260406T150000Z
RRULE:FREQ=DAILY;COUNT=4
SUMMARY:Rule change
END:VEVENT
BEGIN:VEVENT
UID:rule-1
RECURRENCE-ID;RANGE=THISANDFUTURE:20260407T140000Z
DTSTAMP:20260401T090000Z
DTSTART:20260407T160000Z
DTEND:20260407T170000Z
RRULE:FREQ=WEEKLY;COUNT=2
SUMMARY:Rule change (weekly now)
END:VEVENT
END:VCALENDAR`

	got := expandCalendarEvents(decodeICS(t, ics), "test", utc(2026, 4, 6, 0, 0), utc(2026, 4, 21, 0, 0))

	// Actual: the daily base slots are shifted to 16:00. Honouring the override's
	// weekly rule would instead give 04-07 and 04-14 only.
	assertEvents(t, got, []wantEvent{
		{"2026-04-06T14:00:00Z", "Rule change"},
		{"2026-04-07T16:00:00Z", "Rule change (weekly now)"},
		{"2026-04-08T16:00:00Z", "Rule change (weekly now)"},
		{"2026-04-09T16:00:00Z", "Rule change (weekly now)"},
	})
}
