package tui

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

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
		{"email tag [read_now]", "email"},
		{"email tag [read_now]; typesafe unsure (task 0.31)", "ts?"},
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

func TestRepeatCountShownInListAndCard(t *testing.T) {
	m := newTriageModel([]InboxCard{
		{Summary: "Email: PR merged — Bot [read_now]", Proposed: triageArchive, Reason: "email tag [read_now]", Count: 3},
		{Summary: "buy milk", Proposed: triageTask, Count: 1},
	})
	m.width = 120
	list := m.View()
	if !strings.Contains(list, "×3 Email: PR merged") {
		t.Errorf("list should prefix the repeat count:\n%s", list)
	}
	if strings.Contains(list, "×1") {
		t.Errorf("single captures should carry no count:\n%s", list)
	}
	m.view = viewCard
	if card := m.View(); !strings.Contains(card, "3 identical captures") {
		t.Errorf("card should say the decision covers all repeats:\n%s", card)
	}
}

func destCards() []InboxCard {
	return []InboxCard{
		{Summary: "fix portal login", Proposed: triageTask, Reason: "typesafe: task (confidence 0.90); → #portal (0.82)", Destination: "portal"},
		{Summary: "send invoice", Proposed: triageTask, Reason: "typesafe: task (confidence 0.80); → #acme (0.75)", Destination: "acme"},
		{Summary: "buy milk", Proposed: triageTask, Reason: "rule"},
	}
}

func TestSourceHintWithDestination(t *testing.T) {
	if got := sourceHint("typesafe: task (confidence 0.90); → #portal (0.82)"); got != "ts 0.90" {
		t.Errorf("sourceHint = %q, want ts 0.90", got)
	}
}

func TestTriageDestinationDecisions(t *testing.T) {
	tests := []struct {
		name string
		keys []string
		want []InboxDecision
	}{
		{"accept keeps", []string{"enter", "enter", "enter"}, []InboxDecision{{Action: "task", Destination: "portal"}, {Action: "task", Destination: "acme"}, {Action: "task", Destination: ""}}},
		{"manual task keeps", []string{"t"}, []InboxDecision{{Action: "task", Destination: "portal"}, {}, {}}},
		{"x clears before accept", []string{"x", "enter"}, []InboxDecision{{Action: "task", Destination: ""}, {}, {}}},
		{"x twice restores", []string{"x", "x", "enter"}, []InboxDecision{{Action: "task", Destination: "portal"}, {}, {}}},
		{"x after accept clears", []string{"enter", "k", "x"}, []InboxDecision{{Action: "task", Destination: ""}, {}, {}}},
		{"note drops", []string{"n", "a", "d"}, []InboxDecision{{Action: "note", Destination: ""}, {Action: "archive", Destination: ""}, {Action: "delete", Destination: ""}}},
		{"skip drops", []string{"s"}, []InboxDecision{{}, {}, {}}},
		{"x on no-destination row is a no-op", []string{"j", "j", "x", "enter"}, []InboxDecision{{}, {}, {Action: "task", Destination: ""}}},
		{"A keeps", []string{"j", "x", "A"}, []InboxDecision{{Action: "task", Destination: "portal"}, {Action: "task", Destination: ""}, {Action: "task", Destination: ""}}},
		{"card x clears", []string{"tab", "x", "enter"}, []InboxDecision{{Action: "task", Destination: ""}, {}, {}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, _ := press(t, newTriageModel(destCards()), tt.keys...)
			if got := m.decisions(); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("decisions = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestTriageDestinationShownInListAndCard(t *testing.T) {
	m := newTriageModel(destCards())
	list := m.View()
	for _, want := range []string{"fix portal login → #portal", "send invoice → #acme", "x clear dest"} {
		if !strings.Contains(list, want) {
			t.Errorf("list missing %q:\n%s", want, list)
		}
	}
	if strings.Contains(list, "buy milk →") {
		t.Errorf("row without destination shows one:\n%s", list)
	}

	// Cleared or decided as a non-task: the list drops it.
	m, _ = press(t, m, "x", "j", "n")
	list = m.View()
	if strings.Contains(list, "#portal") || strings.Contains(list, "#acme") {
		t.Errorf("cleared / note rows still show a destination:\n%s", list)
	}

	m = newTriageModel(destCards())
	m.view = viewCard
	if card := m.View(); !strings.Contains(card, "dest:     #portal") || !strings.Contains(card, "x clear dest") {
		t.Errorf("card missing destination:\n%s", card)
	}
	m, _ = press(t, m, "x")
	if card := m.View(); !strings.Contains(card, "dest:     #portal (cleared — x restores)") {
		t.Errorf("card missing cleared state:\n%s", card)
	}
	m, _ = press(t, m, "x", "n", "left")
	if card := m.View(); !strings.Contains(card, "dest:     #portal (unused: not a task)") {
		t.Errorf("card missing unused state:\n%s", card)
	}
}

// Without destinations the help lines are unchanged.
func TestTriageNoDestinationHelpUnchanged(t *testing.T) {
	m := newTriageModel(testCards(2))
	if strings.Contains(m.View(), "x clear dest") {
		t.Errorf("list advertises x without destinations")
	}
	m.view = viewCard
	if strings.Contains(m.View(), "dest:") || strings.Contains(m.View(), "x clear dest") {
		t.Errorf("card shows destination UI without one")
	}
}

func TestTriageDestinationSurvivesNarrowList(t *testing.T) {
	cards := []InboxCard{{Summary: strings.Repeat("long capture text ", 10), Proposed: triageTask, Destination: "portal"}}
	m := newTriageModel(cards)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 60, Height: 20})
	view := next.(triageModel).View()
	if !strings.Contains(view, "… → #portal") {
		t.Errorf("destination cut off by a long summary:\n%s", view)
	}
}

func datedCards() []InboxCard {
	return []InboxCard{
		{Summary: "pay bill tomorrow", Proposed: triageTask, Date: time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC), DateRole: "due"},
		{Summary: "dentist friday", Proposed: triageTask, Date: time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC), DateRole: "scheduled"},
		{Summary: "nice quote", Proposed: triageNote},
	}
}

