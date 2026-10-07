package typesafe

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// DestinationNone is the destination key meaning "no configured project or
// client fits". It is always offered so the model is never forced to guess.
const DestinationNone = "none"

// InboxDestination is one place a task can be routed: a configured project or
// client. Key is what comes back; Description is what the model reads, so it
// should say what the project is, not just repeat its tag.
type InboxDestination struct {
	Key         string
	Description string
}

// InboxFanout configures the extra questions ProposeInbox asks per capture
// alongside the task|note|archive action, in the same request (speculative
// fan-out: each is phrased "if this were a task", and the caller uses only the
// answers that apply to the action it ends up with).
type InboxFanout struct {
	// Destinations enables the routing question when non-empty.
	Destinations []InboxDestination
	// Dates enables the date-part questions. Relative phrases are read
	// against each capture's own Captured time ("tomorrow" written last week
	// means last week's tomorrow); calendar math happens in ResolveDate, never
	// in the model.
	Dates bool
	// Lean trades one extra request for far fewer tokens: destination
	// descriptions move into shared state (criteria just point at them), and
	// only the cheap date_mode question rides in the first request — the
	// other date parts are asked in a second request, only for captures whose
	// mode is not "none".
	Lean bool
}

// InboxCapture is one capture as ProposeInbox sees it: its non-empty body
// lines and when it was captured (zero if unknown).
type InboxCapture struct {
	Body     []string
	Captured time.Time
}

// InboxProposal is the fan-out verdict for one capture. Destination and Date
// are zero when their questions were not asked or not answered.
type InboxProposal struct {
	InboxClassification
	Destination           string
	DestinationConfidence float64
	Date                  DateParts
	// DateErr is set when Lean's second (date-detail) request failed for this
	// capture: Date then holds only Mode, and the caller should say "date
	// lookup failed" rather than treat it as an unsure date.
	DateErr error
	// Err is set when the action answer itself is missing; the other fields
	// are then meaningless.
	Err error
}

// destinationKeyRE limits keys to what reads unambiguously as a state path
// segment (`destinations.<key>`) and a Choice option.
var destinationKeyRE = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// validateDestinations rejects keys that would collide with DestinationNone,
// collapse into each other, or make an ambiguous state path.
func validateDestinations(dests []InboxDestination) error {
	seen := make(map[string]bool, len(dests))
	for _, d := range dests {
		switch {
		case !destinationKeyRE.MatchString(d.Key):
			return fmt.Errorf("typesafe: destination key %q must be letters, digits, '-' or '_'", d.Key)
		case strings.EqualFold(d.Key, DestinationNone):
			return fmt.Errorf("typesafe: destination key %q is reserved", d.Key)
		case seen[strings.ToLower(d.Key)]:
			return fmt.Errorf("typesafe: duplicate destination key %q", d.Key)
		}
		seen[strings.ToLower(d.Key)] = true
	}
	return nil
}

// fanoutQuestionID names question q for capture i.
func fanoutQuestionID(q string, i int) string { return fmt.Sprintf("%s_%d", q, i) }

func destinationQuestion(i int, dests []InboxDestination, lean bool) Question {
	criteria := make(map[string]string, len(dests)+1)
	for _, d := range dests {
		criteria[d.Key] = d.Description
		if lean {
			criteria[d.Key] = fmt.Sprintf("The project or client described in `destinations.%s`.", d.Key)
		}
	}
	criteria[DestinationNone] = "None of the listed projects or clients: personal, household, finance, shopping, or unrelated to any of them."
	return Question{
		Type: TypeChoice,
		Instructions: map[string]string{
			"question": fmt.Sprintf("If `captures[%d].text` were a task, which project or client does it belong to?", i),
			"focus":    "Choose the most specific listed project that clearly fits; choose a client only when it fits the client but none of its projects. Judge only this capture.",
		},
		Criteria: criteria,
	}
}

