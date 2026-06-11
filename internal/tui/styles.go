package tui

import (
	"charm.land/lipgloss/v2"

	"github.com/jwstover/td/internal/task"
)

// Styles collects every lipgloss style the TUI uses.
type Styles struct {
	Header       lipgloss.Style
	HeaderAccent lipgloss.Style
	Cursor       lipgloss.Style
	Selected     lipgloss.Style
	Normal       lipgloss.Style
	Dimmed       lipgloss.Style
	State        map[task.State]lipgloss.Style
	DetailBorder lipgloss.Style
	DetailTitle  lipgloss.Style
	Help         lipgloss.Style
	Status       lipgloss.Style
	Error        lipgloss.Style
	PromptLabel  lipgloss.Style
	PanelBorder  lipgloss.Style
	PanelTitle   lipgloss.Style
	PanelKey     lipgloss.Style
	PanelDesc    lipgloss.Style
	ModalBorder  lipgloss.Style
	ModalTitle   lipgloss.Style
}

// DefaultStyles returns the standard dark-terminal styling.
// TODO(owner): light-background detection via tea.RequestBackgroundColor.
func DefaultStyles() Styles {
	stateStyle := func(c string) lipgloss.Style {
		return lipgloss.NewStyle().Foreground(lipgloss.Color(c)).Bold(true)
	}
	return Styles{
		Header:       lipgloss.NewStyle().Bold(true).Padding(0, 1),
		HeaderAccent: lipgloss.NewStyle().Foreground(lipgloss.Color("13")).Bold(true),
		Cursor:       lipgloss.NewStyle().Foreground(lipgloss.Color("13")).Bold(true),
		Selected:     lipgloss.NewStyle().Bold(true),
		Normal:       lipgloss.NewStyle(),
		Dimmed:       lipgloss.NewStyle().Foreground(lipgloss.Color("8")),
		State: map[task.State]lipgloss.Style{
			task.StateInbox:   stateStyle("13"), // magenta
			task.StateTodo:    stateStyle("12"), // blue
			task.StateDoing:   stateStyle("11"), // yellow
			task.StateBlocked: stateStyle("9"),  // red
			task.StateDone:    stateStyle("10"), // green
			task.StateSomeday: stateStyle("8"),  // gray
		},
		DetailBorder: lipgloss.NewStyle().
			Border(lipgloss.NormalBorder(), false, false, false, true).
			BorderForeground(lipgloss.Color("8")).
			Padding(0, 1),
		DetailTitle: lipgloss.NewStyle().Bold(true).Underline(true),
		Help:        lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Padding(0, 1),
		Status:      lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Padding(0, 1),
		Error:       lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Padding(0, 1),
		PromptLabel: lipgloss.NewStyle().Foreground(lipgloss.Color("13")).Bold(true).Padding(0, 1),
		PanelBorder: lipgloss.NewStyle().
			Border(lipgloss.NormalBorder(), true, false, false, false).
			BorderForeground(lipgloss.Color("8")).
			Padding(0, 1),
		PanelTitle: lipgloss.NewStyle().Foreground(lipgloss.Color("13")).Bold(true),
		PanelKey:   lipgloss.NewStyle().Foreground(lipgloss.Color("13")).Bold(true),
		PanelDesc:  lipgloss.NewStyle(),
		ModalBorder: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("13")).
			Padding(0, 1),
		ModalTitle: lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("13")),
	}
}
