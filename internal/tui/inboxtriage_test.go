package tui

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func key(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "space":
		return tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "left":
		return tea.KeyMsg{Type: tea.KeyLeft}
	case "right":
		return tea.KeyMsg{Type: tea.KeyRight}
	case "pgdown":
		return tea.KeyMsg{Type: tea.KeyPgDown}
	case "pgup":
		return tea.KeyMsg{Type: tea.KeyPgUp}
	case "home":
		return tea.KeyMsg{Type: tea.KeyHome}
	case "end":
		return tea.KeyMsg{Type: tea.KeyEnd}
	case "ctrl+c":
		return tea.KeyMsg{Type: tea.KeyCtrlC}
	case "ctrl+d":
		return tea.KeyMsg{Type: tea.KeyCtrlD}
	case "ctrl+u":
		return tea.KeyMsg{Type: tea.KeyCtrlU}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

// press feeds keys to m and returns the final model plus the last command.
func press(t *testing.T, m triageModel, keys ...string) (triageModel, tea.Cmd) {
	t.Helper()
	var cmd tea.Cmd
	for _, k := range keys {
		var next tea.Model
		next, cmd = m.Update(key(k))
		m = next.(triageModel)
	}
	return m, cmd
}

func isQuit(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	_, ok := cmd().(tea.QuitMsg)
	return ok
}

func testCards(n int) []InboxCard {
	proposals := []string{triageTask, triageNote, triageArchive}
	cards := make([]InboxCard, n)
	for i := range cards {
		cards[i] = InboxCard{
			Summary:  fmt.Sprintf("capture %02d", i),
			Body:     []string{fmt.Sprintf("capture %02d", i), "second line"},
			Proposed: proposals[i%len(proposals)],
			Reason:   "rule",
		}
	}
	return cards
}

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
			m := newTriageModel([]InboxCard{{Summary: "buy milk", Proposed: triageTask, Reason: tt.reason}})
			m.view = viewCard
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

func TestSourceHint(t *testing.T) {
	tests := []struct {
		reason, want string
	}{
		{"typesafe: task (confidence 0.91)", "ts 0.91"},
		{"typesafe: note (confidence 0.5)", "ts 0.50"},
		{"typesafe: note", "ts"},
		{"short capture reads as a task; typesafe unsure (note 0.42)", "ts?"},
		{"long capture reads as a note; not classified (classify_limit)", "lim"},
		{"empty capture", "rule"},
		{"", "rule"},
	}
	for _, tt := range tests {
		t.Run(tt.reason, func(t *testing.T) {
			if got := sourceHint(tt.reason); got != tt.want {
				t.Fatalf("sourceHint(%q) = %q, want %q", tt.reason, got, tt.want)
			}
		})
	}
}

func TestTriageListRenders(t *testing.T) {
	cards := []InboxCard{
		{Summary: "buy milk", Proposed: triageTask, Reason: "typesafe: task (confidence 0.91)"},
		{Summary: "idea about gardens", Proposed: triageNote, Reason: "long; typesafe unsure (task 0.40)"},
		{Summary: "old receipt", Proposed: triageArchive, Reason: "x; not classified (classify_limit)"},
		{Summary: strings.Repeat("ü", 300), Proposed: triageTask, Reason: "short capture"},
	}
	m := newTriageModel(cards)
	m, _ = press(t, m, "d") // row 0 explicitly delete; cursor → row 1
	view := m.View()

	for _, want := range []string{
		"action", "capture", "source", // header
		"buy milk", "idea about gardens", "old receipt",
		"ts 0.91", "ts?", "lim", "rule",
		"delete",            // explicit decision
		"note?", "archive?", // untouched proposals are marked
		"2/4", "page 1/1", // position + page
		"delete 1", "skip 3 (3 undecided)",
		"…", // long summary truncated
	} {
		if !strings.Contains(view, want) {
			t.Errorf("list view missing %q\n%s", want, view)
		}
	}
	if strings.Contains(view, "delete?") {
		t.Errorf("decided row rendered as a proposal\n%s", view)
	}
	for _, line := range strings.Split(view, "\n") {
		if strings.Contains(line, "üü") && !strings.Contains(line, "…") {
			t.Errorf("long summary not truncated: %q", line)
		}
	}
}

func TestTriageListPagingKeepsCursorVisible(t *testing.T) {
	const height = 12 // 12 - listChrome = 5 rows per page
	tests := []struct {
		name       string
		keys       []string
		wantCursor int
		wantPage   string
	}{
		{"down past window", []string{"j", "j", "j", "j", "j", "j"}, 6, "page 2/"},
		{"page down", []string{"pgdown"}, 5, "page 2/"},
		{"ctrl+d twice", []string{"ctrl+d", "ctrl+d"}, 10, "page 3/"},
		{"end", []string{"end"}, 29, "page 6/"},
		{"G then up", []string{"G", "k"}, 28, "page 6/"},
		{"G then g", []string{"G", "g"}, 0, "page 1/"},
		{"G then home", []string{"G", "home"}, 0, "page 1/"},
		{"pgup clamps", []string{"pgdown", "pgup", "pgup"}, 0, "page 1/"},
		{"ctrl+u", []string{"end", "ctrl+u"}, 24, "page 5/"},
		{"up at top", []string{"up"}, 0, "page 1/"},
		{"down at bottom", []string{"end", "down"}, 29, "page 6/"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newTriageModel(testCards(30))
			next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: height})
			m = next.(triageModel)
			m, _ = press(t, m, tt.keys...)
			if m.cursor != tt.wantCursor {
				t.Fatalf("cursor = %d, want %d", m.cursor, tt.wantCursor)
			}
			view := m.View()
			if !strings.Contains(view, fmt.Sprintf("capture %02d", tt.wantCursor)) {
				t.Fatalf("cursor row %d not visible\n%s", tt.wantCursor, view)
			}
			if !strings.Contains(view, tt.wantPage) {
				t.Fatalf("view missing %q\n%s", tt.wantPage, view)
			}
			for _, line := range strings.Split(view, "\n") {
				if w := lipgloss.Width(line); w > 80 {
					t.Fatalf("line %d cells wide, wider than terminal: %q", w, line)
				}
			}
			if got := strings.Count(view, "\n"); got > height {
				t.Fatalf("view is %d lines, taller than terminal %d\n%s", got, height, view)
			}
		})
	}
}

