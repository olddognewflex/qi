package tui

import (
	"fmt"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// InboxCard is one capture presented for triage. Proposed is the suggested
// action (task/note/archive) and Reason says where it came from — the
// heuristic rule or the opt-in classifier with its confidence; the user may
// accept it or choose another.
type InboxCard struct {
	Summary  string
	Body     []string
	Proposed string
	Reason   string
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

// triageView selects which screen the triage model renders. The zero value
// is the list, the default for a new session.
type triageView int

const (
	viewList triageView = iota
	viewCard
)

// Layout fallbacks used until the first tea.WindowSizeMsg arrives.
const (
	defaultTriageHeight = 20
	defaultTriageWidth  = 100

	// listChrome is the number of non-row lines the list view draws: title,
	// blank, header, blank, status, two help lines.
	listChrome = 7

	actionColWidth = 8
	sourceColWidth = 7
)

var (
	decidedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("11")).Bold(true)
	deleteStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Bold(true)
)

type triageModel struct {
	cards []InboxCard
	// actions holds one action per card; "" is skip. It is what TriageInbox
	// returns, so an undecided row is a skip.
	actions []string
	// decided marks rows the user has acted on (including an explicit skip),
	// distinguishing them from untouched proposals, which share the "" value.
	decided []bool
	cursor  int
	offset  int // first row shown in the list window
	width   int
	height  int
	view    triageView
	done    bool
	quit    bool
}

func newTriageModel(cards []InboxCard) triageModel {
	return triageModel{
		cards:   cards,
		actions: make([]string, len(cards)),
		decided: make([]bool, len(cards)),
		width:   defaultTriageWidth,
		height:  defaultTriageHeight,
	}
}

func (m triageModel) Init() tea.Cmd { return nil }

func (m triageModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		if msg.Width > 0 {
			m.width = msg.Width
		}
		if msg.Height > 0 {
			m.height = msg.Height
		}
		m.scroll()
	case tea.KeyMsg:
		if m.view == viewCard {
			return m.updateCard(msg)
		}
		return m.updateList(msg)
	}
	return m, nil
}

func (m triageModel) updateList(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	last := len(m.cards) - 1
	switch msg.String() {
	case "ctrl+c", "esc", "q":
		m.quit = true
		return m, tea.Quit
	case "up", "k":
		m.cursor--
	case "down", "j":
		m.cursor++
	case "pgup", "ctrl+u":
		m.cursor -= m.pageSize()
	case "pgdown", "ctrl+d":
		m.cursor += m.pageSize()
	case "g", "home":
		m.cursor = 0
	case "G", "end":
		m.cursor = last
	case " ", "tab":
		m.view = viewCard
		return m, nil
	case "t":
		m.set(triageTask)
		m.cursor++
	case "n":
		m.set(triageNote)
		m.cursor++
	case "a":
		m.set(triageArchive)
		m.cursor++
	case "d":
		m.set(triageDelete)
		m.cursor++
	case "s":
		m.set(triageSkip)
		m.cursor++
	case "enter":
		m.set(m.cards[m.cursor].Proposed)
		m.cursor++
	case "A":
		m.acceptAll()
	case "w":
		m.done = true
		return m, tea.Quit
	}
	m.cursor = clamp(m.cursor, 0, last)
	m.scroll()
	return m, nil
}

func (m triageModel) updateCard(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c", "q":
		m.quit = true
		return m, tea.Quit
	case "esc", "tab":
		m.view = viewList
		m.scroll()
	case "left", "h":
		if m.cursor > 0 {
			m.cursor--
		}
	case "right", "l":
		if m.cursor < len(m.cards)-1 {
			m.cursor++
		}
	case "t":
		m.decideCard(triageTask)
	case "n":
		m.decideCard(triageNote)
	case "a":
		m.decideCard(triageArchive)
	case "d":
		m.decideCard(triageDelete)
	case "s", " ":
		m.decideCard(triageSkip)
	case "enter":
		m.decideCard(m.cards[m.cursor].Proposed)
	case "w":
		m.done = true
		return m, tea.Quit
	}
	return m, nil
}

// set records an explicit decision for the cursor row.
func (m *triageModel) set(action string) {
	m.actions[m.cursor] = action
	m.decided[m.cursor] = true
}

// decideCard records a decision in card view and advances. On the last card
// it stays put: finishing is only ever explicit (w).
func (m *triageModel) decideCard(action string) {
	m.set(action)
	if m.cursor < len(m.cards)-1 {
		m.cursor++
	}
}

// acceptAll takes the proposal for every row the user has not decided,
// leaving explicit choices (including explicit skips) untouched.
func (m *triageModel) acceptAll() {
	for i := range m.cards {
		if !m.decided[i] {
			m.actions[i] = m.cards[i].Proposed
			m.decided[i] = true
		}
	}
}

// pageSize is how many rows fit in the list window.
func (m triageModel) pageSize() int {
	return max(1, m.height-listChrome)
}

