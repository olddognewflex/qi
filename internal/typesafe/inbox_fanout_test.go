package typesafe

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func p(v string, c float64) Part { return Part{Value: v, Confidence: c} }

func TestResolveDate(t *testing.T) {
	// Tuesday, October 6, 2026.
	captured := time.Date(2026, 10, 6, 7, 30, 0, 0, time.UTC)
	day := func(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }
	due := p("due", 0.9)

	tests := []struct {
		name     string
		parts    DateParts
		status   string
		date     time.Time
		wantConf float64
	}{
		{"none", DateParts{Mode: p("none", 0.95)}, DateNone, time.Time{}, 0.95},
		{"absolute this year", DateParts{Mode: p("absolute", 0.9), Month: p("november", 0.8), Day: p("3", 0.7), Role: due}, DateOK, day(2026, 11, 3), 0.7},
		{"absolute recent past is review", DateParts{Mode: p("absolute", 0.9), Month: p("september", 0.9), Day: p("20", 0.9), Role: due}, DateReview, time.Time{}, 0.9},
		{"absolute rolls to next year", DateParts{Mode: p("absolute", 0.9), Month: p("march", 0.9), Day: p("3", 0.9), Role: due}, DateOK, day(2027, 3, 3), 0.9},
		{"impossible date", DateParts{Mode: p("absolute", 0.9), Month: p("february", 0.9), Day: p("30", 0.9), Role: due}, DateReview, time.Time{}, 0.9},
		{"absolute missing day", DateParts{Mode: p("absolute", 0.9), Month: p("march", 0.9), Day: p("none", 0.6), Role: due}, DateReview, time.Time{}, 0.6},
		{"today", DateParts{Mode: p("relative", 0.9), Anchor: p("today", 0.9), Role: due}, DateOK, day(2026, 10, 6), 0.9},
		{"tomorrow", DateParts{Mode: p("relative", 0.9), Anchor: p("tomorrow", 0.5), Role: due}, DateOK, day(2026, 10, 7), 0.5},
		{"day after", DateParts{Mode: p("relative", 0.9), Anchor: p("day_after", 0.9), Role: due}, DateOK, day(2026, 10, 8), 0.9},
		{"bare friday", DateParts{Mode: p("relative", 0.9), Anchor: p("weekday", 0.9), Weekday: p("friday", 0.9), Week: p("bare", 0.9), Role: due}, DateOK, day(2026, 10, 9), 0.9},
		{"bare same weekday is today", DateParts{Mode: p("relative", 0.9), Anchor: p("weekday", 0.9), Weekday: p("tuesday", 0.9), Week: p("bare", 0.9), Role: due}, DateOK, day(2026, 10, 6), 0.9},
		{"bare monday wraps", DateParts{Mode: p("relative", 0.9), Anchor: p("weekday", 0.9), Weekday: p("monday", 0.9), Week: p("bare", 0.9), Role: due}, DateOK, day(2026, 10, 12), 0.9},
		{"this monday is past: review", DateParts{Mode: p("relative", 0.9), Anchor: p("weekday", 0.9), Weekday: p("monday", 0.9), Week: p("current", 0.9), Role: due}, DateReview, time.Time{}, 0.9},
		{"this thursday", DateParts{Mode: p("relative", 0.9), Anchor: p("weekday", 0.9), Weekday: p("thursday", 0.9), Week: p("current", 0.9), Role: due}, DateOK, day(2026, 10, 8), 0.9},
		{"this sunday ends week", DateParts{Mode: p("relative", 0.9), Anchor: p("weekday", 0.9), Weekday: p("sunday", 0.9), Week: p("current", 0.9), Role: due}, DateOK, day(2026, 10, 11), 0.9},
		{"next friday", DateParts{Mode: p("relative", 0.9), Anchor: p("weekday", 0.9), Weekday: p("friday", 0.9), Week: p("next", 0.4), Role: due}, DateOK, day(2026, 10, 16), 0.4},
		{"weekday missing", DateParts{Mode: p("relative", 0.9), Anchor: p("weekday", 0.9), Weekday: p("none", 0.9), Week: p("bare", 0.9), Role: due}, DateReview, time.Time{}, 0.9},
		{"zero capture time", DateParts{Mode: p("relative", 0.9), Anchor: p("tomorrow", 0.9), Role: due}, DateReview, time.Time{}, 0.9},
		{"no role", DateParts{Mode: p("relative", 0.9), Anchor: p("today", 0.9), Role: p("none", 0.3)}, DateReview, time.Time{}, 0.3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := captured
			if tt.name == "zero capture time" {
				c = time.Time{}
			}
			got := ResolveDate(tt.parts, c)
			if got.Status != tt.status || !got.Date.Equal(tt.date) || got.Confidence != tt.wantConf {
				t.Errorf("got %+v, want status %s date %s conf %v", got, tt.status, tt.date.Format("2006-01-02"), tt.wantConf)
			}
		})
	}
}