// dateQuestions are the cookbook's date-part questions for capture i plus a
// role question (due vs scheduled). Each part is a closed set; ResolveDate
// combines them.
func dateQuestions(i int, captured time.Time) map[string]Question {
	ref := fmt.Sprintf("`captures[%d].text` (written on `captures[%d].captured_on`)", i, i)
	months := map[string]string{"none": "No month is named."}
	for m := time.January; m <= time.December; m++ {
		months[strings.ToLower(m.String())] = m.String()
	}
	days := map[string]string{"none": "No day of the month is given as a number."}
	for d := 1; d <= 31; d++ {
		days[fmt.Sprint(d)] = fmt.Sprintf("Day %d of the month.", d)
	}
	// Years are offered around the capture year; anything else is "other",
	// which ResolveDate sends to review rather than re-dating.
	years := map[string]string{
		"none":  "No year is stated.",
		"other": "A year is stated but it is not one of the listed years.",
	}
	if !captured.IsZero() {
		for y := captured.Year() - 1; y <= captured.Year()+5; y++ {
			years[fmt.Sprint(y)] = fmt.Sprintf("The year %d.", y)
		}
	}
	weekdays := map[string]string{"none": "No weekday is named."}
	for w := time.Sunday; w <= time.Saturday; w++ {
		weekdays[strings.ToLower(w.String())] = w.String()
	}
	return map[string]Question{
		fanoutQuestionID("date_mode", i): {
			Type: TypeChoice,
			Instructions: map[string]string{
				"question": fmt.Sprintf("Does %s say when something should happen or is due?", ref),
				"focus":    "Only dates for the user's own action or deadline count. Ignore dates that merely describe past events, timestamps, or other people's schedules the user has no part in.",
			},
			Criteria: map[string]string{
				"absolute": "Names a calendar date with a month (e.g. 'March 3', 'by Aug 1, 2026', '3/14').",
				"relative": "Uses a relative day: today, tonight, tomorrow, the day after tomorrow, or a weekday name ('Friday', 'next Tuesday').",
				"none":     "No date for the user's action, or only a vague time ('soon', 'later', 'sometime', 'in 3 days').",
			},
		},
		fanoutQuestionID("date_role", i): {
			Type:         TypeChoice,
			Instructions: fmt.Sprintf("If %s gives a date for the user's action, what kind of date is it?", ref),
			Criteria: map[string]string{
				"due":       "A deadline: the thing must be done by then ('by Friday', 'due March 3', 'renewal on the 14th', 'expires').",
				"scheduled": "When the user plans to do it or when it happens ('call mom Sunday', 'meeting Tuesday', 'on the 14th').",
				"none":      "There is no such date.",
			},
		},
		fanoutQuestionID("date_month", i): {Type: TypeChoice, Instructions: fmt.Sprintf("If %s names a calendar date for the user's action, which month?", ref), Criteria: months},
		fanoutQuestionID("date_year", i):  {Type: TypeChoice, Instructions: fmt.Sprintf("If %s names a calendar date for the user's action, does it state a year, and which?", ref), Criteria: years},
		fanoutQuestionID("date_day", i):   {Type: TypeChoice, Instructions: fmt.Sprintf("If %s names a calendar date for the user's action, which day of the month?", ref), Criteria: days},
		fanoutQuestionID("date_anchor", i): {
			Type:         TypeChoice,
			Instructions: fmt.Sprintf("If %s uses a relative day for the user's action, which kind?", ref),
			Criteria: map[string]string{
				"today":     "Today or tonight.",
				"tomorrow":  "Tomorrow.",
				"day_after": "The day after tomorrow.",
				"weekday":   "A named weekday ('Friday', 'next Tuesday', 'this Thursday').",
				"none":      "No relative day.",
			},
		},
		fanoutQuestionID("date_weekday", i): {Type: TypeChoice, Instructions: fmt.Sprintf("If %s names a weekday for the user's action, which one?", ref), Criteria: weekdays},
		fanoutQuestionID("date_week", i): {
			Type:         TypeChoice,
			Instructions: fmt.Sprintf("If %s names a weekday for the user's action, which week does it mean?", ref),
			Criteria: map[string]string{
				"bare":    "Just the weekday ('Friday', 'on Tuesday'): the next one coming up.",
				"current": "Explicitly this week ('this Friday').",
				"next":    "Explicitly the following week ('next Friday', 'Friday next week').",
			},
		},
	}
}

// fanoutRequest builds one request over a batch: the same state and action
// questions inboxRequest builds, plus the enabled fan-out questions.
func fanoutRequest(caps []InboxCapture, f InboxFanout) Request {
	bodies := make([][]string, len(caps))
	for i, c := range caps {
		bodies[i] = c.Body
	}
	req, captures := inboxRequestParts(bodies)
	state := req.State.(map[string]any)
	if len(f.Destinations) > 0 {
		for i := range bodies {
			req.Questions[fanoutQuestionID("dest", i)] = destinationQuestion(i, f.Destinations, f.Lean)
		}
		if f.Lean {
			descs := make(map[string]string, len(f.Destinations))
			for _, d := range f.Destinations {
				descs[d.Key] = d.Description
			}
			state["destinations"] = descs
		}
	}
	if f.Dates {
		for i, c := range caps {
			if !c.Captured.IsZero() {
				captures[i]["captured_on"] = c.Captured.Format("Monday, January 2, 2006")
			}
		}
		for i := range bodies {
			for id, q := range dateQuestions(i, caps[i].Captured) {
				if f.Lean && !strings.HasPrefix(id, "date_mode_") {
					continue
				}
				req.Questions[id] = q
			}
		}
	}
	return req
}

