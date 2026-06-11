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
// It has two input modes: multiline (enter inserts a newline, ctrl+enter
// submits) and single-line (enter submits).
type modal struct {
	kind      modalKind
	multiline bool
	title     string
	target    int64  // task the modal acts on
	extra     string // captured context, e.g. the old body for modalLog
	area      textarea.Model
	rows      int // textarea height in multiline mode, from SetSize

	submitMulti  key.Binding
	submitSingle key.Binding
}

func newModal() modal {
	ta := textarea.New()
	ta.ShowLineNumbers = false
	ta.Prompt = ""
	st := textarea.DefaultStyles(true)
	// The default focused cursor-line background fights the modal border
	// colors and makes the text hard to read.
	st.Focused.CursorLine = lipgloss.NewStyle()
	ta.SetStyles(st)
	return modal{
		area: ta,
		// alt+enter covers terminals without kitty-protocol support,
		// where ctrl+enter is indistinguishable from plain enter.
		submitMulti:  key.NewBinding(key.WithKeys("ctrl+enter", "alt+enter")),
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
		m.area.SetHeight(max(m.rows, 6))
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

// SetSize fits the modal to the terminal.
func (m *modal) SetSize(termWidth, termHeight int) {
	w := min(100, max(termWidth*3/4, 20))
	m.area.SetWidth(w - 4) // border + padding
	m.rows = min(max(termHeight/2-4, 6), 20)
	if m.Active() && m.multiline {
		m.area.SetHeight(m.rows)
	}
}

func (m modal) Update(msg tea.Msg) (modal, tea.Cmd) {
	var cmd tea.Cmd
	m.area, cmd = m.area.Update(msg)
	return m, cmd
}

func (m modal) helpText() string {
	if m.multiline {
		return "ctrl+enter save · enter newline · esc cancel"
	}
	return "enter save · esc cancel"
}

func (m modal) View(styles Styles) string {
	return styles.ModalBorder.Render(lipgloss.JoinVertical(lipgloss.Left,
		styles.ModalTitle.Render(m.title),
		m.area.View(),
		styles.Dimmed.Render(m.helpText())))
}