func TestResolveDateLeapDayRollover(t *testing.T) {
	feb29 := DateParts{Mode: p("absolute", 0.9), Month: p("february", 0.9), Day: p("29", 0.9), Role: p("due", 0.9)}
	tests := []struct {
		captured time.Time
		status   string
		date     time.Time
	}{
		// Feb 29 2028 is past the cutoff; 2029 has no Feb 29 → review, not Mar 1.
		{time.Date(2028, 12, 1, 0, 0, 0, 0, time.UTC), DateReview, time.Time{}},
		// Captured in 2027, Feb 29 rolls into leap year 2028.
		{time.Date(2027, 12, 1, 0, 0, 0, 0, time.UTC), DateOK, time.Date(2028, 2, 29, 0, 0, 0, 0, time.UTC)},
		// Captured early in a leap year: this year's Feb 29.
		{time.Date(2028, 1, 10, 0, 0, 0, 0, time.UTC), DateOK, time.Date(2028, 2, 29, 0, 0, 0, 0, time.UTC)},
	}
	for _, tt := range tests {
		got := ResolveDate(feb29, tt.captured)
		if got.Status != tt.status || !got.Date.Equal(tt.date) {
			t.Errorf("captured %s: got %+v, want %s %s", tt.captured.Format("2006-01-02"), got, tt.status, tt.date.Format("2006-01-02"))
		}
	}
}

