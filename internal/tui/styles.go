package tui

import (
	"image/color"

	"charm.land/lipgloss/v2"

	"github.com/jwstover/tend/internal/task"
)

// token is one semantic color from the design spec, carrying both
// background variants. Components never see a hex directly — they go
// through the resolved palette.
type token struct{ dark, light string }

// Semantic tokens (design spec §01). Each pairs the dark and light hex.
var (
	tokFg     = token{"#C9D1D9", "#1B2026"}
	tokFgDim  = token{"#8B949E", "#5A6573"}
	tokMuted  = token{"#6E7681", "#767676"}
	tokFaint  = token{"#4A525C", "#9AA0A6"} // light value interpolated; spec omits it
	tokBorder = token{"#30363D", "#C5CAD1"}
	tokRule   = token{"#21262D", "#DADADA"}

	tokAccent   = token{"#4A90D9", "#2066AC"}
	tokAccentBg = token{"#1A2744", "#E9F0F7"}
	tokLink     = token{"#1AB8E8", "#007A9E"}

	tokInbox   = token{"#E8833A", "#C2410C"}
	tokTodo    = token{"#8B98A8", "#5A6573"}
	tokDoing   = token{"#F2C94C", "#A16207"}
	tokBlocked = token{"#F85149", "#DC2626"}
	tokDone    = token{"#6E7681", "#909090"} // gray: done recedes, it doesn't celebrate
	tokSomeday = token{"#A371F7", "#4E46D7"}

	tokComplete  = token{"#3FB950", "#16A34A"} // completion signals ONLY (▣, full N/M)
	tokOverdue   = token{"#F85149", "#DC2626"}
	tokDueToday  = token{"#E3B341", "#C66A00"}
	tokDueFuture = token{"#8B949E", "#767676"}
	tokP1        = token{"#F85149", "#DC2626"}
	tokP2        = token{"#E3B341", "#C66A00"}
	tokP3        = token{"#58A6FF", "#2066AC"}
	tokP4        = token{"#6E7681", "#767676"} // D: quiet; spec only names 1–3
)

// palette is the token table resolved for the active terminal background.
type palette struct {
	Fg, FgDim, Muted, Faint color.Color
	Border, Rule            color.Color
	Accent, AccentBg, Link  color.Color

	Inbox, Todo, Doing, Blocked, Done, Someday color.Color

	Complete, Overdue, DueToday, DueFuture color.Color
	P1, P2, P3, P4                         color.Color
}

func newPalette(isDark bool) palette {
	pick := func(t token) color.Color {
		return lipgloss.LightDark(isDark)(lipgloss.Color(t.light), lipgloss.Color(t.dark))
	}
	return palette{
		Fg: pick(tokFg), FgDim: pick(tokFgDim), Muted: pick(tokMuted), Faint: pick(tokFaint),
		Border: pick(tokBorder), Rule: pick(tokRule),
		Accent: pick(tokAccent), AccentBg: pick(tokAccentBg), Link: pick(tokLink),
		Inbox: pick(tokInbox), Todo: pick(tokTodo), Doing: pick(tokDoing),
		Blocked: pick(tokBlocked), Done: pick(tokDone), Someday: pick(tokSomeday),
		Complete: pick(tokComplete), Overdue: pick(tokOverdue),
		DueToday: pick(tokDueToday), DueFuture: pick(tokDueFuture),
		P1: pick(tokP1), P2: pick(tokP2), P3: pick(tokP3), P4: pick(tokP4),
	}
}

// glyphs is the symbol set, chosen once at startup. Widths are identical
// in both sets so nothing in the layout depends on which one renders.
type glyphs struct {
	State map[task.State]string

	SelBar                   string // selection gutter bar
	CaretClosed, CaretOpen   string // disclosure carets
	BoxChecked, BoxUnchecked string // sub-task checkboxes
	Flag                     string // priority flag
	Link                     string // detected-URL marker
	Plus                     string // capture flash / quick-add marker
	Pen                      string // edit-saved flash marker
	RuleH, RuleV             string // section rules / pane divider
	TeeDown, TeeUp           string // rule-to-divider joins (┬ ┴)
	TeeRight, TeeLeft        string // box-internal divider joins (├ ┤)
	Ellipsis                 string // title truncation

	ProgressOn, ProgressOff    string // triage progress segments (▰ ▱)
	BoxTL, BoxTR, BoxBL, BoxBR string // triage card corners (┌ ┐ └ ┘)
	ZeroMark                   string // inbox-zero celebration mark
}

