package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// destFake answers each capture (keyed by joined body) with a fixed verdict
// and records the destinations it was offered per batch.
type destFake struct {
	mu       sync.Mutex
	verdicts map[string]InboxVerdict
	batchErr error
	offered  [][]InboxDestination
}

func (f *destFake) ClassifyInbox(_ context.Context, caps []InboxCapture, dests []InboxDestination) ([]InboxVerdict, error) {
	f.mu.Lock()
	f.offered = append(f.offered, dests)
	f.mu.Unlock()
	if f.batchErr != nil {
		return nil, f.batchErr
	}
	out := make([]InboxVerdict, len(caps))
	for i, c := range caps {
		out[i] = f.verdicts[strings.Join(c.Body, "\n")]
	}
	return out, nil
}

var testDestinations = []InboxDestination{
	{Key: "portal", Description: "Acme customer portal"},
	{Key: "acme", Description: "Acme Corp", Client: true},
}

func TestInboxRefineDestination(t *testing.T) {
	task := InboxClassification{Action: InboxActionTask, Confidence: 0.9}
	cases := []struct {
		name        string
		body        string
		v           InboxVerdict
		wantAction  string
		wantProject string
		wantClient  string
		wantReason  string
		wantRouted  int
	}{
		{
			name:        "confident project",
			body:        "fix portal login",
			v:           InboxVerdict{InboxClassification: task, Destination: "portal", DestinationConfidence: 0.82},
			wantAction:  InboxActionTask,
			wantProject: "portal",
			wantReason:  "typesafe: task (confidence 0.90); → #portal (0.82)",
			wantRouted:  1,
		},
		{
			name:       "confident client",
			body:       "send acme invoice",
			v:          InboxVerdict{InboxClassification: task, Destination: "acme", DestinationConfidence: 0.7},
			wantAction: InboxActionTask,
			wantClient: "acme",
			wantReason: "typesafe: task (confidence 0.90); → #acme (0.70)",
			wantRouted: 1,
		},
		{
			name:       "below floor proposes nothing",
			body:       "fix portal login",
			v:          InboxVerdict{InboxClassification: task, Destination: "portal", DestinationConfidence: 0.69},
			wantAction: InboxActionTask,
			wantReason: "typesafe: task (confidence 0.90)",
		},
		{
			name:       "none",
			body:       "buy milk",
			v:          InboxVerdict{InboxClassification: task, Destination: "none", DestinationConfidence: 0.95},
			wantAction: InboxActionTask,
			wantReason: "typesafe: task (confidence 0.90)",
		},
		{
			name:       "unknown key",
			body:       "buy milk",
			v:          InboxVerdict{InboxClassification: task, Destination: "globex", DestinationConfidence: 0.95},
			wantAction: InboxActionTask,
			wantReason: "typesafe: task (confidence 0.90)",
		},
		{
			name:       "non-task action",
			body:       "portal idea",
			v:          InboxVerdict{InboxClassification: InboxClassification{Action: InboxActionNote, Confidence: 0.9}, Destination: "portal", DestinationConfidence: 0.95},
			wantAction: InboxActionNote,
			wantReason: "typesafe: note (confidence 0.90)",
		},
		{
			// Unsure action, but the heuristic's settled task still routes:
			// the destination question is "if this were a task".
			name:        "heuristic task kept",
			body:        "fix portal login",
			v:           InboxVerdict{InboxClassification: InboxClassification{Action: InboxActionNote, Confidence: 0.3}, Destination: "portal", DestinationConfidence: 0.9},
			wantAction:  InboxActionTask,
			wantProject: "portal",
			wantReason:  "short single-line capture reads as a task; typesafe unsure (note 0.30); → #portal (0.90)",
			wantRouted:  1,
		},
		{
			name:       "per-capture error",
			body:       "fix portal login",
			v:          InboxVerdict{Destination: "portal", DestinationConfidence: 0.9, Err: errors.New("no answer")},
			wantAction: InboxActionTask,
			wantReason: "short single-line capture reads as a task",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &destFake{verdicts: map[string]InboxVerdict{tc.body: tc.v}}
			got, stats, _ := InboxService{}.Refine(context.Background(), []InboxItem{inboxItemFor(tc.body)}, fake, InboxRefineOptions{
				MinConfidence:            0.5,
				Destinations:             testDestinations,
				MinDestinationConfidence: 0.7,
			})
			it := got[0]
			if it.Action != tc.wantAction || it.Project != tc.wantProject || it.Client != tc.wantClient || it.Reason != tc.wantReason {
				t.Errorf("got %s project=%q client=%q %q\nwant %s project=%q client=%q %q",
					it.Action, it.Project, it.Client, it.Reason, tc.wantAction, tc.wantProject, tc.wantClient, tc.wantReason)
			}
			if stats.Routed != tc.wantRouted {
				t.Errorf("routed = %d, want %d", stats.Routed, tc.wantRouted)
			}
			if !reflect.DeepEqual(fake.offered, [][]InboxDestination{testDestinations}) {
				t.Errorf("offered = %v", fake.offered)
			}
		})
	}
}

