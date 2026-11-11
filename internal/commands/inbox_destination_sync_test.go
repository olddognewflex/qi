package commands

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"qi/internal/config"
	"qi/internal/index"
	"qi/internal/service"
	qisync "qi/internal/sync"
	"qi/internal/tui"
	"qi/internal/vault"
)

func TestInboxAcceptedDestinationSurvivesSync(t *testing.T) {
	for _, dest := range []string{"portal", "acme"} {
		t.Run(dest, func(t *testing.T) {
			cfg := routedInboxConfig(t)
			cfg.Inbox.Classifier = config.InboxClassifierTypeSafe
			for _, p := range cfg.Projects {
				if err := os.MkdirAll(filepath.Dir(p.File), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("QI_TEST_TYPESAFE_KEY", "sk-test")
			t.Setenv("XDG_DATA_HOME", t.TempDir())
			capture := filepath.Join(cfg.InboxPath, "01-idea.md")
			if err := os.WriteFile(capture, []byte("2026-11-11 10:00:00\nFix portal login #urgent\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			stub := stubInboxClassifierFor(t, service.InboxClassification{Action: service.InboxActionTask, Confidence: 0.9})
			stub.dest, stub.destConf = dest, 0.9
			orig := triageInbox
			t.Cleanup(func() { triageInbox = orig })
			triageInbox = func(cards []tui.InboxCard) ([]tui.InboxDecision, error) {
				decisions := make([]tui.InboxDecision, len(cards))
				decisions[0] = tui.InboxDecision{Action: cards[0].Proposed, Destination: cards[0].Destination}
				return decisions, nil
			}
			if _, _, err := runInboxCmd(t, cfg); err != nil {
				t.Fatal(err)
			}
			idx, err := index.Open()
			if err != nil {
				t.Fatal(err)
			}
			defer idx.Close()
			if _, err := qisync.Reconcile(cfg, idx, false); err != nil {
				t.Fatal(err)
			}
			var projection string
			for _, p := range cfg.Projects {
				if p.Project == dest {
					projection = p.File
				}
			}
			tasks, err := vault.ReadTasks(projection)
			if err != nil {
				t.Fatal(err)
			}
			if len(tasks) != 1 {
				t.Fatalf("accepted destination %q received %d tasks after sync; want 1", dest, len(tasks))
			}
			if tasks[0].Project != dest || !reflect.DeepEqual(tasks[0].Tags, []string{dest, "urgent"}) {
				t.Errorf("project=%q tags=%v; want %q with urgent preserved", tasks[0].Project, tasks[0].Tags, dest)
			}
		})
	}
}