func unicodeGlyphs() glyphs {
	return glyphs{
		State: map[task.State]string{
			task.StateInbox:   "●",
			task.StateTodo:    "○",
			task.StateDoing:   "◐",
			task.StateBlocked: "⊘",
			task.StateDone:    "✓",
			task.StateSomeday: "◇",
		},
		SelBar:      "▌",
		CaretClosed: "▸", CaretOpen: "▾",
		BoxChecked: "▣", BoxUnchecked: "▢",
		Flag: "⚑", Link: "↗",
		Plus: "✚", Pen: "✎",
		RuleH: "─", RuleV: "│", TeeDown: "┬", TeeUp: "┴",
		TeeRight: "├", TeeLeft: "┤",
		Ellipsis: "…",

		ProgressOn: "▰", ProgressOff: "▱",
		BoxTL: "┌", BoxTR: "┐", BoxBL: "└", BoxBR: "┘",
		ZeroMark: "◖ ◗",
	}
}

// asciiGlyphs is the documented fallback set for terminals without
// usable Unicode.
// TODO(owner): select via termenv capabilities at startup; for now the
// unicode set is always used.
//
//nolint:unused // kept until capability detection wires it in
func asciiGlyphs() glyphs {
	return glyphs{
		State: map[task.State]string{
			task.StateInbox:   "*",
			task.StateTodo:    "o",
			task.StateDoing:   ">",
			task.StateBlocked: "!",
			task.StateDone:    "x",
			task.StateSomeday: "~",
		},
		SelBar:      ">",
		CaretClosed: ">", CaretOpen: "v",
		BoxChecked: "[x]", BoxUnchecked: "[ ]",
		Flag: "!", Link: "->",
		Plus: "+", Pen: "~",
		RuleH: "-", RuleV: "|", TeeDown: "+", TeeUp: "+",
		TeeRight: "+", TeeLeft: "+",
		Ellipsis: ".",

		ProgressOn: "#", ProgressOff: "·",
		BoxTL: "+", BoxTR: "+", BoxBL: "+", BoxBR: "+",
		ZeroMark: `\o/`,
	}
}

// Styles collects every lipgloss style the TUI uses, derived from the
// semantic palette — never an inline hex outside this file.
type Styles struct {
	Palette palette
	Glyphs  glyphs

	// Chrome.
	HeaderApp  lipgloss.Style // "tend" — accent bold
	HeaderSep  lipgloss.Style // "·" separators — faint
	HeaderView lipgloss.Style // view label — bold fg
	InboxNudge lipgloss.Style // "● N in inbox" — inbox orange
	CountNum   lipgloss.Style // shown count — bold fg
	CountLabel lipgloss.Style // "shown" — muted
	Rule       lipgloss.Style // ─ rules and the │ pane divider
	FooterKey  lipgloss.Style // key hints — accent bold
	FooterDesc lipgloss.Style // hint labels — muted

	// List rows.
	SelBar     lipgloss.Style // ▌ gutter bar
	Title      lipgloss.Style
	TitleDone  lipgloss.Style // gray + strikethrough
	Caret      lipgloss.Style
	Project    lipgloss.Style // #name meta column
	DueOver    lipgloss.Style
	DueToday   lipgloss.Style
	DueFuture  lipgloss.Style
	SubFull    lipgloss.Style // N/M when complete — the only green
	SubPartial lipgloss.Style
	GroupRule  lipgloss.Style // ─ fill in group headers
	GroupCount lipgloss.Style

	// Triage view.
	ProgressDone lipgloss.Style // ▰ filled segments — complete green
	ProgressRest lipgloss.Style // ▱ unfilled segments — faint
	CardBorder   lipgloss.Style // the capture card's box — border token
	InboxZero    lipgloss.Style // "inbox zero" — complete green bold

	// Standup view.
	DayHeading lipgloss.Style // "Today · Tue Jul 7" day sections — green bold (owner's pick)

	// Shared text levels + per-state/priority color maps.
	Normal   lipgloss.Style
	Dimmed   lipgloss.Style // fgDim
	Muted    lipgloss.Style
	Faint    lipgloss.Style
	Accent   lipgloss.Style
	State    map[task.State]lipgloss.Style
	Priority map[int64]lipgloss.Style

	// Detail pane.
	DetailID    lipgloss.Style // "#7" — muted
	DetailLabel lipgloss.Style // "project" / "due" / "pri" — muted
	DetailFaint lipgloss.Style // created/updated line
	SubHeader   lipgloss.Style // "SUB-TASKS" — accent bold
	CheckDone   lipgloss.Style // ▣ — complete green
	CheckOpen   lipgloss.Style // ▢ — muted
	SubDoneText lipgloss.Style // done sub-task title — dim + strikethrough
	SubSelText  lipgloss.Style // checklist row of the selected node — accent bold
	Link        lipgloss.Style

	// Inputs, panels, modals (pre-existing chrome).
	Error       lipgloss.Style
	PromptLabel lipgloss.Style
	PanelBorder lipgloss.Style
	PanelTitle  lipgloss.Style
	PanelKey    lipgloss.Style
	PanelDesc   lipgloss.Style
	ModalBorder lipgloss.Style
	ModalTitle  lipgloss.Style
}

