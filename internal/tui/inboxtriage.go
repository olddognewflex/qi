package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// InboxCard is one capture presented for triage. Proposed is the action the
// triage heuristics suggest (task/note/archive); the user may accept it or
// choose another.
type InboxCard struct {
	Summary  string
	Body     []string
	Proposed string
}

// Triage action labels returned by TriageInbox, one per input card. An empty
// string means the user skipped that card.
const (
	triageSkip    = ""
	triageTask    = "task"
	triageNote    = "note"
	triageArchive = "archive"
	triageDelete  = "delete"
)

type triageModel struct {
	cards   []InboxCard
	actions []string
	cursor  int
	done    bool
	quit    bool
}

func (m triageModel) Init() tea.Cmd { return nil }

func (m triageModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "esc", "q":
			m.quit = true
			return m, tea.Quit
		case "left", "h":
			if m.cursor > 0 {
				m.cursor--
			}
		case "right", "l":
			if m.cursor < len(m.cards)-1 {
				m.cursor++
			}
		case "t":
			return m.decide(triageTask)
		case "n":
			return m.decide(triageNote)
		case "a":
			return m.decide(triageArchive)
		case "d":
			return m.decide(triageDelete)
		case "s", " ":
			return m.decide(triageSkip)
		case "enter":
			return m.decide(m.cards[m.cursor].Proposed)
		case "w":
			m.done = true
			return m, tea.Quit
		}
	}
	return m, nil
}

// decide records an action for the current card and advances. Acting on the
// last card finishes the session.
func (m triageModel) decide(action string) (tea.Model, tea.Cmd) {
	m.actions[m.cursor] = action
	if m.cursor < len(m.cards)-1 {
		m.cursor++
		return m, nil
	}
	m.done = true
	return m, tea.Quit
}

func (m triageModel) View() string {
	var b strings.Builder
	c := m.cards[m.cursor]

	b.WriteString(titleStyle.Render(fmt.Sprintf("Inbox triage — %d/%d", m.cursor+1, len(m.cards))))
	b.WriteString("\n\n")

	b.WriteString(selectedStyle.Render(c.Summary))
	b.WriteString("\n")
	if len(c.Body) > 1 {
		for _, line := range c.Body[1:] {
			fmt.Fprintf(&b, "%s\n", dueStyle.Render("  "+line))
		}
	}
	b.WriteString("\n")
	fmt.Fprintf(&b, "%s\n", helpStyle.Render("proposed: "+labelFor(c.Proposed)))

	if cur := m.actions[m.cursor]; cur != triageSkip {
		fmt.Fprintf(&b, "%s\n", cursorStyle.Render("chosen:   "+labelFor(cur)))
	}

	b.WriteString("\n")
	b.WriteString(helpStyle.Render("t task · n note · a archive · d delete · s skip · enter accept proposed"))
	b.WriteString("\n")
	b.WriteString(helpStyle.Render("←/→ move · w finish · q quit"))
	b.WriteString("\n")
	return b.String()
}

func labelFor(action string) string {
	switch action {
	case triageTask:
		return "task"
	case triageNote:
		return "note"
	case triageArchive:
		return "archive"
	case triageDelete:
		return "delete"
	default:
		return "skip"
	}
}

// TriageInbox walks each card interactively and returns one action per card,
// aligned by index (empty string = skip). A nil result means the user aborted
// (q/esc) and nothing should be applied.
func TriageInbox(cards []InboxCard) ([]string, error) {
	if len(cards) == 0 {
		return nil, nil
	}
	m := triageModel{
		cards:   cards,
		actions: make([]string, len(cards)),
	}
	prog := tea.NewProgram(m)
	final, err := prog.Run()
	if err != nil {
		return nil, fmt.Errorf("triage: %w", err)
	}
	fm := final.(triageModel)
	if fm.quit || !fm.done {
		return nil, nil
	}
	return fm.actions, nil
}