// newDatedModel is a model over datedCards triaged on Monday 2026-11-09, so
// none of the proposed dates is past.
func newDatedModel() triageModel {
	m := newTriageModel(datedCards())
	m.today = time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	return m
}

func TestDateShownInListAndCard(t *testing.T) {
	m := newDatedModel()
	m.width = 120
	list := m.View()
	for _, want := range []string{"due 11-10", "sched 11-13", "c clear date"} {
		if !strings.Contains(list, want) {
			t.Errorf("list missing %q:\n%s", want, list)
		}
	}
	m.view = viewCard
	if card := m.View(); !strings.Contains(card, "date:     due Tue 2026-11-10") || !strings.Contains(card, "c clear/restore date") {
		t.Errorf("card should show role + date and the clear key:\n%s", card)
	}
	m.cursor = 2
	if card := m.View(); strings.Contains(card, "date:") || strings.Contains(card, "clear/restore") {
		t.Errorf("undated card should show no date line:\n%s", card)
	}
}

func TestNoDateColumnWithoutDates(t *testing.T) {
	m := newTriageModel([]InboxCard{{Summary: "buy milk", Proposed: triageTask}})
	if list := m.View(); strings.Contains(list, "date") || strings.Contains(list, "clear date") {
		t.Errorf("no card has a date: list should not grow a date column:\n%s", list)
	}
}

func TestClearDateToggles(t *testing.T) {
	// c clears without deciding or moving; c again restores.
	m, _ := press(t, newDatedModel(), "c")
	if !m.dateCleared[0] || m.decided[0] || m.cursor != 0 {
		t.Fatalf("after c: cleared=%v decided=%v cursor=%d", m.dateCleared, m.decided, m.cursor)
	}
	if list := m.View(); strings.Contains(list, "due 11-10") {
		t.Errorf("cleared date still shown:\n%s", list)
	}
	m.view = viewCard
	if card := m.View(); !strings.Contains(card, "cleared (was due Tue 2026-11-10)") {
		t.Errorf("card should say the date was cleared:\n%s", card)
	}
	m, _ = press(t, m, "c")
	if m.dateCleared[0] {
		t.Fatalf("second c should restore the date")
	}
	// c on an undated card is a no-op.
	m.view = viewList
	m, _ = press(t, m, "j", "j", "c")
	if m.dateCleared[2] {
		t.Errorf("undated card cannot be cleared")
	}
}