// DefaultStyles resolves the palette for a dark background.
// TODO(owner): wire tea.RequestBackgroundColor / tea.BackgroundColorMsg
// and rebuild with newStyles(msg.IsDark()).
func DefaultStyles() Styles {
	return newStyles(true)
}

func newStyles(isDark bool) Styles {
	p := newPalette(isDark)
	fg := func(c color.Color) lipgloss.Style { return lipgloss.NewStyle().Foreground(c) }

	return Styles{
		Palette: p,
		Glyphs:  unicodeGlyphs(),

		HeaderApp:  fg(p.Accent).Bold(true),
		HeaderSep:  fg(p.Faint),
		HeaderView: fg(p.Fg).Bold(true),
		InboxNudge: fg(p.Inbox),
		CountNum:   fg(p.Fg).Bold(true),
		CountLabel: fg(p.Muted),
		Rule:       fg(p.Border),
		FooterKey:  fg(p.Accent).Bold(true),
		FooterDesc: fg(p.Muted),

		SelBar:     fg(p.Accent).Bold(true),
		Title:      fg(p.Fg),
		TitleDone:  fg(p.Done).Strikethrough(true),
		Caret:      fg(p.Muted),
		Project:    fg(p.FgDim),
		DueOver:    fg(p.Overdue).Bold(true),
		DueToday:   fg(p.DueToday).Bold(true),
		DueFuture:  fg(p.DueFuture),
		SubFull:    fg(p.Complete),
		SubPartial: fg(p.Muted),
		GroupRule:  fg(p.Rule),
		GroupCount: fg(p.Muted),

		ProgressDone: fg(p.Complete),
		ProgressRest: fg(p.Faint),
		CardBorder:   fg(p.Border),
		InboxZero:    fg(p.Complete).Bold(true),

		DayHeading: fg(p.Complete).Bold(true),

		Normal: lipgloss.NewStyle(),
		Dimmed: fg(p.FgDim),
		Muted:  fg(p.Muted),
		Faint:  fg(p.Faint),
		Accent: fg(p.Accent),
		State: map[task.State]lipgloss.Style{
			task.StateInbox:   fg(p.Inbox),
			task.StateTodo:    fg(p.Todo),
			task.StateDoing:   fg(p.Doing),
			task.StateBlocked: fg(p.Blocked),
			task.StateDone:    fg(p.Done),
			task.StateSomeday: fg(p.Someday),
		},
		Priority: map[int64]lipgloss.Style{
			1: fg(p.P1).Bold(true),
			2: fg(p.P2),
			3: fg(p.P3),
			4: fg(p.P4),
		},

		DetailID:    fg(p.Muted),
		DetailLabel: fg(p.Muted),
		DetailFaint: fg(p.Faint),
		SubHeader:   fg(p.Accent).Bold(true),
		CheckDone:   fg(p.Complete),
		CheckOpen:   fg(p.Muted),
		SubDoneText: fg(p.Muted).Strikethrough(true),
		SubSelText:  fg(p.Accent).Bold(true),
		Link:        fg(p.Link),

		Error:       fg(p.Blocked).Padding(0, 1),
		PromptLabel: fg(p.Accent).Bold(true).Padding(0, 1),
		PanelBorder: lipgloss.NewStyle().
			Border(lipgloss.NormalBorder(), true, false, false, false).
			BorderForeground(p.Border).
			Padding(0, 1),
		PanelTitle: fg(p.Accent).Bold(true),
		PanelKey:   fg(p.Accent).Bold(true),
		PanelDesc:  fg(p.FgDim),
		ModalBorder: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(p.Accent).
			Padding(0, 1),
		ModalTitle: fg(p.Accent).Bold(true),
	}
}
