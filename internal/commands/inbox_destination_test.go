package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"qi/internal/config"
	"qi/internal/service"
	"qi/internal/tui"
	"qi/internal/typesafe"
)

// routedInboxConfig is inboxTestConfig plus clients/projects loaded through
// config.LoadFrom (so the synthetic client projection is real): acme has a
// description, a task_file (synthetic projection) and a described "portal"
// project plus an invalid key; globex has no descriptions, a key colliding
// with portal, an all-digit tag and a plain "ops" project.
func routedInboxConfig(t *testing.T) config.Config {
	t.Helper()
	cfg := inboxTestConfig(t)
	clients := t.TempDir()
	path := filepath.Join(t.TempDir(), "config.toml")
	toml := `vault_path = "` + cfg.VaultPath + `"

[[client]]
name = "acme"
vault_path = "` + filepath.Join(clients, "acme") + `"
description = "Acme Corp consulting work"
task_file = "10-tasks/acme.md"

[[client.project]]
project = "portal"
description = "Acme customer portal"

[[client.project]]
project = "web.site"

[[client]]
name = "globex"
vault_path = "` + filepath.Join(clients, "globex") + `"

[[client.project]]
project = "Portal"

[[client.project]]
project = "2026"

[[client.project]]
project = "ops"
`
	if err := os.WriteFile(path, []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	cfg.Clients, cfg.Projects = loaded.Clients, loaded.Projects
	cfg.Inbox.MinDestinationConfidence = config.DefaultInboxMinDestinationConfidence
	return cfg
}

func TestInboxDestinations(t *testing.T) {
	var errOut bytes.Buffer
	got := inboxDestinations(routedInboxConfig(t), &errOut)
	want := []service.InboxDestination{
		{Key: "portal", Description: "Acme customer portal"},
		{Key: "ops", Description: "The ops project for client globex."},
		{Key: "acme", Description: "Acme Corp consulting work", Client: true},
		{Key: "globex", Description: "Client globex, when no one of its projects fits.", Client: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("destinations =\n%+v\nwant\n%+v", got, want)
	}
	if w := "inbox: typesafe: not offering destination(s) web.site, Portal, 2026 (keys must be unique letters, digits, - or _)\n"; errOut.String() != w {
		t.Errorf("warning = %q, want %q", errOut.String(), w)
	}

	errOut.Reset()
	if got := inboxDestinations(inboxTestConfig(t), &errOut); got != nil || errOut.Len() != 0 {
		t.Errorf("no clients: %v / %q, want nothing", got, errOut.String())
	}
}

// The adapter adds the lean destination fan-out only when destinations are
// offered; without them the request holds just the action and date_mode
// questions.
func TestTypeSafeInboxAdapterDestinationRequest(t *testing.T) {
	var got struct {
		State struct {
			Destinations map[string]string `json:"destinations"`
		} `json:"state"`
		Questions map[string]json.RawMessage `json:"questions"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.State.Destinations = nil
		got.Questions = nil
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode: %v", err)
		}
		io.WriteString(w, `{"model":"jev","answers":{
			"action_0":{"type":"choice","choice":"task","confidence":0.9},
			"dest_0":{"type":"choice","choice":"portal","confidence":0.8}}}`)
	}))
	defer srv.Close()
	c := typesafeInboxClassifier{typesafe.InboxClassifier{Client: typesafe.NewClient(srv.URL, "sk-test", "", srv.Client())}}

	dests := []service.InboxDestination{{Key: "portal", Description: "Acme customer portal"}, {Key: "acme", Description: "Acme", Client: true}}
	v, err := c.ClassifyInbox(context.Background(), []service.InboxCapture{{Body: []string{"fix portal login"}}}, dests)
	if err != nil {
		t.Fatalf("ClassifyInbox: %v", err)
	}
	if len(got.Questions) != 3 || got.Questions["dest_0"] == nil || got.Questions["date_mode_0"] == nil {
		t.Errorf("questions = %v, want action_0 + dest_0 + date_mode_0", keysOf(got.Questions))
	}
	if want := map[string]string{"portal": "Acme customer portal", "acme": "Acme"}; !reflect.DeepEqual(got.State.Destinations, want) {
		t.Errorf("state.destinations = %v, want %v (lean)", got.State.Destinations, want)
	}
	if v[0].Action != "task" || v[0].Destination != "portal" || v[0].DestinationConfidence != 0.8 {
		t.Errorf("verdict = %+v", v[0])
	}

	v, err = c.ClassifyInbox(context.Background(), []service.InboxCapture{{Body: []string{"fix portal login"}}}, nil)
	if err != nil {
		t.Fatalf("ClassifyInbox: %v", err)
	}
	if len(got.Questions) != 2 || got.Questions["date_mode_0"] == nil || got.State.Destinations != nil {
		t.Errorf("no destinations: questions %v, state.destinations %v; want action + date_mode only", keysOf(got.Questions), got.State.Destinations)
	}
	if v[0].Destination != "" {
		t.Errorf("verdict carries a destination it was not asked for: %+v", v[0])
	}
}

func keysOf(m map[string]json.RawMessage) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestInboxDryRunShowsDestination(t *testing.T) {
	cfg := routedInboxConfig(t)
	t.Setenv("QI_TEST_TYPESAFE_KEY", "sk-test")
	stub := stubInboxClassifierFor(t, service.InboxClassification{Action: service.InboxActionTask, Confidence: 0.9})
	stub.dest, stub.destConf = "portal", 0.8

	out, errOut, err := runInboxCmd(t, cfg, "--dry-run", "--classifier", "typesafe")
	if err != nil {
		t.Fatalf("inbox: %v", err)
	}
	if !strings.Contains(out, "task    caching layer could reuse the index → #portal  (typesafe: task (confidence 0.90); → #portal (0.80))") {
		t.Errorf("destination not shown:\n%s", out)
	}
	// The marker capture is never sent, so never routed.
	if !strings.Contains(out, "task    todo: renew passport  (contains a task marker)") {
		t.Errorf("marker capture changed:\n%s", out)
	}
	if !strings.Contains(errOut, "inbox: typesafe: refined 1, unsure 0, failed 0, routed 1\n") {
		t.Errorf("summary missing routed count: %q", errOut)
	}
	if len(stub.offered) != 1 || len(stub.offered[0]) != 4 {
		t.Errorf("offered = %v, want the 4 valid destinations", stub.offered)
	}

	// Below [inbox] min_destination_confidence: no proposal at all.
	stub.destConf = 0.69
	out, errOut, err = runInboxCmd(t, cfg, "--dry-run", "--classifier", "typesafe")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "→") || !strings.Contains(errOut, "routed 0") {
		t.Errorf("below floor still routed:\n%s\n%s", out, errOut)
	}
}

// With no clients configured the classifier is offered no destinations and
// the summary line is unchanged.
func TestInboxDryRunNoDestinationsConfigured(t *testing.T) {
	cfg := inboxTestConfig(t)
	t.Setenv("QI_TEST_TYPESAFE_KEY", "sk-test")
	stub := stubInboxClassifierFor(t, service.InboxClassification{Action: service.InboxActionTask, Confidence: 0.9})
	stub.dest, stub.destConf = "portal", 0.99

	out, errOut, err := runInboxCmd(t, cfg, "--dry-run", "--classifier", "typesafe")
	if err != nil {
		t.Fatal(err)
	}
	if len(stub.offered) != 1 || stub.offered[0] != nil {
		t.Errorf("offered = %v, want none", stub.offered)
	}
	if strings.Contains(out, "→") || strings.Contains(errOut, "routed") {
		t.Errorf("routed without destinations:\n%s\n%s", out, errOut)
	}
}

// The default heuristic path never asks for or shows a destination, even with
// clients configured.
func TestInboxDryRunHeuristicIgnoresDestinations(t *testing.T) {
	cfg := routedInboxConfig(t)
	stub := stubInboxClassifierFor(t, service.InboxClassification{Action: service.InboxActionTask, Confidence: 0.9})
	out, errOut, err := runInboxCmd(t, cfg, "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	want := "task    caching layer could reuse the index  (short single-line capture reads as a task)\n" +
		"task    todo: renew passport  (contains a task marker)\n" +
		"\n2 capture(s); run without --dry-run to triage.\n"
	if out != want || errOut != "" || stub.calls != 0 {
		t.Errorf("heuristic output changed:\n%q\nstderr %q, calls %d", out, errOut, stub.calls)
	}
}

// End to end: an accepted destination routes the task exactly like
// `qi task add --project/--client` — into the canon 10-tasks/<tag>.md with
// the #tag — and a cleared one lands in the default task file.
func TestInboxApplyRoutesTaskToDestination(t *testing.T) {
	cases := []struct {
		name     string
		dest     string
		clear    bool
		wantFile string
		wantLine string
		routed   bool
	}{
		{"project", "portal", false, "portal.md", "caching layer could reuse the index #portal", true},
		{"client", "acme", false, "acme.md", "caching layer could reuse the index #acme", true},
		{"cleared", "acme", true, "inbox.md", "caching layer could reuse the index ^", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := routedInboxConfig(t)
			t.Setenv("QI_TEST_TYPESAFE_KEY", "sk-test")
			cfg.Inbox.Classifier = config.InboxClassifierTypeSafe
			stub := stubInboxClassifierFor(t, service.InboxClassification{Action: service.InboxActionTask, Confidence: 0.9})
			stub.dest, stub.destConf = tc.dest, 0.8

			orig := triageInbox
			t.Cleanup(func() { triageInbox = orig })
			var cardDests []string
			triageInbox = func(cards []tui.InboxCard) ([]tui.InboxDecision, error) {
				out := make([]tui.InboxDecision, len(cards))
				for i, c := range cards {
					cardDests = append(cardDests, c.Destination)
					out[i] = tui.InboxDecision{Action: c.Proposed, Destination: c.Destination}
					if tc.clear {
						out[i].Destination = ""
					}
				}
				return out, nil
			}

			out, _, err := runInboxCmd(t, cfg)
			if err != nil {
				t.Fatalf("inbox: %v\n%s", err, out)
			}
			if !reflect.DeepEqual(cardDests, []string{tc.dest, ""}) {
				t.Errorf("card destinations = %q, want [%s \"\"]", cardDests, tc.dest)
			}
			tasksDir := filepath.Dir(cfg.TaskFilePath)
			data, err := os.ReadFile(filepath.Join(tasksDir, tc.wantFile))
			if err != nil {
				t.Fatalf("task file: %v", err)
			}
			if !strings.Contains(string(data), tc.wantLine) {
				t.Errorf("%s missing %q:\n%s", tc.wantFile, tc.wantLine, data)
			}
			// The unrouted marker task always goes to the default file.
			inbox, _ := os.ReadFile(cfg.TaskFilePath)
			if !strings.Contains(string(inbox), "todo: renew passport") {
				t.Errorf("marker task not in inbox.md:\n%s", inbox)
			}
			wantSummary := "\n2 applied, 0 skipped.\n"
			if tc.routed {
				wantSummary = "\n2 applied, 0 skipped, routed 1.\n"
				if !strings.Contains(out, "task    caching layer could reuse the index → #"+tc.dest+"\n") {
					t.Errorf("applied row missing destination:\n%s", out)
				}
			}
			if !strings.HasSuffix(out, wantSummary) {
				t.Errorf("summary: want suffix %q:\n%s", wantSummary, out)
			}
		})
	}
}
