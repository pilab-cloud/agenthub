package main

import (
	"fmt"
	"io"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// wizardHarness is one selectable target in the wizard.
type wizardHarness struct {
	ID       string
	Name     string
	Detected bool
	Global   bool
}

// wizardInput seeds the wizard. A non-empty Location skips the location step;
// Preselect marks targets that start checked.
type wizardInput struct {
	Harnesses []wizardHarness
	Preselect map[string]bool
	Location  location
	Assets    int
}

// wizardResult is what the user chose.
type wizardResult struct {
	Targets   []string
	Location  location
	Cancelled bool
}

type wizardStage int

const (
	stageTargets wizardStage = iota
	stageLocation
	stageConfirm
)

var (
	titleStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("63"))
	activeStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("212"))
	dimStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	errStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
)

// wizardModel is the bubbletea model of `agenthub install`.
type wizardModel struct {
	in       wizardInput
	stage    wizardStage
	cursor   int
	selected []bool
	loc      location
	locIdx   int
	errMsg   string
	done     bool
	result   wizardResult
}

var locationChoices = []location{locGlobal, locLocal}

func newWizardModel(in wizardInput) wizardModel {
	m := wizardModel{in: in, selected: make([]bool, len(in.Harnesses)), loc: in.Location}
	for i, h := range in.Harnesses {
		m.selected[i] = in.Preselect[h.ID]
	}
	return m
}

// Init implements tea.Model.
func (m wizardModel) Init() tea.Cmd { return nil }

func (m wizardModel) chosen() []wizardHarness {
	var out []wizardHarness
	for i, h := range m.in.Harnesses {
		if m.selected[i] {
			out = append(out, h)
		}
	}
	return out
}

func (m wizardModel) finish(cancel bool) (tea.Model, tea.Cmd) {
	m.done = true
	m.result = wizardResult{Cancelled: cancel}
	if !cancel {
		for _, h := range m.chosen() {
			m.result.Targets = append(m.result.Targets, h.ID)
		}
		m.result.Location = m.loc
	}
	return m, tea.Quit
}

func (m wizardModel) listLen() int {
	if m.stage == stageTargets {
		return len(m.in.Harnesses)
	}
	return len(locationChoices)
}

// Update implements tea.Model.
func (m wizardModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	m.errMsg = ""
	switch key.String() {
	case "ctrl+c", "esc", "q":
		return m.finish(true)
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < m.listLen()-1 {
			m.cursor++
		}
	case " ", "x":
		if m.stage == stageTargets {
			m.selected[m.cursor] = !m.selected[m.cursor]
		}
	case "a":
		if m.stage == stageTargets {
			all := true
			for _, s := range m.selected {
				all = all && s
			}
			for i := range m.selected {
				m.selected[i] = !all
			}
		}
	case "left", "h", "backspace":
		return m.back(), nil
	case "enter":
		return m.next()
	}
	return m, nil
}

func (m wizardModel) back() wizardModel {
	switch m.stage {
	case stageLocation:
		m.stage, m.cursor = stageTargets, 0
	case stageConfirm:
		if m.in.Location != "" {
			m.stage, m.cursor = stageTargets, 0
		} else {
			m.stage, m.cursor = stageLocation, m.locIdx
		}
	}
	return m
}

func (m wizardModel) next() (tea.Model, tea.Cmd) {
	switch m.stage {
	case stageTargets:
		if len(m.chosen()) == 0 {
			m.errMsg = "select at least one target (space to toggle)"
			return m, nil
		}
		if m.in.Location != "" {
			m.stage = stageConfirm
			return m, nil
		}
		m.stage, m.cursor = stageLocation, m.locIdx
	case stageLocation:
		m.locIdx = m.cursor
		m.loc = locationChoices[m.cursor]
		m.stage = stageConfirm
	case stageConfirm:
		return m.finish(false)
	}
	return m, nil
}

// View implements tea.Model.
func (m wizardModel) View() string {
	if m.done {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(titleStyle.Render("AgentHub install") + "\n")
	sb.WriteString(dimStyle.Render(fmt.Sprintf("%d locked asset(s) will be installed", m.in.Assets)) + "\n\n")

	switch m.stage {
	case stageTargets:
		sb.WriteString("Which agents should receive the assets?\n\n")
		for i, h := range m.in.Harnesses {
			sb.WriteString(m.row(i == m.cursor, checkbox(m.selected[i]), h.Name, harnessHint(h)))
		}
		sb.WriteString("\n" + dimStyle.Render("space toggle · a all · enter next · esc cancel") + "\n")
	case stageLocation:
		sb.WriteString("Where should they be installed?\n\n")
		labels := map[location]string{locGlobal: "global (your user config)", locLocal: "local (this project)"}
		for i, l := range locationChoices {
			mark := "( )"
			if i == m.cursor {
				mark = "(•)"
			}
			sb.WriteString(m.row(i == m.cursor, mark, labels[l], ""))
		}
		sb.WriteString("\n" + dimStyle.Render("↑/↓ move · enter next · ← back · esc cancel") + "\n")
	case stageConfirm:
		loc := m.loc
		if loc == "" {
			loc = locationChoices[m.locIdx]
		}
		sb.WriteString(fmt.Sprintf("Install into (%s):\n\n", loc))
		for _, h := range m.chosen() {
			line := "  • " + h.Name
			if loc == locGlobal && !h.Global {
				line += dimStyle.Render("  (no global location; will be skipped)")
			}
			sb.WriteString(line + "\n")
		}
		sb.WriteString("\n" + dimStyle.Render("enter install · ← back · esc cancel") + "\n")
	}
	if m.errMsg != "" {
		sb.WriteString(errStyle.Render(m.errMsg) + "\n")
	}
	return sb.String()
}

func (m wizardModel) row(active bool, mark, label, hint string) string {
	prefix := "  "
	if active {
		prefix = activeStyle.Render("> ")
		label = activeStyle.Render(label)
	}
	line := prefix + mark + " " + label
	if hint != "" {
		line += " " + dimStyle.Render(hint)
	}
	return line + "\n"
}

func checkbox(on bool) string {
	if on {
		return "[x]"
	}
	return "[ ]"
}

func harnessHint(h wizardHarness) string {
	var parts []string
	if h.Detected {
		parts = append(parts, "detected")
	}
	if !h.Global {
		parts = append(parts, "local only")
	}
	if len(parts) == 0 {
		return ""
	}
	return "(" + strings.Join(parts, ", ") + ")"
}

// runWizard runs the TUI on in/out and returns the user's choices.
func runWizard(in io.Reader, out io.Writer, input wizardInput) (wizardResult, error) {
	final, err := tea.NewProgram(newWizardModel(input), tea.WithInput(in), tea.WithOutput(out)).Run()
	if err != nil {
		return wizardResult{}, err
	}
	m, ok := final.(wizardModel)
	if !ok {
		return wizardResult{}, fmt.Errorf("unexpected wizard model %T", final)
	}
	return m.result, nil
}
