package commands

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"qi/internal/config"
	"qi/internal/service"
	"qi/internal/tui"
	"qi/internal/typesafe"
)

// stubInboxClassifier returns a fixed verdict per capture (with dest /
// destConf as its destination answer, and date as its date) and counts the
// captures it was sent (calls) and the batch requests (batches), recording
// the destinations offered per batch.
type stubInboxClassifier struct {
	mu       sync.Mutex
	cls      service.InboxClassification
	dest     string
	destConf float64
	date     *service.InboxDate
	calls    int
	batches  int
	offered  [][]service.InboxDestination
}

func (s *stubInboxClassifier) ClassifyInbox(_ context.Context, caps []service.InboxCapture, dests []service.InboxDestination) ([]service.InboxVerdict, error) {
	s.mu.Lock()
	s.calls += len(caps)
	s.batches++
	s.offered = append(s.offered, dests)
	s.mu.Unlock()
	out := make([]service.InboxVerdict, len(caps))
	for i := range out {
		out[i].InboxClassification = s.cls
		out[i].Destination, out[i].DestinationConfidence = s.dest, s.destConf
		out[i].Date = s.date
	}
	return out, nil
}

// inboxTestConfig builds a config over a temp vault holding two captures: one
// the heuristic calls a task (short line) and one with an explicit marker.
func inboxTestConfig(t *testing.T) config.Config {
	t.Helper()
	vault := t.TempDir()
	inbox := filepath.Join(vault, "00-inbox")
	if err := os.MkdirAll(inbox, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"01-idea.md":   "caching layer could reuse the index",
		"02-marker.md": "todo: renew passport",
	} {
		if err := os.WriteFile(filepath.Join(inbox, name), []byte("2026-06-11 09:00:00\n\n"+body+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return config.Config{
		VaultPath:    vault,
		InboxPath:    inbox,
		NotesPath:    filepath.Join(vault, "20-notes"),
		TaskFilePath: filepath.Join(vault, "10-tasks", "inbox.md"),
		TypeSafe:     config.TypeSafeConfig{APIKeyEnv: "QI_TEST_TYPESAFE_KEY"},
		Inbox:        config.InboxConfig{Classifier: config.InboxClassifierHeuristic, MinConfidence: 0.5, ClassifyLimit: config.DefaultInboxClassifyLimit, DuplicateCheck: true},
	}
}

func stubInboxClassifierFor(t *testing.T, cls service.InboxClassification) *stubInboxClassifier {
	t.Helper()
	stub := &stubInboxClassifier{cls: cls}
	prev := buildInboxClassifier
	buildInboxClassifier = func(config.Config, string) service.InboxClassifier { return stub }
	t.Cleanup(func() { buildInboxClassifier = prev })
	return stub
}

func runInboxCmd(t *testing.T, cfg config.Config, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	cmd := newInboxCommand(cfg)
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)
	err = cmd.Execute()
	return out.String(), errOut.String(), err
}

func TestInboxDryRunTypeSafeRefines(t *testing.T) {
	cfg := inboxTestConfig(t)
	t.Setenv("QI_TEST_TYPESAFE_KEY", "sk-test")
	stub := stubInboxClassifierFor(t, service.InboxClassification{Action: service.InboxActionNote, Confidence: 0.87})

	out, errOut, err := runInboxCmd(t, cfg, "--dry-run", "--classifier", "typesafe")
	if err != nil {
		t.Fatalf("inbox: %v", err)
	}
	if !strings.Contains(out, "note    caching layer could reuse the index  (typesafe: note (confidence 0.87))") {
		t.Errorf("idea not refined:\n%s", out)
	}
	if !strings.Contains(out, "task    todo: renew passport  (contains a task marker)") {
		t.Errorf("marker capture should keep its heuristic:\n%s", out)
	}
	if stub.calls != 1 {
		t.Errorf("classifier calls = %d, want 1 (marker capture never sent)", stub.calls)
	}
	// Progress and summary go to stderr only; stdout above is the clean
	// dry-run listing.
	wantErr := "inbox: typesafe: classifying 1 capture(s) in 1 batch(es)…\n" +
		"inbox: typesafe: refined 1, unsure 0, failed 0\n"
	if errOut != wantErr {
		t.Errorf("stderr = %q, want %q", errOut, wantErr)
	}
	if strings.Contains(out, "inbox: typesafe") {
		t.Errorf("progress leaked to stdout:\n%s", out)
	}
}

func TestInboxDryRunTypeSafeFromConfig(t *testing.T) {
	cfg := inboxTestConfig(t)
	cfg.Inbox.Classifier = config.InboxClassifierTypeSafe
	t.Setenv("QI_TEST_TYPESAFE_KEY", "sk-test")
	stub := stubInboxClassifierFor(t, service.InboxClassification{Action: service.InboxActionNote, Confidence: 0.9})

	if _, _, err := runInboxCmd(t, cfg, "--dry-run"); err != nil {
		t.Fatal(err)
	}
	if stub.calls != 1 {
		t.Errorf("config classifier not used: calls = %d", stub.calls)
	}

	// --classifier heuristic overrides the config back off.
	stub.calls = 0
	if _, _, err := runInboxCmd(t, cfg, "--dry-run", "--classifier", "heuristic"); err != nil {
		t.Fatal(err)
	}
	if stub.calls != 0 {
		t.Errorf("heuristic flag still called the classifier %d time(s)", stub.calls)
	}
}

func TestInboxDryRunTypeSafeMissingKeyFallsBack(t *testing.T) {
	cfg := inboxTestConfig(t)
	t.Setenv("QI_TEST_TYPESAFE_KEY", "")
	stub := stubInboxClassifierFor(t, service.InboxClassification{Action: service.InboxActionNote, Confidence: 0.9})

	out, errOut, err := runInboxCmd(t, cfg, "--dry-run", "--classifier", "typesafe")
	if err != nil {
		t.Fatalf("inbox: %v", err)
	}
	if !strings.Contains(errOut, "inbox: QI_TEST_TYPESAFE_KEY unset; using heuristic proposals") {
		t.Errorf("missing-key warning absent: %q", errOut)
	}
	if !strings.Contains(out, "task    caching layer could reuse the index  (short single-line capture reads as a task)") {
		t.Errorf("heuristic proposal expected:\n%s", out)
	}
	if stub.calls != 0 {
		t.Errorf("classifier called %d time(s) without a key", stub.calls)
	}
}

func TestInboxRejectsUnknownClassifier(t *testing.T) {
	_, _, err := runInboxCmd(t, inboxTestConfig(t), "--dry-run", "--classifier", "llm")
	if err == nil || !strings.Contains(err.Error(), `unknown --classifier "llm"`) {
		t.Fatalf("err = %v", err)
	}
}

// writeCaptures adds n heuristic-guess captures to cfg's inbox.
func writeCaptures(t *testing.T, cfg config.Config, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		name := filepath.Join(cfg.InboxPath, fmt.Sprintf("10-extra-%03d.md", i))
		if err := os.WriteFile(name, []byte(fmt.Sprintf("2026-06-11 09:00:00\n\nextra thought %03d\n", i)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestInboxDryRunClassifyLimit(t *testing.T) {
	cfg := inboxTestConfig(t)
	writeCaptures(t, cfg, 29) // 30 eligible with the idea capture, plus the marker
	t.Setenv("QI_TEST_TYPESAFE_KEY", "sk-test")
	stub := stubInboxClassifierFor(t, service.InboxClassification{Action: service.InboxActionNote, Confidence: 0.4})

	out, errOut, err := runInboxCmd(t, cfg, "--dry-run", "--classifier", "typesafe", "--classify-limit", "26")
	if err != nil {
		t.Fatalf("inbox: %v", err)
	}
	if stub.calls != 26 || stub.batches != 2 {
		t.Errorf("sent %d capture(s) in %d batch(es), want 26 in 2", stub.calls, stub.batches)
	}
	for _, want := range []string{
		"inbox: typesafe: classifying 26 capture(s) in 2 batch(es)…",
		"inbox: typesafe: 4 more capture(s) over classify_limit 26 keep heuristic proposals",
		"inbox: typesafe: refined 0, unsure 26, failed 0",
	} {
		if !strings.Contains(errOut, want) {
			t.Errorf("stderr missing %q:\n%s", want, errOut)
		}
	}
	if n := strings.Count(out, "; not classified (classify_limit)"); n != 4 {
		t.Errorf("%d capture(s) marked over the limit, want 4:\n%s", n, out)
	}

	// The config limit applies without the flag; --classify-limit 0 lifts it.
	cfg.Inbox.ClassifyLimit = 5
	stub.calls, stub.batches = 0, 0
	if _, _, err := runInboxCmd(t, cfg, "--dry-run", "--classifier", "typesafe"); err != nil {
		t.Fatal(err)
	}
	if stub.calls != 5 {
		t.Errorf("config limit: sent %d, want 5", stub.calls)
	}
	stub.calls, stub.batches = 0, 0
	if _, _, err := runInboxCmd(t, cfg, "--dry-run", "--classifier", "typesafe", "--classify-limit", "0"); err != nil {
		t.Fatal(err)
	}
	if stub.calls != 30 {
		t.Errorf("--classify-limit 0: sent %d, want 30", stub.calls)
	}
}

func TestInboxRejectsNegativeClassifyLimit(t *testing.T) {
	_, _, err := runInboxCmd(t, inboxTestConfig(t), "--dry-run", "--classify-limit", "-1")
	if err == nil || !strings.Contains(err.Error(), "--classify-limit -1 must be >= 0") {
		t.Fatalf("err = %v", err)
	}
}

func TestInboxClassifyBudget(t *testing.T) {
	cases := []struct {
		batches int
		dup     bool
		want    time.Duration
	}{
		// Each round of 4 batches gets two requests' time: the action
		// request, then the lean date details.
		{0, false, 60 * time.Second},
		{1, false, 60 * time.Second},
		{4, false, 60 * time.Second},
		{5, false, 120 * time.Second},
		{12, false, 3 * time.Minute},
		{100, false, 3 * time.Minute},
		// The duplicate check adds a third request per batch.
		{1, true, 90 * time.Second},
		{4, true, 90 * time.Second},
		{5, true, 3 * time.Minute},
	}
	for _, tc := range cases {
		if got := inboxClassifyBudget(tc.batches, tc.dup); got != tc.want {
			t.Errorf("inboxClassifyBudget(%d, %v) = %s, want %s", tc.batches, tc.dup, got, tc.want)
		}
	}
}

func TestInboxDryRunCollapsesRepeatsBeforeClassifying(t *testing.T) {
	cfg := inboxTestConfig(t)
	for i, body := range []string{
		"Email: PR merged — Bot [read_now]",
		"Email: PR merged — Bot [read_now]",
		"Email: PR merged — Bot [read_now]",
		"Email: Contract question — Pat Lee [reply_needed]",
	} {
		name := filepath.Join(cfg.InboxPath, fmt.Sprintf("1%d-email.md", i))
		if err := os.WriteFile(name, []byte("2026-06-11 09:00:00\n\n"+body+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("QI_TEST_TYPESAFE_KEY", "sk-test")
	stub := stubInboxClassifierFor(t, service.InboxClassification{Action: service.InboxActionNote, Confidence: 0.1})

	out, _, err := runInboxCmd(t, cfg, "--dry-run", "--classifier", "typesafe")
	if err != nil {
		t.Fatalf("inbox: %v", err)
	}
	if !strings.Contains(out, "archive ×3 Email: PR merged — Bot [read_now]  (email tag [read_now]") {
		t.Errorf("repeats not collapsed with a count:\n%s", out)
	}
	if !strings.Contains(out, "task    Email: Contract question — Pat Lee [reply_needed]  (email tag [reply_needed]") {
		t.Errorf("email tag heuristic not applied:\n%s", out)
	}
	if !strings.Contains(out, "4 distinct of 6 capture(s)") {
		t.Errorf("summary should count distinct vs total:\n%s", out)
	}
	// idea + 2 distinct emails; the marker capture is authoritative.
	if stub.calls != 3 {
		t.Errorf("classifier saw %d captures, want 3 (repeats sent once)", stub.calls)
	}
}

func TestInboxApplyCollapsedRowAppliesEveryCapture(t *testing.T) {
	cfg := inboxTestConfig(t)
	var paths []string
	for i := 0; i < 3; i++ {
		p := filepath.Join(cfg.InboxPath, fmt.Sprintf("1%d-email.md", i))
		if err := os.WriteFile(p, []byte("2026-06-11 09:00:00\n\nEmail: Contract question — Pat Lee [reply_needed]\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	orig := triageInbox
	t.Cleanup(func() { triageInbox = orig })
	var seen []int
	triageInbox = func(cards []tui.InboxCard) ([]tui.InboxDecision, error) {
		actions := make([]tui.InboxDecision, len(cards))
		for i, c := range cards {
			seen = append(seen, c.Count)
			if strings.HasPrefix(c.Summary, "Email:") {
				actions[i].Action = service.InboxActionTask
				os.Remove(paths[2]) // one repeat vanishes before apply
			}
		}
		return actions, nil
	}

	out, _, err := runInboxCmd(t, cfg)
	if err != nil {
		t.Fatalf("inbox: %v\n%s", err, out)
	}
	if !strings.Contains(out, "task    ×3 Email: Contract question") {
		t.Errorf("collapsed row not applied:\n%s", out)
	}
	if !strings.Contains(out, "2 applied, 2 skipped.") {
		t.Errorf("want 2 applied (one repeat vanished) and the 2 other captures skipped:\n%s", out)
	}
	for _, p := range paths[:2] {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s still in inbox", p)
		}
	}
	tasks, _ := os.ReadFile(cfg.TaskFilePath)
	if n := strings.Count(string(tasks), "Contract question"); n != 1 {
		t.Errorf("task file has %d copies, want 1:\n%s", n, tasks)
	}
	if fmt.Sprint(seen) != "[1 1 3]" && fmt.Sprint(seen) != "[3 1 1]" && fmt.Sprint(seen) != "[1 3 1]" {
		t.Errorf("card counts = %v, want one ×3 card and two singles", seen)
	}
}

// writeDatedCapture adds a capture written Monday 2026-11-09 to cfg's inbox.
func writeDatedCapture(t *testing.T, cfg config.Config, text string) string {
	t.Helper()
	p := filepath.Join(cfg.InboxPath, "2026-11-09-081500.md")
	if err := os.WriteFile(p, []byte("2026-11-09 08:15:00\n\n"+text+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestTypeSafeAdapterResolvesDateFromCaptureTime(t *testing.T) {
	// The model only answers date parts; the adapter does the calendar math
	// against the capture's own time. "tomorrow" written Monday is Tuesday,
	// whatever day triage runs.
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		io.WriteString(w, `{"model":"jev","answers":{
			"action_0":{"type":"choice","choice":"task","confidence":0.9},
			"date_mode_0":{"type":"choice","choice":"relative","confidence":0.9},
			"date_anchor_0":{"type":"choice","choice":"tomorrow","confidence":0.8},
			"date_role_0":{"type":"choice","choice":"due","confidence":0.7},
			"action_1":{"type":"choice","choice":"task","confidence":0.9},
			"date_mode_1":{"type":"choice","choice":"none","confidence":0.9}
		}}`)
	}))
	defer srv.Close()
	c := typesafeInboxClassifier{typesafe.InboxClassifier{Client: typesafe.NewClient(srv.URL, "sk-test", "", srv.Client())}}

	monday := time.Date(2026, 11, 9, 8, 15, 0, 0, time.Local)
	got, err := c.ClassifyInbox(context.Background(), []service.InboxCapture{
		{Body: []string{"pay bill tomorrow"}, Captured: monday},
		{Body: []string{"buy milk"}, Captured: monday},
	}, nil)
	if err != nil {
		t.Fatalf("classify: %v", err)
	}
	if requests != 2 {
		t.Errorf("requests = %d, want 2 (lean: date details only for the dated task)", requests)
	}
	want := service.InboxDate{Date: time.Date(2026, 11, 10, 0, 0, 0, 0, time.Local), Role: service.InboxDateDue, Confidence: 0.7}
	if got[0].Date == nil || *got[0].Date != want {
		t.Errorf("date = %+v, want %+v", got[0].Date, want)
	}
	if got[1].Date != nil {
		t.Errorf("undated capture got date %+v", *got[1].Date)
	}
}

func TestTypeSafeAdapterNoDateWhenUnresolved(t *testing.T) {
	// Unknown capture time → ResolveDate says review → no date, not a guess.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"model":"jev","answers":{
			"action_0":{"type":"choice","choice":"task","confidence":0.9},
			"date_mode_0":{"type":"choice","choice":"relative","confidence":0.9},
			"date_anchor_0":{"type":"choice","choice":"tomorrow","confidence":0.9},
			"date_role_0":{"type":"choice","choice":"due","confidence":0.9}
		}}`)
	}))
	defer srv.Close()
	c := typesafeInboxClassifier{typesafe.InboxClassifier{Client: typesafe.NewClient(srv.URL, "sk-test", "", srv.Client())}}
	got, err := c.ClassifyInbox(context.Background(), []service.InboxCapture{{Body: []string{"pay bill tomorrow"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Date != nil {
		t.Errorf("date = %+v, want none for an unknown capture time", *got[0].Date)
	}
}

func TestInboxDryRunShowsProposedDate(t *testing.T) {
	cfg := inboxTestConfig(t)
	writeDatedCapture(t, cfg, "pay bill tomorrow")
	t.Setenv("QI_TEST_TYPESAFE_KEY", "sk-test")
	stubInboxClassifierFor(t, service.InboxClassification{Action: service.InboxActionTask, Confidence: 0.9}).date =
		&service.InboxDate{Date: time.Date(2026, 11, 10, 0, 0, 0, 0, time.Local), Role: service.InboxDateDue, Confidence: 0.8}

	out, errOut, err := runInboxCmd(t, cfg, "--dry-run", "--classifier", "typesafe")
	if err != nil {
		t.Fatalf("inbox: %v", err)
	}
	if !strings.Contains(out, "task    pay bill tomorrow  [due Tue 2026-11-10]  (typesafe: task (confidence 0.90))") {
		t.Errorf("dry-run should show the proposed date:\n%s", out)
	}
	// The marker capture is never sent, so it gets no date.
	if !strings.Contains(out, "task    todo: renew passport  (contains a task marker)") {
		t.Errorf("unsent capture must stay undated:\n%s", out)
	}
	if !strings.Contains(errOut, "dated 2") { // the idea + the bill, both proposed as tasks by the stub
		t.Errorf("summary should count dated tasks: %q", errOut)
	}
}

func TestInboxDryRunDateBelowFloorNotShown(t *testing.T) {
	cfg := inboxTestConfig(t)
	cfg.Inbox.MinDateConfidence = 0.9
	writeDatedCapture(t, cfg, "pay bill tomorrow")
	t.Setenv("QI_TEST_TYPESAFE_KEY", "sk-test")
	stubInboxClassifierFor(t, service.InboxClassification{Action: service.InboxActionTask, Confidence: 0.9}).date =
		&service.InboxDate{Date: time.Date(2026, 11, 10, 0, 0, 0, 0, time.Local), Role: service.InboxDateDue, Confidence: 0.8}

	out, errOut, err := runInboxCmd(t, cfg, "--dry-run", "--classifier", "typesafe")
	if err != nil {
		t.Fatalf("inbox: %v", err)
	}
	if strings.Contains(out, "[due") || strings.Contains(errOut, "dated") {
		t.Errorf("date below min_date_confidence must not be proposed:\n%s\n%s", out, errOut)
	}
}

func TestInboxApplyDatedTask(t *testing.T) {
	for _, tc := range []struct {
		name  string
		clear bool
		want  string
	}{
		{"kept", false, "- [ ] pay bill tomorrow 📅 2026-11-10"},
		{"cleared", true, "- [ ] pay bill tomorrow ^"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := inboxTestConfig(t)
			writeDatedCapture(t, cfg, "pay bill tomorrow")
			t.Setenv("QI_TEST_TYPESAFE_KEY", "sk-test")
			cfg.Inbox.Classifier = config.InboxClassifierTypeSafe
			stubInboxClassifierFor(t, service.InboxClassification{Action: service.InboxActionTask, Confidence: 0.9}).date =
				&service.InboxDate{Date: time.Date(2026, 11, 10, 0, 0, 0, 0, time.Local), Role: service.InboxDateDue, Confidence: 0.8}
			orig := triageInbox
			t.Cleanup(func() { triageInbox = orig })
			triageInbox = func(cards []tui.InboxCard) ([]tui.InboxDecision, error) {
				out := make([]tui.InboxDecision, len(cards))
				for i, c := range cards {
					if c.Summary == "pay bill tomorrow" {
						if c.DateRole != "due" || !c.Date.Equal(time.Date(2026, 11, 10, 0, 0, 0, 0, time.Local)) {
							t.Errorf("card date = %s %v", c.DateRole, c.Date)
						}
						out[i] = tui.InboxDecision{Action: service.InboxActionTask, ClearDate: tc.clear}
					}
				}
				return out, nil
			}
			out, _, err := runInboxCmd(t, cfg)
			if err != nil {
				t.Fatalf("inbox: %v\n%s", err, out)
			}
			tasks, _ := os.ReadFile(cfg.TaskFilePath)
			if !strings.Contains(string(tasks), tc.want) {
				t.Errorf("task file = %q, want %q", tasks, tc.want)
			}
			if tc.clear == strings.Contains(out, "[due Tue 2026-11-10]") {
				t.Errorf("applied line date shown = %v, want %v:\n%s", !tc.clear, !tc.clear, out)
			}
		})
	}
}

func TestTypeSafeAdapterInNDays(t *testing.T) {
	// "in 3 days" written Monday → Thursday: the count arrives in the lean
	// date-detail request and the adapter adds it to the capture day.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"model":"jev","answers":{
			"action_0":{"type":"choice","choice":"task","confidence":0.9},
			"date_mode_0":{"type":"choice","choice":"relative","confidence":0.9},
			"date_anchor_0":{"type":"choice","choice":"in_n_days","confidence":0.8},
			"date_count_0":{"type":"choice","choice":"3","confidence":0.75},
			"date_role_0":{"type":"choice","choice":"due","confidence":0.9}
		}}`)
	}))
	defer srv.Close()
	c := typesafeInboxClassifier{typesafe.InboxClassifier{Client: typesafe.NewClient(srv.URL, "sk-test", "", srv.Client())}}
	monday := time.Date(2026, 11, 9, 8, 15, 0, 0, time.Local)
	got, err := c.ClassifyInbox(context.Background(), []service.InboxCapture{{Body: []string{"car registration renewal in 3 days"}, Captured: monday}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := service.InboxDate{Date: time.Date(2026, 11, 12, 0, 0, 0, 0, time.Local), Role: service.InboxDateDue, Confidence: 0.75}
	if got[0].Date == nil || *got[0].Date != want {
		t.Errorf("date = %+v, want %+v", got[0].Date, want)
	}
}

// stubDupChecker answers every candidate with prob and records the checks.
type stubDupChecker struct {
	mu     sync.Mutex
	prob   float64
	checks []service.InboxDupCheck
}

func (s *stubDupChecker) CheckDuplicates(_ context.Context, checks []service.InboxDupCheck) ([][]float64, error) {
	s.mu.Lock()
	s.checks = append(s.checks, checks...)
	s.mu.Unlock()
	out := make([][]float64, len(checks))
	for i, c := range checks {
		out[i] = make([]float64, len(c.Candidates))
		for k := range c.Candidates {
			out[i][k] = s.prob
		}
	}
	return out, nil
}

func stubDupCheckerFor(t *testing.T, prob float64) *stubDupChecker {
	t.Helper()
	stub := &stubDupChecker{prob: prob}
	prev := buildInboxDuplicateChecker
	buildInboxDuplicateChecker = func(config.Config, string) service.InboxDuplicateChecker { return stub }
	t.Cleanup(func() { buildInboxDuplicateChecker = prev })
	return stub
}

func writeOpenTasks(t *testing.T, cfg config.Config, lines ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(cfg.TaskFilePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.TaskFilePath, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestInboxDryRunDuplicateWarning(t *testing.T) {
	cases := []struct {
		name     string
		prob     float64
		wantWarn bool
	}{
		{"above floor", 0.92, true},
		{"at floor", 0.85, true},
		{"below floor", 0.84, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := inboxTestConfig(t)
			cfg.Inbox.MinDuplicateProbability = config.DefaultInboxMinDuplicateProbability
			writeOpenTasks(t, cfg, "- [ ] reuse the caching layer", "- [x] caching layer done", "- [ ] buy milk")
			t.Setenv("QI_TEST_TYPESAFE_KEY", "sk-test")
			stubInboxClassifierFor(t, service.InboxClassification{Action: service.InboxActionTask, Confidence: 0.9})
			dup := stubDupCheckerFor(t, tc.prob)

			out, errOut, err := runInboxCmd(t, cfg, "--dry-run", "--classifier", "typesafe")
			if err != nil {
				t.Fatalf("inbox: %v", err)
			}
			warn := `! possible duplicate of "reuse the caching layer"`
			if got := strings.Contains(out, warn); got != tc.wantWarn {
				t.Errorf("warning shown = %v, want %v:\n%s", got, tc.wantWarn, out)
			}
			// The proposal itself never changes.
			if !strings.Contains(out, "task    caching layer could reuse the index") {
				t.Errorf("proposal changed:\n%s", out)
			}
			// Only open tasks are candidates; the marker capture never leaves.
			if len(dup.checks) != 1 || fmt.Sprint(dup.checks[0].Candidates) != "[reuse the caching layer]" {
				t.Errorf("checks = %+v, want one check against the open task only", dup.checks)
			}
			flagged := 0
			if tc.wantWarn {
				flagged = 1
			}
			wantLine := fmt.Sprintf("inbox: typesafe: duplicate check: 1 task capture(s) checked, %d possible duplicate(s), 0 failed\n", flagged)
			if !strings.Contains(errOut, wantLine) {
				t.Errorf("stderr = %q, want %q", errOut, wantLine)
			}
		})
	}
}

func TestInboxDuplicateCheckSwitchedOff(t *testing.T) {
	cfg := inboxTestConfig(t)
	cfg.Inbox.DuplicateCheck = false
	writeOpenTasks(t, cfg, "- [ ] reuse the caching layer")
	t.Setenv("QI_TEST_TYPESAFE_KEY", "sk-test")
	stubInboxClassifierFor(t, service.InboxClassification{Action: service.InboxActionTask, Confidence: 0.9})
	dup := stubDupCheckerFor(t, 0.99)

	out, errOut, err := runInboxCmd(t, cfg, "--dry-run", "--classifier", "typesafe")
	if err != nil {
		t.Fatal(err)
	}
	if len(dup.checks) != 0 || strings.Contains(errOut, "open task") || strings.Contains(out, "possible duplicate") {
		t.Errorf("duplicate_check = false still checked: checks %v, stderr %q", dup.checks, errOut)
	}
}

func TestInboxDuplicateCheckDisclosedOnStderr(t *testing.T) {
	cfg := inboxTestConfig(t)
	writeOpenTasks(t, cfg, "- [ ] reuse the caching layer", "- [ ] buy milk")
	t.Setenv("QI_TEST_TYPESAFE_KEY", "sk-test")
	stubInboxClassifierFor(t, service.InboxClassification{Action: service.InboxActionTask, Confidence: 0.9})
	stubDupCheckerFor(t, 0.1)

	_, errOut, err := runInboxCmd(t, cfg, "--dry-run", "--classifier", "typesafe")
	if err != nil {
		t.Fatal(err)
	}
	if want := "checking task captures against 2 open task(s): shortlisted task text is sent too"; !strings.Contains(errOut, want) {
		t.Errorf("stderr = %q, want disclosure %q", errOut, want)
	}
}

func TestInboxNoDuplicateCheckWithoutOpenTasks(t *testing.T) {
	cfg := inboxTestConfig(t)
	writeOpenTasks(t, cfg, "- [x] reuse the caching layer")
	t.Setenv("QI_TEST_TYPESAFE_KEY", "sk-test")
	stubInboxClassifierFor(t, service.InboxClassification{Action: service.InboxActionTask, Confidence: 0.9})
	dup := stubDupCheckerFor(t, 0.99)

	_, errOut, err := runInboxCmd(t, cfg, "--dry-run", "--classifier", "typesafe")
	if err != nil {
		t.Fatal(err)
	}
	if len(dup.checks) != 0 || strings.Contains(errOut, "duplicate") {
		t.Errorf("duplicate check ran with no open tasks: checks %v, stderr %q", dup.checks, errOut)
	}
}

func TestInboxDuplicateReachesTriageAndArchiveNote(t *testing.T) {
	cfg := inboxTestConfig(t)
	cfg.Inbox.MinDuplicateProbability = 0.85
	writeOpenTasks(t, cfg, "- [ ] reuse the caching layer")
	t.Setenv("QI_TEST_TYPESAFE_KEY", "sk-test")
	stubInboxClassifierFor(t, service.InboxClassification{Action: service.InboxActionTask, Confidence: 0.9})
	stubDupCheckerFor(t, 0.95)

	orig := triageInbox
	t.Cleanup(func() { triageInbox = orig })
	triageInbox = func(cards []tui.InboxCard) ([]tui.InboxDecision, error) {
		actions := make([]tui.InboxDecision, len(cards))
		for i, c := range cards {
			if c.DuplicateOf != "" {
				if c.DuplicateOf != "reuse the caching layer" || c.DuplicateProbability != 0.95 || c.Proposed != service.InboxActionTask {
					t.Errorf("card = %+v", c)
				}
				actions[i] = tui.InboxDecision{Action: service.InboxActionArchive} // what u returns
			}
		}
		return actions, nil
	}
	out, _, err := runInboxCmd(t, cfg, "--classifier", "typesafe")
	if err != nil {
		t.Fatalf("inbox: %v\n%s", err, out)
	}
	if !strings.Contains(out, `archive caching layer could reuse the index  (possible duplicate of "reuse the caching layer" (0.95))`) {
		t.Errorf("archived duplicate not reported:\n%s", out)
	}
	tasks, _ := os.ReadFile(cfg.TaskFilePath)
	if strings.Count(string(tasks), "- [ ]") != 1 {
		t.Errorf("task file changed:\n%s", tasks)
	}
}

func TestTypeSafeAdapterCheckDuplicates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"answers": {"dup_0_0": {"type": "noul", "noul": 0.88}}}`)
	}))
	defer srv.Close()
	cfg := config.Config{TypeSafe: config.TypeSafeConfig{URL: srv.URL}}
	got, err := buildInboxDuplicateChecker(cfg, "sk-test").CheckDuplicates(context.Background(),
		[]service.InboxDupCheck{{Body: []string{"renew my passport"}, Candidates: []string{"renew passport", "passport photos"}}})
	if err != nil {
		t.Fatalf("CheckDuplicates: %v", err)
	}
	if fmt.Sprint(got) != "[[0.88 -1]]" {
		t.Errorf("got %v, want [[0.88 -1]]", got)
	}
}