func TestDateHiddenWhenNotTask(t *testing.T) {
	m, _ := press(t, newDatedModel(), "n") // row 0 → note
	if list := m.View(); strings.Contains(list, "due 11-10") {
		t.Errorf("a note row must not show a task date:\n%s", list)
	}
	m.cursor, m.view = 0, viewCard
	if card := m.View(); !strings.Contains(card, "(only if made a task)") {
		t.Errorf("card should say the date applies only to a task:\n%s", card)
	}
}

func TestDecisionsCarryClearedDates(t *testing.T) {
	// Row 0: clear, then task. Row 1: task, date kept. Row 2: accepted note.
	m, _ := press(t, newDatedModel(), "c", "t", "t", "A")
	want := []InboxDecision{
		{Action: triageTask, ClearDate: true},
		{Action: triageTask},
		{Action: triageNote},
	}
	if got := m.decisions(); !reflect.DeepEqual(got, want) {
		t.Fatalf("decisions = %+v, want %+v", got, want)
	}
}

func TestClearDateOnlyOnTaskRows(t *testing.T) {
	// Row 0 decided note: c must not flip a hidden flag that would surface
	// as an undated task after t.
	m, _ := press(t, newDatedModel(), "n", "k", "c", "t")
	if m.dateCleared[0] {
		t.Errorf("c on a note row cleared the date")
	}
	if m.decisions()[0] != (InboxDecision{Action: triageTask}) {
		t.Errorf("decision = %+v, want dated task", m.decisions()[0])
	}
}

func TestPastDateFlagged(t *testing.T) {
	m := newDatedModel()
	m.today = time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC) // row 0 (11-10) past, row 1 (11-13) not
	if !m.past(0) || m.past(1) {
		t.Fatalf("past = %v %v, want true false", m.past(0), m.past(1))
	}
	m.view = viewCard
	if card := m.View(); !strings.Contains(card, "due Tue 2026-11-10, already past") {
		t.Errorf("card should flag a past date:\n%s", card)
	}
	m.cursor = 1
	if card := m.View(); strings.Contains(card, "already past") {
		t.Errorf("future date flagged as past:\n%s", card)
	}
}

func TestClearDateAndDestinationIndependent(t *testing.T) {
	m := newTriageModel([]InboxCard{{
		Summary: "portal invoice tomorrow", Proposed: triageTask, Destination: "portal",
		Date: time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC), DateRole: "due",
	}})
	m, _ = press(t, m, "c", "enter")
	if got := m.decisions()[0]; got != (InboxDecision{Action: triageTask, Destination: "portal", ClearDate: true}) {
		t.Errorf("c then accept = %+v, want task kept at #portal, date cleared", got)
	}
	m, _ = press(t, m, "c", "x")
	if got := m.decisions()[0]; got != (InboxDecision{Action: triageTask}) {
		t.Errorf("c (restore) then x = %+v, want task, dest cleared, date kept", got)
	}
}

func dupCards() []InboxCard {
	return []InboxCard{
		{Summary: "renew my passport", Proposed: triageTask, Reason: "typesafe: task (confidence 0.90)", DuplicateOf: "renew passport", DuplicateProbability: 0.93},
		{Summary: "buy milk", Proposed: triageTask, Reason: "rule"},
	}
}

