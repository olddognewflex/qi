package typesafe

import (
	"strconv"
	"strings"
	"time"
)

// Part is one answered date-part question: the chosen option and its
// confidence.
type Part struct {
	Value      string
	Confidence float64
}

// DateParts are the model's answers to the date-part questions for one
// capture. They are structure, not a date: ResolveDate does the calendar math.
type DateParts struct {
	Mode, Role, Month, Day, Year, Anchor, Weekday, Week Part
}

// Date resolution outcomes.
const (
	DateNone   = "none"   // no date for the user's action
	DateOK     = "ok"     // resolved
	DateReview = "review" // the parts are incomplete, impossible, or contradictory
)

// ResolvedDate is ResolveDate's result. Confidence is the minimum confidence
// over the parts actually used, so one shaky part makes the whole date shaky.
type ResolvedDate struct {
	Status     string
	Date       time.Time
	Role       string // "due" or "scheduled" when Status is DateOK
	Confidence float64
}

// ResolveDate turns date parts into a calendar date relative to captured (the
// day the capture was written), following the TypeSafe date-extraction
// cookbook:
//   - absolute: month and day must be given and valid; a stated year is used
//     as is, otherwise the year is inferred as captured's, rolling to the next
//     year when the date would be more than 31 days before captured;
//   - relative: today/tomorrow/day_after offset from captured; a weekday is the
//     next one on or after captured ("bare"), the one in captured's Monday-start
//     week ("current"), or the one in the following week ("next").
//
// Only an explicit "none" mode is DateNone; a missing or unknown mode answer is
// DateReview, as is a year outside the offered range ("other"). A zero
// captured time, a date with no due/scheduled role, an impossible date (February 30), a
// missing part needed by the mode, or a date before captured (the questions
// ask about the user's upcoming action, so a past date is a misreading) is
// DateReview, never a guess.
func ResolveDate(p DateParts, captured time.Time) ResolvedDate {
	conf := p.Mode.Confidence
	use := func(parts ...Part) {
		for _, x := range parts {
			conf = min(conf, x.Confidence)
		}
	}
	review := func() ResolvedDate { return ResolvedDate{Status: DateReview, Confidence: conf} }
	if captured.IsZero() { // nothing to anchor to: never resolve against year 1
		return review()
	}
	day0 := time.Date(captured.Year(), captured.Month(), captured.Day(), 0, 0, 0, 0, captured.Location())

	var date time.Time
	switch p.Mode.Value {
	case "absolute":
		use(p.Month, p.Day, p.Year)
		month, ok := monthByName(p.Month.Value)
		if !ok {
			return review()
		}
		d, err := strconv.Atoi(p.Day.Value)
		if err != nil {
			return review()
		}
		// Pick the year from (month, day) alone, then build the date once, so a
		// Feb 29 is validated in the year it actually lands in.
		year := day0.Year()
		switch p.Year.Value {
		case "none":
			if before(year, month, d, day0.AddDate(0, 0, -31)) {
				year++
			}
		default:
			y, err := strconv.Atoi(p.Year.Value)
			if err != nil { // "other", missing, or unknown
				return review()
			}
			year = y
		}
		date = time.Date(year, month, d, 0, 0, 0, 0, day0.Location())
		if date.Month() != month { // normalised past the month's end
			return review()
		}
	case "relative":
		use(p.Anchor)
		switch p.Anchor.Value {
		case "today":
			date = day0
		case "tomorrow":
			date = day0.AddDate(0, 0, 1)
		case "day_after":
			date = day0.AddDate(0, 0, 2)
		case "weekday":
			use(p.Weekday, p.Week)
			wd, ok := weekdayByName(p.Weekday.Value)
			if !ok {
				return review()
			}
			switch p.Week.Value {
			case "bare":
				date = day0.AddDate(0, 0, (int(wd)-int(day0.Weekday())+7)%7)
			case "current", "next":
				monday := day0.AddDate(0, 0, -((int(day0.Weekday()) + 6) % 7))
				date = monday.AddDate(0, 0, (int(wd)+6)%7)
				if p.Week.Value == "next" {
					date = date.AddDate(0, 0, 7)
				}
			default:
				return review()
			}
		default:
			return review()
		}
	case "none":
		return ResolvedDate{Status: DateNone, Confidence: conf}
	default: // missing or unknown answer: not evidence of "no date"
		return review()
	}

	use(p.Role)
	if p.Role.Value != "due" && p.Role.Value != "scheduled" || date.Before(day0) {
		return review()
	}
	return ResolvedDate{Status: DateOK, Date: date, Role: p.Role.Value, Confidence: conf}
}

// before reports whether calendar day (y, m, d) is before t's calendar day,
// without normalising an invalid d.
func before(y int, m time.Month, d int, t time.Time) bool {
	if y != t.Year() {
		return y < t.Year()
	}
	if m != t.Month() {
		return m < t.Month()
	}
	return d < t.Day()
}

func monthByName(s string) (time.Month, bool) {
	for m := time.January; m <= time.December; m++ {
		if strings.EqualFold(m.String(), s) {
			return m, true
		}
	}
	return 0, false
}

func weekdayByName(s string) (time.Weekday, bool) {
	for w := time.Sunday; w <= time.Saturday; w++ {
		if strings.EqualFold(w.String(), s) {
			return w, true
		}
	}
	return 0, false
}