// ProposeInbox is ClassifyInbox plus the fan-out questions in f, in ONE
// request per batch (two in Lean mode). Invalid destinations, or a transport
// or API error on the first request, fail the batch (nil results). A missing
// action answer is a per-item Err; a missing fan-out answer just leaves that
// field zero; a failed Lean date-detail request is a per-item DateErr on the
// captures it covered. Usage is returned so callers can measure the cost.
func (c InboxClassifier) ProposeInbox(ctx context.Context, caps []InboxCapture, f InboxFanout) ([]InboxProposal, Usage, error) {
	if err := validateDestinations(f.Destinations); err != nil {
		return nil, Usage{}, err
	}
	if len(caps) == 0 {
		return nil, Usage{}, nil
	}
	resp, err := c.Client.SystemOne(ctx, fanoutRequest(caps, f))
	if err != nil {
		return nil, Usage{}, err
	}
	out := make([]InboxProposal, len(caps))
	for i := range caps {
		ans, ok := resp.Answers[inboxQuestionID(i)]
		if !ok || ans.Choice == "" {
			out[i].Err = fmt.Errorf("typesafe: response has no %q choice answer", inboxQuestionID(i))
			continue
		}
		out[i].InboxClassification = InboxClassification{Action: ans.Choice, Confidence: ans.Confidence, Probabilities: ans.Probabilities}
		if d, ok := resp.Answers[fanoutQuestionID("dest", i)]; ok {
			out[i].Destination, out[i].DestinationConfidence = d.Choice, d.Confidence
		}
		if f.Dates {
			out[i].Date = datePartsFrom(resp.Answers, i)
		}
	}
	usage := resp.Usage
	if f.Dates && f.Lean {
		u := c.dateDetails(ctx, caps, out)
		usage.InputTokens += u.InputTokens
		usage.OutputTokens += u.OutputTokens
	}
	return out, usage, nil
}

// dateDetails is Lean's second request: the remaining date-part questions,
// only for captures proposed as tasks (dates are applied to nothing else)
// whose first-stage date_mode found a date. The state holds just those
// captures, so the questions are re-indexed and mapped back. A failure marks
// each of them with DateErr.
func (c InboxClassifier) dateDetails(ctx context.Context, caps []InboxCapture, out []InboxProposal) Usage {
	var idx []int
	for i := range out {
		if out[i].Err == nil && out[i].Action == InboxTask && out[i].Date.Mode.Value != "" && out[i].Date.Mode.Value != "none" {
			idx = append(idx, i)
		}
	}
	if len(idx) == 0 {
		return Usage{}
	}
	sub := make([]InboxCapture, len(idx))
	for k, i := range idx {
		sub[k] = caps[i]
	}
	req := fanoutRequest(sub, InboxFanout{Dates: true})
	for id := range req.Questions {
		if strings.HasPrefix(id, "action_") || strings.HasPrefix(id, "date_mode_") {
			delete(req.Questions, id)
		}
	}
	resp, err := c.Client.SystemOne(ctx, req)
	if err != nil {
		for _, i := range idx {
			out[i].DateErr = fmt.Errorf("typesafe: date details: %w", err)
		}
		return Usage{}
	}
	for k, i := range idx {
		mode := out[i].Date.Mode
		out[i].Date = datePartsFrom(resp.Answers, k)
		out[i].Date.Mode = mode
	}
	return resp.Usage
}

// datePartsFrom collects capture i's date-part answers.
func datePartsFrom(answers map[string]Answer, i int) DateParts {
	get := func(q string) Part {
		a := answers[fanoutQuestionID(q, i)]
		return Part{Value: a.Choice, Confidence: a.Confidence}
	}
	return DateParts{
		Mode:    get("date_mode"),
		Role:    get("date_role"),
		Month:   get("date_month"),
		Year:    get("date_year"),
		Day:     get("date_day"),
		Anchor:  get("date_anchor"),
		Weekday: get("date_weekday"),
		Week:    get("date_week"),
	}
}