func TestTriageListDecisions(t *testing.T) {
	tests := []struct {
		name        string
		keys        []string
		wantActions []string
		wantDecided []bool
		wantCursor  int
	}{
		{"task", []string{"t"}, []string{"task", "", "", ""}, []bool{true, false, false, false}, 1},
		{"note", []string{"n"}, []string{"note", "", "", ""}, []bool{true, false, false, false}, 1},
		{"archive", []string{"a"}, []string{"archive", "", "", ""}, []bool{true, false, false, false}, 1},
		{"delete", []string{"d"}, []string{"delete", "", "", ""}, []bool{true, false, false, false}, 1},
		{"explicit skip", []string{"s"}, []string{"", "", "", ""}, []bool{true, false, false, false}, 1},
		{"enter accepts proposed", []string{"j", "enter"}, []string{"", "note", "", ""}, []bool{false, true, false, false}, 2},
		{"override later", []string{"t", "k", "d"}, []string{"delete", "", "", ""}, []bool{true, false, false, false}, 1},
		{"last row stays", []string{"G", "t"}, []string{"", "", "", "task"}, []bool{false, false, false, true}, 3},
		{"A fills undecided only", []string{"d", "s", "A"}, []string{"delete", "", "archive", "task"}, []bool{true, true, true, true}, 2},
		{"A with nothing decided", []string{"A"}, []string{"task", "note", "archive", "task"}, []bool{true, true, true, true}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, cmd := press(t, newTriageModel(testCards(4)), tt.keys...)
			if isQuit(cmd) {
				t.Fatal("decision finished the session")
			}
			if !reflect.DeepEqual(m.actions, tt.wantActions) {
				t.Fatalf("actions = %q, want %q", m.actions, tt.wantActions)
			}
			if !reflect.DeepEqual(m.decided, tt.wantDecided) {
				t.Fatalf("decided = %v, want %v", m.decided, tt.wantDecided)
			}
			if m.cursor != tt.wantCursor {
				t.Fatalf("cursor = %d, want %d", m.cursor, tt.wantCursor)
			}
		})
	}
}