func TestInboxRefineBatchErrorProposesNoDestination(t *testing.T) {
	fake := &destFake{batchErr: errors.New("boom")}
	got, stats, err := InboxService{}.Refine(context.Background(), []InboxItem{inboxItemFor("fix portal login")}, fake, InboxRefineOptions{
		MinConfidence: 0.5, Destinations: testDestinations, MinDestinationConfidence: 0.7,
	})
	if err == nil || got[0].Project != "" || got[0].Client != "" || stats.Routed != 0 {
		t.Errorf("batch error: %+v %+v %v", got[0], stats, err)
	}
}

// Without destinations the classifier is offered none, and a stray answer
// (there should be none) is ignored.
func TestInboxRefineNoDestinationsOffered(t *testing.T) {
	fake := &destFake{verdicts: map[string]InboxVerdict{"fix portal login": {
		InboxClassification: InboxClassification{Action: InboxActionTask, Confidence: 0.9},
		Destination:         "portal", DestinationConfidence: 0.99,
	}}}
	got, stats, _ := InboxService{}.Refine(context.Background(), []InboxItem{inboxItemFor("fix portal login")}, fake, InboxRefineOptions{MinConfidence: 0.5})
	if len(fake.offered) != 1 || fake.offered[0] != nil {
		t.Errorf("offered = %v, want one nil", fake.offered)
	}
	if got[0].Project != "" || stats.Routed != 0 {
		t.Errorf("routed without destinations: %+v %+v", got[0], stats)
	}
}

// The default path: no classifier leaves items exactly as List made them,
// destination options or not.
func TestInboxRefineNilClassifierIgnoresDestinations(t *testing.T) {
	in := []InboxItem{inboxItemFor("fix portal login"), inboxItemFor("idea", "more")}
	got, stats, err := InboxService{}.Refine(context.Background(), in, nil, InboxRefineOptions{
		MinConfidence: 0.5, Destinations: testDestinations, MinDestinationConfidence: 0,
	})
	if err != nil || !reflect.DeepEqual(got, in) || stats != (InboxRefineStats{}) {
		t.Errorf("nil classifier changed items: %+v %+v %v", got, stats, err)
	}
}

func newRoutingInboxService(t *testing.T) (InboxService, string) {
	t.Helper()
	vault := t.TempDir()
	inbox := filepath.Join(vault, "00-inbox")
	svc := InboxService{
		InboxDir:   inbox,
		ArchiveDir: filepath.Join(inbox, "archive"),
		Tasks:      NewTaskService(filepath.Join(vault, "10-tasks", "inbox.md")),
		Notes:      NewNoteService(filepath.Join(vault, "20-notes")),
		IsClient:   func(n string) bool { return n == "acme" },
	}
	return svc, vault
}