func TestProposeInboxRequestAndParse(t *testing.T) {
	var got struct {
		State struct {
			Captures []map[string]string `json:"captures"`
		} `json:"state"`
		Questions map[string]Question `json:"questions"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode: %v", err)
		}
		io.WriteString(w, `{"model":"jev","answers":{
			"action_0":{"type":"choice","choice":"task","confidence":0.8},
			"dest_0":{"type":"choice","choice":"portal","confidence":0.7},
			"date_mode_0":{"type":"choice","choice":"relative","confidence":0.9},
			"date_anchor_0":{"type":"choice","choice":"tomorrow","confidence":0.85},
			"date_role_0":{"type":"choice","choice":"due","confidence":0.6},
			"dest_1":{"type":"choice","choice":"none","confidence":0.9}},
			"usage":{"input_tokens":100,"output_tokens":10}}`)
	}))
	defer srv.Close()

	captured := time.Date(2026, 10, 6, 7, 0, 0, 0, time.UTC)
	caps := []InboxCapture{{Body: []string{"fix preview CI tomorrow"}, Captured: captured}, {Body: []string{"milk"}}}
	f := InboxFanout{Destinations: []InboxDestination{{Key: "portal", Description: "Globex portal platform"}}, Dates: true}
	res, usage, err := InboxClassifier{Client: newTestClient(srv)}.ProposeInbox(context.Background(), caps, f)
	if err != nil {
		t.Fatalf("ProposeInbox: %v", err)
	}

	// 2 captures × (action + dest + 7 date parts).
	if len(got.Questions) != 18 {
		t.Errorf("questions = %d, want 18", len(got.Questions))
	}
	if got.State.Captures[0]["captured_on"] != "Tuesday, October 6, 2026" {
		t.Errorf("captured_on = %q", got.State.Captures[0]["captured_on"])
	}
	if _, ok := got.State.Captures[1]["captured_on"]; ok {
		t.Errorf("zero capture time should be omitted: %v", got.State.Captures[1])
	}
	crit, _ := got.Questions["dest_0"].Criteria.(map[string]any)
	if crit["portal"] != "Globex portal platform" || crit[DestinationNone] == nil {
		t.Errorf("dest criteria = %v", crit)
	}
	for id, q := range got.Questions {
		if strings.HasPrefix(id, "date_") {
			instr, _ := json.Marshal(q.Instructions)
			if !strings.Contains(string(instr), "captured_on") {
				t.Errorf("%s does not reference the capture date: %s", id, instr)
			}
		}
	}

	if usage.InputTokens != 100 {
		t.Errorf("usage = %+v", usage)
	}
	if res[0].Action != "task" || res[0].Destination != "portal" || res[0].DestinationConfidence != 0.7 {
		t.Errorf("res[0] = %+v", res[0])
	}
	rd := ResolveDate(res[0].Date, captured)
	if rd.Status != DateOK || rd.Date.Day() != 7 || rd.Role != "due" || rd.Confidence != 0.6 {
		t.Errorf("resolved = %+v", rd)
	}
	if res[1].Err == nil {
		t.Errorf("missing action answer should be a per-item error: %+v", res[1])
	}
}

func TestProposeInboxWithoutFanoutMatchesClassifyRequest(t *testing.T) {
	bodies := [][]string{{"a"}, {"b", "c"}}
	caps := []InboxCapture{{Body: bodies[0]}, {Body: bodies[1]}}
	a, _ := json.Marshal(inboxRequest(bodies))
	b, _ := json.Marshal(fanoutRequest(caps, InboxFanout{}))
	if string(a) != string(b) {
		t.Errorf("fan-out with nothing enabled changed the request:\n%s\n%s", a, b)
	}
}

func TestCheckDuplicates(t *testing.T) {
	var got struct {
		Questions map[string]Question `json:"questions"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got)
		io.WriteString(w, `{"answers":{"dup_0_0":{"type":"noul","noul":0.92},"dup_1_1":{"type":"noul","noul":0.1}}}`)
	}))
	defer srv.Close()

	checks := []DupCheck{
		{Body: []string{"renew hover domain"}, Candidates: []string{"Renew example.com on Hover"}},
		{Body: []string{"call mom"}, Candidates: []string{"email dad", "call the bank"}},
		{Body: []string{"no candidates"}},
	}
	res, _, err := InboxClassifier{Client: newTestClient(srv)}.CheckDuplicates(context.Background(), checks)
	if err != nil {
		t.Fatalf("CheckDuplicates: %v", err)
	}
	if len(got.Questions) != 3 || got.Questions["dup_1_1"].Type != TypeNoul {
		t.Errorf("questions = %v", got.Questions)
	}
	if res[0].Probabilities[0] != 0.92 || res[1].Probabilities[0] != -1 || res[1].Probabilities[1] != 0.1 || len(res[2].Probabilities) != 0 {
		t.Errorf("results = %+v", res)
	}
}

func TestCheckDuplicatesNoCandidatesSendsNothing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("unexpected request")
	}))
	defer srv.Close()
	res, _, err := InboxClassifier{Client: newTestClient(srv)}.CheckDuplicates(context.Background(), []DupCheck{{Body: []string{"x"}}})
	if err != nil || len(res) != 1 {
		t.Fatalf("res=%v err=%v", res, err)
	}
}