func TestTriageCardView(t *testing.T) {
	tests := []struct {
		name       string
		keys       []string
		wantView   triageView
		wantCursor int
	}{
		{"space opens card", []string{"j", "space"}, viewCard, 1},
		{"tab opens card", []string{"tab"}, viewCard, 0},
		{"tab back to list", []string{"tab", "tab"}, viewList, 0},
		{"esc back to list", []string{"tab", "esc"}, viewList, 0},
		{"right/left move in card", []string{"tab", "right", "right", "left"}, viewCard, 1},
		{"l/h move in card", []string{"tab", "l", "l", "h"}, viewCard, 1},
		{"decide advances in card", []string{"tab", "t"}, viewCard, 1},
		{"card cursor carries to list", []string{"tab", "right", "right", "esc"}, viewList, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, cmd := press(t, newTriageModel(testCards(4)), tt.keys...)
			if isQuit(cmd) || m.quit {
				t.Fatal("unexpected quit")
			}
			if m.view != tt.wantView {
				t.Fatalf("view = %v, want %v", m.view, tt.wantView)
			}
			if m.cursor != tt.wantCursor {
				t.Fatalf("cursor = %d, want %d", m.cursor, tt.wantCursor)
			}
			if tt.wantView == viewCard && !strings.Contains(m.View(), "proposed:") {
				t.Fatalf("card view not rendered\n%s", m.View())
			}
		})
	}
}

func TestTriageCardDecisions(t *testing.T) {
	m, _ := press(t, newTriageModel(testCards(3)), "tab", "t", "space", "enter")
	want := []string{"task", "", "archive"}
	if !reflect.DeepEqual(m.actions, want) {
		t.Fatalf("actions = %q, want %q", m.actions, want)
	}
	if !reflect.DeepEqual(m.decided, []bool{true, true, true}) {
		t.Fatalf("decided = %v", m.decided)
	}
	if !strings.Contains(m.View(), "chosen:   archive") {
		t.Fatalf("card view missing chosen line\n%s", m.View())
	}
}

func TestTriageCardLastDecisionDoesNotFinish(t *testing.T) {
	m, cmd := press(t, newTriageModel(testCards(2)), "tab", "right", "d")
	if isQuit(cmd) || m.done || m.quit {
		t.Fatalf("deciding the last card ended the session (done=%v quit=%v)", m.done, m.quit)
	}
	if m.cursor != 1 || m.view != viewCard {
		t.Fatalf("cursor=%d view=%v, want to stay on last card", m.cursor, m.view)
	}
	if m.actions[1] != triageDelete {
		t.Fatalf("actions = %q", m.actions)
	}
}

func TestTriageFinishAndQuit(t *testing.T) {
	tests := []struct {
		name     string
		keys     []string
		wantDone bool
		wantQuit bool
		want     []string
	}{
		{"w returns actions with undecided as skip", []string{"t", "j", "w"}, true, false, []string{"task", "", ""}},
		{"w after A", []string{"s", "A", "w"}, true, false, []string{"", "note", "archive"}},
		{"w from card view", []string{"tab", "n", "w"}, true, false, []string{"note", "", ""}},
		{"q quits list", []string{"t", "q"}, false, true, nil},
		{"esc quits list", []string{"esc"}, false, true, nil},
		{"ctrl+c quits list", []string{"ctrl+c"}, false, true, nil},
		{"q quits card", []string{"tab", "q"}, false, true, nil},
		{"ctrl+c quits card", []string{"tab", "ctrl+c"}, false, true, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, cmd := press(t, newTriageModel(testCards(3)), tt.keys...)
			if !isQuit(cmd) {
				t.Fatal("expected tea.Quit")
			}
			if m.done != tt.wantDone || m.quit != tt.wantQuit {
				t.Fatalf("done=%v quit=%v, want done=%v quit=%v", m.done, m.quit, tt.wantDone, tt.wantQuit)
			}
			if tt.wantDone && !reflect.DeepEqual(m.actions, tt.want) {
				t.Fatalf("actions = %q, want %q", m.actions, tt.want)
			}
		})
	}
}

func TestTruncateRuneSafe(t *testing.T) {
	tests := []struct {
		in    string
		width int
		want  string
	}{
		{"short", 10, "short"},
		{"exactly10!", 10, "exactly10!"},
		{"this is too long", 8, "this is…"},
		{"ééééééééé", 5, "éééé…"},
		{"abc", 1, "…"},
	}
	for _, tt := range tests {
		if got := truncate(tt.in, tt.width); got != tt.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tt.in, tt.width, got, tt.want)
		}
	}
}
