package tui

import (
	"slices"
	"strings"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// paletteCommand is one runnable palette entry. aliases keep the old
// typed-command names (`:q`, `:triage`, `:list`, …) working: an exact
// alias match sorts ahead of label-substring hits.
type paletteCommand struct {
	icon, label, hint string
	aliases           []string
	act               func(a app) (tea.Model, tea.Cmd)
}

// paletteCommands is the full command list, wired to the same behaviors
// as the direct key bindings.
func (a app) paletteCommands() []paletteCommand {
	return []paletteCommand{
		{icon: "▤", label: "Toggle detail pane", hint: "]",
			act: func(a app) (tea.Model, tea.Cmd) {
				if a.mode == modeTriage {
					a.status = flash{text: "no detail pane in triage"}
					return a, nil
				}
				return a.toggleDetail()
			}},
		{icon: "◎", label: "Triage the inbox", hint: "i", aliases: []string{"triage", "inbox"},
			act: func(a app) (tea.Model, tea.Cmd) {
				a.startTriage()
				return a, a.loadTasks(modeTriage)
			}},
		{icon: "✚", label: "Quick-add to inbox", hint: "n",
			act: func(a app) (tea.Model, tea.Cmd) {
				return a, a.openPrompt(promptAdd, "add: ", 0)
			}},
		{icon: "⌕", label: "Search the list", hint: "/",
			act: func(a app) (tea.Model, tea.Cmd) {
				var cmd tea.Cmd
				if a.mode == modeTriage {
					a.mode = modeList
					cmd = a.loadTasks(modeList)
				}
				a.list.SetFilterState(list.Filtering)
				return a, cmd
			}},
		{icon: "≡", label: "Go to list", aliases: []string{"list", "tasks"},
			act: func(a app) (tea.Model, tea.Cmd) {
				a.mode = modeList
				return a, a.loadTasks(modeList)
			}},
		{icon: "?", label: "Show keyboard help", hint: "?",
			act: func(a app) (tea.Model, tea.Cmd) {
				a.helpOpen = true
				return a, nil
			}},
		{icon: "✗", label: "Delete selected task", hint: "dd", aliases: []string{"delete", "rm"},
			act: func(a app) (tea.Model, tea.Cmd) {
				if t, ok := a.selected(); ok {
					return a, a.deleteTask(t)
				}
				a.status = flash{text: "nothing selected"}
				return a, nil
			}},
		{icon: "✗", label: "Quit", hint: "q", aliases: []string{"q", "quit"},
			act: func(a app) (tea.Model, tea.Cmd) { return a, tea.Quit }},
	}
}

// paletteMatches filters the commands against the typed query,
// case-insensitively. `add <text>` (and `a <text>`) becomes a synthetic
// capture entry, preserving the old typed command; exact alias matches
// come next, then label substrings.
func (a app) paletteMatches() []paletteCommand {
	raw := strings.TrimSpace(a.paletteQuery)
	q := strings.ToLower(raw)
	cmds := a.paletteCommands()

	var out []paletteCommand
	if name, rest, _ := strings.Cut(raw, " "); name != "" {
		rest = strings.TrimSpace(rest)
		if n := strings.ToLower(name); (n == "add" || n == "a") && rest != "" {
			out = append(out, paletteCommand{
				icon: "✚", label: `Add task: "` + rest + `"`,
				act: func(a app) (tea.Model, tea.Cmd) {
					return a, a.mutate(flash{kind: flashAdd, text: "captured to inbox: " + rest}, func() error {
						_, err := a.store.AddTask(a.ctx, rest)
						return err
					})
				},
			})
		}
	}
	if q == "" {
		return append(out, cmds...)
	}
	for _, c := range cmds {
		if slices.Contains(c.aliases, q) {
			out = append(out, c)
		}
	}
	for _, c := range cmds {
		if !slices.Contains(c.aliases, q) && strings.Contains(strings.ToLower(c.label), q) {
			out = append(out, c)
		}
	}
	return out
}

func (a *app) openPalette() {
	a.paletteOpen, a.paletteQuery, a.paletteSel = true, "", 0
}

func (a *app) closePalette() {
	a.paletteOpen, a.paletteQuery, a.paletteSel = false, "", 0
}

// handlePaletteKey owns the keyboard while the palette is open: type to
// filter, ↑/↓ (or ctrl+p/ctrl+n) to move, ⏎ runs, esc dismisses.
func (a app) handlePaletteKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	items := a.paletteMatches()
	switch msg.String() {
	case "esc":
		a.closePalette()
		return a, nil
	case "enter":
		sel := a.paletteSel
		a.closePalette()
		if sel >= 0 && sel < len(items) {
			return items[sel].act(a)
		}
		return a, nil
	case "up", "ctrl+p":
		if a.paletteSel > 0 {
			a.paletteSel--
		}
		return a, nil
	case "down", "ctrl+n":
		if a.paletteSel < len(items)-1 {
			a.paletteSel++
		}
		return a, nil
	case "backspace":
		if r := []rune(a.paletteQuery); len(r) > 0 {
			a.paletteQuery = string(r[:len(r)-1])
		}
		a.paletteSel = 0
		return a, nil
	}
	if msg.Text != "" {
		a.paletteQuery += msg.Text
		a.paletteSel = 0
	}
	return a, nil
}

// paletteView renders the fuzzy-finder box: a prompt row, a divider, then
// the filtered commands, each with a right-aligned key hint.
func (a app) paletteView() string {
	s, g := a.styles, a.styles.Glyphs
	w := max(a.width, 20)
	cb := s.CardBorder
	hbar := strings.Repeat(g.RuleH, w-4)

	// Rows sit between `  │ ` and a closing `│` at the last column.
	row := func(content string) string {
		gap := max(w-5-lipgloss.Width(content), 0)
		return "  " + cb.Render(g.RuleV) + " " + content +
			strings.Repeat(" ", gap) + cb.Render(g.RuleV)
	}

	lines := []string{"  " + cb.Render(g.BoxTL+hbar+g.BoxTR)}
	lines = append(lines, row(s.Accent.Bold(true).Render("❯ ")+
		s.Title.Render(a.paletteQuery)+s.Accent.Render("▏")))
	lines = append(lines, "  "+cb.Render(g.TeeRight+hbar+g.TeeLeft))

	items := a.paletteMatches()
	sel := min(a.paletteSel, len(items)-1)
	if len(items) == 0 {
		lines = append(lines, row("  "+s.Muted.Render("no matching commands")))
	}
	for i, it := range items {
		var content string
		if i == sel {
			content = s.SelBar.Render(g.SelBar+" ") +
				s.Accent.Render(it.icon+"  ") + s.Title.Bold(true).Render(it.label)
		} else {
			content = "  " + s.Muted.Render(it.icon+"  ") + s.Dimmed.Render(it.label)
		}
		if it.hint != "" {
			gap := max(w-5-lipgloss.Width(content)-lipgloss.Width(it.hint)-1, 1)
			content += strings.Repeat(" ", gap) + s.Faint.Render(it.hint)
		}
		lines = append(lines, row(content))
	}
	lines = append(lines, "  "+cb.Render(g.BoxBL+hbar+g.BoxBR))
	return strings.Join(lines, "\n")
}