func TestProposeInboxLeanAsksDateDetailsOnlyForDatedCaptures(t *testing.T) {
	var reqs []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		reqs = append(reqs, body)
		if len(reqs) == 1 {
			io.WriteString(w, `{"answers":{
				"action_0":{"type":"choice","choice":"task","confidence":0.9},
				"date_mode_0":{"type":"choice","choice":"none","confidence":0.9},
				"action_1":{"type":"choice","choice":"task","confidence":0.9},
				"date_mode_1":{"type":"choice","choice":"relative","confidence":0.8},
				"dest_0":{"type":"choice","choice":"none"},"dest_1":{"type":"choice","choice":"none"}},
				"usage":{"input_tokens":50}}`)
			return
		}
		// Second request: capture 1 re-indexed as 0.
		io.WriteString(w, `{"answers":{
			"date_anchor_0":{"type":"choice","choice":"tomorrow","confidence":0.7},
			"date_role_0":{"type":"choice","choice":"due","confidence":0.9}},
			"usage":{"input_tokens":20}}`)
	}))
	defer srv.Close()

	captured := time.Date(2026, 10, 6, 7, 0, 0, 0, time.UTC)
	caps := []InboxCapture{{Body: []string{"milk"}, Captured: captured}, {Body: []string{"pay bill tomorrow"}, Captured: captured}}
	f := InboxFanout{Destinations: []InboxDestination{{Key: "Globex", Description: "Globex Corporation"}}, Dates: true, Lean: true}
	res, usage, err := InboxClassifier{Client: newTestClient(srv)}.ProposeInbox(context.Background(), caps, f)
	if err != nil {
		t.Fatal(err)
	}
	if len(reqs) != 2 {
		t.Fatalf("requests = %d, want 2", len(reqs))
	}
	q1 := reqs[0]["questions"].(map[string]any)
	if len(q1) != 6 { // 2 × (action, dest, date_mode)
		t.Errorf("first request questions = %d, want 6: %v", len(q1), q1)
	}
	st := reqs[0]["state"].(map[string]any)
	if st["destinations"].(map[string]any)["Globex"] != "Globex Corporation" {
		t.Errorf("lean state destinations = %v", st["destinations"])
	}
	crit := q1["dest_0"].(map[string]any)["criteria"].(map[string]any)
	if !strings.Contains(crit["Globex"].(string), "`destinations.Globex`") {
		t.Errorf("lean criteria should point at state: %v", crit["Globex"])
	}
	q2 := reqs[1]["questions"].(map[string]any)
	if len(q2) != 6 { // role, month, day, anchor, weekday, week for one capture
		t.Errorf("second request questions = %d, want 6: %v", len(q2), q2)
	}
	caps2 := reqs[1]["state"].(map[string]any)["captures"].([]any)
	if len(caps2) != 1 || caps2[0].(map[string]any)["text"] != "pay bill tomorrow" {
		t.Errorf("second request captures = %v", caps2)
	}
	rd := ResolveDate(res[1].Date, captured)
	if rd.Status != DateOK || rd.Date.Day() != 7 || rd.Confidence != 0.7 {
		t.Errorf("resolved = %+v (parts %+v)", rd, res[1].Date)
	}
	if res[0].Date.Mode.Value != "none" || usage.InputTokens != 70 {
		t.Errorf("res[0] date = %+v, usage = %+v", res[0].Date, usage)
	}
}

func TestProposeInboxLeanDateFailureIsPerItem(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			io.WriteString(w, `{"answers":{
				"action_0":{"type":"choice","choice":"task","confidence":0.9},
				"date_mode_0":{"type":"choice","choice":"relative","confidence":0.9},
				"action_1":{"type":"choice","choice":"archive","confidence":0.9},
				"date_mode_1":{"type":"choice","choice":"relative","confidence":0.9}}}`)
			return
		}
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	captured := time.Date(2026, 10, 6, 7, 0, 0, 0, time.UTC)
	caps := []InboxCapture{{Body: []string{"pay bill tomorrow"}, Captured: captured}, {Body: []string{"sale ends tonight"}, Captured: captured}}
	res, _, err := InboxClassifier{Client: newTestClient(srv)}.ProposeInbox(context.Background(), caps, InboxFanout{Dates: true, Lean: true})
	if err != nil {
		t.Fatalf("stage-2 failure should not fail the batch: %v", err)
	}
	if res[0].Action != "task" || res[0].DateErr == nil {
		t.Errorf("res[0] = %+v, want task with DateErr", res[0])
	}
	if res[1].DateErr != nil {
		t.Errorf("archive proposal should not be sent for date details: %+v", res[1])
	}
}

func TestProposeInboxRejectsBadDestinations(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("unexpected request")
	}))
	defer srv.Close()
	for _, dests := range [][]InboxDestination{
		{{Key: "none"}},
		{{Key: "Acme Corp"}},
		{{Key: "acme"}, {Key: "ACME"}},
		{{Key: "a.b"}},
	} {
		_, _, err := InboxClassifier{Client: newTestClient(srv)}.ProposeInbox(context.Background(), []InboxCapture{{Body: []string{"x"}}}, InboxFanout{Destinations: dests})
		if err == nil {
			t.Errorf("destinations %v: want error", dests)
		}
	}
}
