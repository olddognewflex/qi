package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"qi/internal/domain"
)

var (
	titleStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	cursorStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("13")).Bold(true)
	selectedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	dueStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	helpStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Italic(true)
)

type pickerModel struct {
	title    string
	tasks    []domain.Task
	cursor   int
	selected map[int]bool
	done     bool
	quit     bool
}

func (m pickerModel) Init() tea.Cmd { return nil }

func (m pickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "esc", "q":
			m.quit = true
			return m, tea.Quit
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.tasks)-1 {
				m.cursor++
			}
		case "g":
			m.cursor = 0
		case "G":
			m.cursor = len(m.tasks) - 1
		case " ", "tab", "x":
			m.selected[m.cursor] = !m.selected[m.cursor]
		case "a":
			allOn := len(m.selected) == len(m.tasks)
			m.selected = map[int]bool{}
			if !allOn {
				for i := range m.tasks {
					m.selected[i] = true
				}
			}
		case "enter":
			if len(m.collect()) == 0 {
				m.selected[m.cursor] = true
			}
			m.done = true
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m pickerModel) View() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render(m.title))
	b.WriteString("\n\n")

	for i, t := range m.tasks {
		cursor := "  "
		if i == m.cursor {
			cursor = cursorStyle.Render("> ")
		}
		check := "[ ]"
		if m.selected[i] {
			check = selectedStyle.Render("[x]")
		}
		line := t.Text
		if t.Due != nil {
			line += " " + dueStyle.Render("(due "+t.Due.Format("2006-01-02")+")")
		}
		if i == m.cursor {
			line = cursorStyle.Render(line)
		}
		fmt.Fprintf(&b, "%s%s %s\n", cursor, check, line)
	}

	b.WriteString("\n")
	b.WriteString(helpStyle.Render("↑/k up · ↓/j down · space toggle · a all · enter confirm · q quit"))
	b.WriteString("\n")
	return b.String()
}

func (m pickerModel) collect() []domain.Task {
	out := make([]domain.Task, 0, len(m.selected))
	for i, t := range m.tasks {
		if m.selected[i] {
			out = append(out, t)
		}
	}
	return out
}

// PickTasks shows interactive multi-select picker. Returns selected tasks.
// Empty result + nil error means user aborted.
func PickTasks(title string, tasks []domain.Task) ([]domain.Task, error) {
	if len(tasks) == 0 {
		return nil, nil
	}
	m := pickerModel{
		title:    title,
		tasks:    tasks,
		selected: map[int]bool{},
	}
	prog := tea.NewProgram(m)
	final, err := prog.Run()
	if err != nil {
		return nil, fmt.Errorf("picker: %w", err)
	}
	fm := final.(pickerModel)
	if fm.quit || !fm.done {
		return nil, nil
	}
	return fm.collect(), nil
}
