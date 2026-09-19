package tui

import (
	"fmt"
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
		{icon: "▸", label: "Toggle projects column", hint: "[", aliases: []string{"projects"},
			act: func(a app) (tea.Model, tea.Cmd) {
				if a.mode != modeList {
					a.status = flash{text: "projects column is list-view only"}
					return a, nil
				}
				return a.toggleProjects()
			}},
		{icon: "✚", label: "New project", aliases: []string{"newproject", "mkproject"},
			act: func(a app) (tea.Model, tea.Cmd) {
				return a, a.openPrompt(promptNewProject, "new project: ", 0)
			}},
		{icon: "▸", label: "Move task to project", hint: "P", aliases: []string{"move", "project"},
			act: func(a app) (tea.Model, tea.Cmd) {
				t, ok := a.selected()
				if !ok {
					a.status = flash{text: "nothing selected"}
					return a, nil
				}
				a.openProjectPicker(t)
				return a, nil
			}},
		{icon: "▸", label: "Move task to parent", hint: "m", aliases: []string{"parent", "reparent", "promote", "demote"},
			act: func(a app) (tea.Model, tea.Cmd) {
				t, ok := a.selected()
				if !ok {
					a.status = flash{text: "nothing selected"}
					return a, nil
				}
				return a, a.loadParentCandidates(t)
			}},
		{icon: "⊘", label: "Edit dependencies (blocked by)", hint: "b", aliases: []string{"dependencies", "depends", "blockers", "blockedby"},
			act: func(a app) (tea.Model, tea.Cmd) {
				t, ok := a.selected()
				if !ok {
					a.status = flash{text: "nothing selected"}
					return a, nil
				}
				return a, a.loadDependencyCandidates(t)
			}},
		{icon: "≡", label: "Group by state", hint: "gs", aliases: []string{"group", "groupstate"},
			act: func(a app) (tea.Model, tea.Cmd) { return a.setGroupBy(groupByState) }},
		{icon: "⚑", label: "Group by priority", hint: "gp", aliases: []string{"grouppriority"},
			act: func(a app) (tea.Model, tea.Cmd) { return a.setGroupBy(groupByPriority) }},
		{icon: "◉", label: "Group by agent status", hint: "ga", aliases: []string{"groupagent"},
			act: func(a app) (tea.Model, tea.Cmd) { return a.setGroupBy(groupByAgent) }},
		{icon: "◎", label: "Triage the inbox", hint: "i", aliases: []string{"triage", "inbox"},
			act: func(a app) (tea.Model, tea.Cmd) {
				a.startTriage()
				return a, a.loadTasks(modeTriage)
			}},
		{icon: "✚", label: "Quick-add to inbox", hint: "n",
			act: func(a app) (tea.Model, tea.Cmd) {
				return a, a.openPrompt(promptAdd, "add: ", 0)
			}},
		{icon: "▤", label: "Standup view", hint: "S", aliases: []string{"standup", "log"},
			act: func(a app) (tea.Model, tea.Cmd) {
				a.startStandup()
				return a, a.loadStandup()
			}},
		{icon: "⛭", label: "Workflows view", hint: "W", aliases: []string{"workflows", "workflow", "wf"},
			act: func(a app) (tea.Model, tea.Cmd) {
				a.startWorkflows()
				return a, a.loadWorkflows(0)
			}},
		{icon: "◉", label: "Agents view", hint: "A", aliases: []string{"agents", "sessions"},
			act: func(a app) (tea.Model, tea.Cmd) {
				a.startAgents()
				return a, a.loadAgentSessions()
			}},
		{icon: "⚡", label: "Run workflow on task", hint: "w", aliases: []string{"run"},
			act: func(a app) (tea.Model, tea.Cmd) {
				t, ok := a.selected()
				if !ok {
					a.status = flash{text: "nothing selected"}
					return a, nil
				}
				return a, a.loadWorkflowsForRun(t)
			}},
		{icon: "◉", label: "Watch workflow run on task", hint: "v", aliases: []string{"watch", "runs"},
			act: func(a app) (tea.Model, tea.Cmd) {
				t, ok := a.selected()
				if !ok {
					a.status = flash{text: "nothing selected"}
					return a, nil
				}
				return a, a.loadRunsForView(t)
			}},
		{icon: "✎", label: "Capture a note", hint: "N", aliases: []string{"note"},
			act: func(a app) (tea.Model, tea.Cmd) {
				return a, a.modal.Open(modalLog, true, "note", 0, "")
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
		{icon: "✎", label: "Rename selected task", hint: "R", aliases: []string{"rename"},
			act: func(a app) (tea.Model, tea.Cmd) {
				if t, ok := a.selected(); ok {
					return a, a.openPromptWith(promptRename, fmt.Sprintf("rename #%d: ", t.ID), t.Title, t.ID)
				}
				a.status = flash{text: "nothing selected"}
				return a, nil
			}},
		{icon: "✗", label: "Delete selected task", hint: "dd", aliases: []string{"delete", "rm"},
			act: func(a app) (tea.Model, tea.Cmd) {
				if t, ok := a.selected(); ok {
					a.armTaskDelete(t)
					return a, nil
				}
				a.status = flash{text: "nothing selected"}
				return a, nil
			}},
		{icon: "✗", label: "Quit", hint: "q", aliases: []string{"q", "quit"},
			act: func(a app) (tea.Model, tea.Cmd) { return a, tea.Quit }},
	}
}

// palettePicks is the palette's own filter: `add <text>` (and `a <text>`)
// becomes a synthetic capture entry, preserving the old typed command;
// exact alias matches come next; then the fuzzy hits over the labels.
func palettePicks(query string, cmds []paletteCommand) []paletteCommand {
	raw := strings.TrimSpace(query)
	q := strings.ToLower(raw)

	var out []paletteCommand
	if name, rest, _ := strings.Cut(raw, " "); name != "" {
		rest = strings.TrimSpace(rest)
		if n := strings.ToLower(name); (n == "add" || n == "a") && rest != "" {
			out = append(out, paletteCommand{
				icon: "✚", label: `Add task: "` + rest + `"`,
				act: func(a app) (tea.Model, tea.Cmd) {
					return a, a.captureTask(rest)
				},
			})
		}
	}
	if q == "" {
		return append(out, cmds...)
	}
	var rest []paletteCommand
	for _, c := range cmds {
		if slices.Contains(c.aliases, q) {
			out = append(out, c)
		} else {
			rest = append(rest, c)
		}
	}
	return append(out, fuzzyFilter(query, rest, func(c paletteCommand) string { return c.label })...)
}

func (a *app) openPalette() {
	a.palette = picker[paletteCommand]{open: true, items: a.paletteCommands(), filter: palettePicks}
}

func (a *app) closePalette() {
	a.palette = picker[paletteCommand]{}
}

// handlePaletteKey owns the keyboard while the palette is open: type to
// filter, ↑/↓ (or ctrl+p/ctrl+n) to move, ⏎ runs, esc dismisses. Every
// digit is filter text -- the palette is not numbered.
func (a app) handlePaletteKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	action, idx := a.palette.key(msg, a.height)
	switch action {
	case pickerCancel:
		a.closePalette()
		return a, nil
	case pickerPick:
		c, ok := a.palette.at(idx)
		a.closePalette()
		if !ok {
			return a, nil
		}
		return c.act(a)
	}
	return a, nil
}

// paletteView renders the fuzzy-finder box: a prompt row (there is no
// title row above it), then the filtered commands, each with a
// right-aligned key hint.
func (a app) paletteView() string {
	s := a.styles
	return a.palette.render(s, a.width, a.height, pickerView[paletteCommand]{
		row: func(c paletteCommand, selected bool, w int) string {
			icon, title := s.Muted.Render(c.icon+"  "), s.Dimmed.Render(c.label)
			if selected {
				icon, title = s.Accent.Render(c.icon+"  "), s.Title.Bold(true).Render(c.label)
			}
			content := icon + title
			if c.hint != "" {
				gap := max(w-lipgloss.Width(content)-lipgloss.Width(c.hint)-1, 1)
				content += strings.Repeat(" ", gap) + s.Faint.Render(c.hint)
			}
			return content
		},
		noMatch: "no matching commands",
	})
}