// scroll moves the window so the cursor row is visible.
func (m *triageModel) scroll() {
	page := m.pageSize()
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+page {
		m.offset = m.cursor - page + 1
	}
	m.offset = clamp(m.offset, 0, max(0, len(m.cards)-page))
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func (m triageModel) View() string {
	if m.view == viewCard {
		return m.cardView()
	}
	return m.listView()
}

func (m triageModel) listView() string {
	var b strings.Builder
	page := m.pageSize()
	pages := (len(m.cards) + page - 1) / page

	b.WriteString(titleStyle.Render(fmt.Sprintf("Inbox triage — %d captures", len(m.cards))))
	b.WriteString("\n\n")

	summaryW := max(10, m.width-2-actionColWidth-1-1-sourceColWidth)
	header := "  " + padRight("action", actionColWidth) + " " + padRight("capture", summaryW) + " " + "source"
	b.WriteString(helpStyle.Render(header))
	b.WriteString("\n")

	end := min(len(m.cards), m.offset+page)
	for i := m.offset; i < end; i++ {
		c := m.cards[i]
		marker := "  "
		if i == m.cursor {
			marker = cursorStyle.Render("> ")
		}
		summary := padRight(truncate(c.Summary, summaryW), summaryW)
		if i == m.cursor {
			summary = selectedStyle.Render(summary)
		}
		fmt.Fprintf(&b, "%s%s %s %s\n", marker, m.actionCell(i), summary, dueStyle.Render(sourceHint(c.Reason)))
	}

	b.WriteString("\n")
	b.WriteString(titleStyle.Render(fmt.Sprintf("%d/%d", m.cursor+1, len(m.cards))))
	b.WriteString(helpStyle.Render(fmt.Sprintf("  page %d/%d  ", m.cursor/page+1, pages)))
	b.WriteString(helpStyle.Render(m.counts()))
	b.WriteString("\n")
	b.WriteString(helpStyle.Render("t task · n note · a archive · d delete · s skip · enter accept · A accept rest"))
	b.WriteString("\n")
	b.WriteString(helpStyle.Render("↑↓/jk move · pgup/pgdn page · g/G ends · space/tab card · w finish · q quit"))
	b.WriteString("\n")
	return b.String()
}

// actionCell renders the action column: an explicit decision in bold, an
// untouched proposal dimmed with a trailing "?" (the row is still a skip until
// accepted).
func (m triageModel) actionCell(i int) string {
	if !m.decided[i] {
		return dueStyle.Render(padRight(labelFor(m.cards[i].Proposed)+"?", actionColWidth))
	}
	cell := padRight(labelFor(m.actions[i]), actionColWidth)
	switch m.actions[i] {
	case triageDelete:
		return deleteStyle.Render(cell)
	case triageSkip:
		return helpStyle.Render(cell)
	default:
		return decidedStyle.Render(cell)
	}
}

// counts summarizes what finishing now would apply. Undecided rows count as
// skips (that is what they return) and are also broken out.
func (m triageModel) counts() string {
	var task, note, archive, del, skip, undecided int
	for i, a := range m.actions {
		if !m.decided[i] {
			undecided++
		}
		switch a {
		case triageTask:
			task++
		case triageNote:
			note++
		case triageArchive:
			archive++
		case triageDelete:
			del++
		default:
			skip++
		}
	}
	return fmt.Sprintf("task %d  note %d  archive %d  delete %d  skip %d (%d undecided)",
		task, note, archive, del, skip, undecided)
}

func (m triageModel) cardView() string {
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
	if c.Reason != "" {
		fmt.Fprintf(&b, "%s\n", helpStyle.Render("why:      "+c.Reason))
	}

	if m.decided[m.cursor] {
		fmt.Fprintf(&b, "%s\n", cursorStyle.Render("chosen:   "+labelFor(m.actions[m.cursor])))
	}

	b.WriteString("\n")
	b.WriteString(helpStyle.Render("t task · n note · a archive · d delete · s skip · enter accept proposed"))
	b.WriteString("\n")
	b.WriteString(helpStyle.Render("←/→ move · tab/esc list · w finish · q quit"))
	b.WriteString("\n")
	return b.String()
}

// sourceHint condenses a proposal Reason into a short column value: the
// classifier with its confidence, an unsure classifier, a capture left
// unclassified by classify_limit, or a heuristic rule.
func sourceHint(reason string) string {
	switch {
	case strings.HasPrefix(reason, "typesafe:"):
		if i := strings.Index(reason, "confidence "); i >= 0 {
			num := strings.TrimRight(reason[i+len("confidence "):], ")")
			if f, err := strconv.ParseFloat(strings.TrimSpace(num), 64); err == nil {
				return fmt.Sprintf("ts %.2f", f)
			}
		}
		return "ts"
	case strings.Contains(reason, "typesafe unsure"):
		return "ts?"
	case strings.Contains(reason, "not classified (classify_limit)"):
		return "lim"
	default:
		return "rule"
	}
}

// truncate cuts s to at most width display cells, ending in "…" when cut.
// It works rune by rune so multi-byte characters are never split.
func truncate(s string, width int) string {
	if lipgloss.Width(s) <= width {
		return s
	}
	if width <= 1 {
		return "…"
	}
	var b strings.Builder
	used := 0
	for _, r := range s {
		w := lipgloss.Width(string(r))
		if used+w > width-1 {
			break
		}
		b.WriteRune(r)
		used += w
	}
	b.WriteString("…")
	return b.String()
}

// padRight pads s with spaces to width display cells.
func padRight(s string, width int) string {
	if w := lipgloss.Width(s); w < width {
		return s + strings.Repeat(" ", width-w)
	}
	return s
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

// TriageInbox shows the captures as a paged list (with a per-capture card
// view) and returns one action per card, aligned by index (empty string =
// skip, which is also what an undecided capture returns). A nil result means
// the user quit (q/ctrl+c) and nothing should be applied.
func TriageInbox(cards []InboxCard) ([]string, error) {
	if len(cards) == 0 {
		return nil, nil
	}
	prog := tea.NewProgram(newTriageModel(cards))
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
