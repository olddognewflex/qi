package tui

import (
	"strings"
	"testing"
)

func TestTriageViewShowsReason(t *testing.T) {
	tests := []struct {
		name    string
		reason  string
		wantWhy bool
	}{
		{"classifier reason", "typesafe: task (confidence 0.91)", true},
		{"heuristic fallback", "short single-line capture reads as a task; typesafe unsure (note 0.42)", true},
		{"no reason", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := triageModel{
				cards:   []InboxCard{{Summary: "buy milk", Proposed: triageTask, Reason: tt.reason}},
				actions: make([]string, 1),
			}
			view := m.View()
			if got := strings.Contains(view, "why:"); got != tt.wantWhy {
				t.Fatalf("why line present = %v, want %v\n%s", got, tt.wantWhy, view)
			}
			if tt.wantWhy && !strings.Contains(view, tt.reason) {
				t.Fatalf("view missing reason %q\n%s", tt.reason, view)
			}
		})
	}
}