func TestApplyGroupRoutesTaskToDestination(t *testing.T) {
	cases := []struct {
		name     string
		project  string
		client   string
		wantFile string
		wantTag  string
	}{
		{"project", "portal", "", "portal.md", "#portal"},
		{"client", "", "acme", "acme.md", "#acme"},
		{"none", "", "", "inbox.md", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, vault := newRoutingInboxService(t)
			p := writeInboxCapture(t, svc.InboxDir, "01.md", "fix portal login")
			r := writeInboxCapture(t, svc.InboxDir, "02.md", "fix portal login")
			it := InboxItem{Path: p, Summary: "fix portal login", Action: InboxActionTask, Repeats: []string{r}, Project: tc.project, Client: tc.client}

			outs, err := svc.ApplyGroup(it, InboxActionTask)
			if err != nil {
				t.Fatalf("ApplyGroup: %v", err)
			}
			want := filepath.Join(vault, "10-tasks", tc.wantFile)
			if outs[0].Created != want {
				t.Errorf("created %s, want %s", outs[0].Created, want)
			}
			data, err := os.ReadFile(want)
			if err != nil {
				t.Fatal(err)
			}
			if n := strings.Count(string(data), "fix portal login"); n != 1 {
				t.Errorf("%d task lines, want 1 (repeat archived):\n%s", n, data)
			}
			if tc.wantTag != "" && !strings.Contains(string(data), "fix portal login "+tc.wantTag) {
				t.Errorf("task not tagged %s:\n%s", tc.wantTag, data)
			}
			if _, err := os.Stat(r); !os.IsNotExist(err) {
				t.Errorf("repeat still in inbox")
			}
		})
	}
}

// A destination never leaks into a note or archive.
func TestApplyGroupNonTaskIgnoresDestination(t *testing.T) {
	svc, vault := newRoutingInboxService(t)
	p := writeInboxCapture(t, svc.InboxDir, "01.md", "portal idea")
	if _, err := svc.ApplyGroup(InboxItem{Path: p, Project: "portal"}, InboxActionNote); err != nil {
		t.Fatalf("ApplyGroup note: %v", err)
	}
	if _, err := os.Stat(filepath.Join(vault, "10-tasks", "portal.md")); !os.IsNotExist(err) {
		t.Errorf("note created a project task file")
	}
}

func TestInboxApplyClientValidation(t *testing.T) {
	cases := []struct {
		name    string
		in      InboxApplyInput
		isNil   bool
		wantErr string
	}{
		{"unknown client", InboxApplyInput{Action: InboxActionTask, Client: "globex"}, false, `unknown client "globex"`},
		{"no client resolver", InboxApplyInput{Action: InboxActionTask, Client: "acme"}, true, `unknown client "acme"`},
		{"both", InboxApplyInput{Action: InboxActionTask, Client: "acme", Project: "portal"}, false, "mutually exclusive"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, _ := newRoutingInboxService(t)
			if tc.isNil {
				svc.IsClient = nil
			}
			tc.in.Path = writeInboxCapture(t, svc.InboxDir, "01.md", "send invoice")
			_, err := svc.Apply(tc.in)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want %q", err, tc.wantErr)
			}
			// Rejected before anything moved.
			if _, err := os.Stat(tc.in.Path); err != nil {
				t.Errorf("capture moved on a rejected apply: %v", err)
			}
		})
	}
}

func TestTaskRouteTag(t *testing.T) {
	for _, tc := range []struct{ project, client, want string }{
		{"portal", "", "portal"},
		{"", "acme", "acme"},
		{" portal ", "", "portal"},
		{"", "", ""},
	} {
		if got := TaskRouteTag(tc.project, tc.client); got != tc.want {
			t.Errorf("TaskRouteTag(%q, %q) = %q, want %q", tc.project, tc.client, got, tc.want)
		}
	}
}