func TestDuplicateWarningShownInListAndCard(t *testing.T) {
	m := newTriageModel(dupCards())
	m.width = 120
	list := m.View()
	if !strings.Contains(list, "dup! renew my passport") {
		t.Errorf("list should mark the flagged capture:\n%s", list)
	}
	if strings.Contains(list, "dup! buy milk") {
		t.Errorf("unflagged capture marked:\n%s", list)
	}
	if !strings.Contains(list, `possible duplicate of "renew passport" (0.93) · u archive as duplicate`) {
		t.Errorf("list should name the cursor row's duplicate:\n%s", list)
	}
	// The proposal itself is unchanged: still an undecided task.
	if !strings.Contains(list, "task?") {
		t.Errorf("warning must not change the proposal:\n%s", list)
	}
	m.view = viewCard
	card := m.View()
	if !strings.Contains(card, "possible duplicate of open task: renew passport (0.93)") {
		t.Errorf("card should name the task it may repeat:\n%s", card)
	}
	m.cursor = 1
	if card := m.View(); strings.Contains(card, "possible duplicate") {
		t.Errorf("unflagged card shows a warning:\n%s", card)
	}
}

func TestDuplicateLineOnlyWhenFlagged(t *testing.T) {
	plain := newTriageModel(testCards(3))
	if strings.Contains(plain.View(), "dup!") || plain.pageSize() != defaultTriageHeight-listChrome {
		t.Errorf("no flagged card: want no dup line and the usual page size")
	}
	m := newTriageModel(dupCards())
	if m.pageSize() != defaultTriageHeight-listChrome-1 {
		t.Errorf("pageSize = %d, want one row fewer for the dup line", m.pageSize())
	}
	m.cursor = 1
	if !strings.Contains(m.View(), "dup! = may repeat an open task") {
		t.Errorf("unflagged cursor row should explain the marker:\n%s", m.View())
	}
	// Narrow terminals: no line wider than the window.
	next, _ := m.Update(tea.WindowSizeMsg{Width: 40, Height: 20})
	m = next.(triageModel)
	m.cursor = 0
	for _, line := range strings.Split(m.dupLine(), "\n") {
		if w := lipgloss.Width(line); w > 40 {
			t.Errorf("dup line %d cells wide on a 40-cell terminal: %q", w, line)
		}
	}
}

func TestArchiveAsDuplicateKeyU(t *testing.T) {
	// List view: u archives a flagged row and moves on; on an unflagged row
	// it does nothing (no decision, cursor stays).
	m, _ := press(t, newTriageModel(dupCards()), "u")
	if m.actions[0] != triageArchive || !m.decided[0] || m.cursor != 1 {
		t.Fatalf("u on flagged row: actions %q decided %v cursor %d", m.actions, m.decided, m.cursor)
	}
	m, _ = press(t, m, "u")
	if m.decided[1] || m.actions[1] != triageSkip || m.cursor != 1 {
		t.Errorf("u on unflagged row: actions %q decided %v cursor %d", m.actions, m.decided, m.cursor)
	}

	// Card view: same rule.
	m, _ = press(t, newTriageModel(dupCards()), "tab", "right", "u")
	if m.decided[1] {
		t.Errorf("u in card view decided an unflagged capture")
	}
	m, _ = press(t, m, "left", "u")
	if m.actions[0] != triageArchive || !m.decided[0] {
		t.Errorf("u in card view: actions %q decided %v", m.actions, m.decided)
	}
}

func TestAcceptAllNeverArchivesDuplicates(t *testing.T) {
	m, _ := press(t, newTriageModel(dupCards()), "A")
	if want := []string{triageTask, triageTask}; !reflect.DeepEqual(m.actions, want) {
		t.Errorf("A = %q, want proposals %q: a duplicate warning is never auto-applied", m.actions, want)
	}
}

func TestDuplicateKeyLeavesDestinationKeyAlone(t *testing.T) {
	cards := []InboxCard{{Summary: "renew my passport", Proposed: triageTask, Destination: "home", DuplicateOf: "renew passport", DuplicateProbability: 0.9}}
	m, _ := press(t, newTriageModel(cards), "x")
	if m.decided[0] || !m.cleared[0] {
		t.Fatalf("x should clear the destination, not decide: decided %v cleared %v", m.decided, m.cleared)
	}
	m, _ = press(t, m, "u")
	if m.actions[0] != triageArchive || !m.decided[0] {
		t.Errorf("u should archive as duplicate: actions %q", m.actions)
	}
}
