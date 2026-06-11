package tui

import (
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

type modalKind int

const (
	modalNone modalKind = iota
	modalLog
)

// modal is a centered floating input box. It owns presentation and text
// entry only; the app decides what submit means by switching on kind.
// It has two input modes: multiline (enter inserts a newline, cmd+enter
// submits) and single-line (enter submits).
type modal struct {
	kind      modalKind
	multiline bool
	title     string
	target    int64  // task the modal acts on
	extra     string // captured context, e.g. the old body for modalLog
	area      textarea.Model

	submitMulti  key.Binding
	submitSingle key.Binding
}

func newModal() modal {
	ta := textarea.New()
	ta.ShowLineNumbers = false
	ta.Prompt = ""
	return modal{
		area: ta,
		// ctrl+enter and alt+enter cover terminals that don't pass cmd
		// (super) through to the app.
		submitMulti:  key.NewBinding(key.WithKeys("super+enter", "ctrl+enter", "alt+enter")),
		submitSingle: key.NewBinding(key.WithKeys("enter")),
	}
}

// Open resets the modal for a new use and focuses the textarea.
func (m *modal) Open(kind modalKind, multiline bool, title string, target int64, extra string) tea.Cmd {
	m.kind = kind
	m.multiline = multiline
	m.title = title
	m.target = target
	m.extra = extra
	m.area.Reset()
	if multiline {
		m.area.SetHeight(6)
	} else {
		m.area.SetHeight(1)
	}
	return m.area.Focus()
}

func (m *modal) Close() {
	m.kind = modalNone
	m.multiline = false
	m.title = ""
	m.target = 0
	m.extra = ""
	m.area.Reset()
	m.area.Blur()
}

func (m modal) Active() bool {
	return m.kind != modalNone
}

// IsSubmit reports whether the key submits the modal. In single-line mode
// plain enter is intercepted here before it reaches the textarea, so it
// never inserts a newline; in multiline mode plain enter falls through to
// the textarea's default InsertNewline binding.
func (m modal) IsSubmit(msg tea.KeyPressMsg) bool {
	if m.multiline {
		return key.Matches(msg, m.submitMulti)
	}
	return key.Matches(msg, m.submitSingle)
}

func (m modal) Value() string {
	return strings.TrimSpace(m.area.Value())
}

// SetWidth fits the modal to the terminal width.
func (m *modal) SetWidth(termWidth int) {
	w := min(64, max(termWidth-8, 20))
	m.area.SetWidth(w - 4) // border + padding
}

func (m modal) Update(msg tea.Msg) (modal, tea.Cmd) {
	var cmd tea.Cmd
	m.area, cmd = m.area.Update(msg)
	return m, cmd
}

func (m modal) helpText() string {
	if m.multiline {
		return "⌘+enter save · enter newline · esc cancel"
	}
	return "enter save · esc cancel"
}

func (m modal) View(styles Styles) string {
	return styles.ModalBorder.Render(lipgloss.JoinVertical(lipgloss.Left,
		styles.ModalTitle.Render(m.title),
		m.area.View(),
		styles.Dimmed.Render(m.helpText())))
}
