package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/jwstover/tend/internal/task"
)

// Global task search is the `gf` chord ("find"): an overlay in the
// workflow picker's mould where typing a query lists matching tasks from
// every project -- done and canceled ones included -- by title, tag and
// body. It is separate from the `/` filter, which narrows the current
// view; this one finds a task anywhere. Picking a result hands off to
// `gt`'s jump (gototask.go), so the project column, the completed toggle
// and the parents' disclosure all sort themselves out.

const (
	// searchLimit caps the rows a query returns: more than a screenful is
	// refined by typing, not scrolled.
	searchLimit = 50
	// searchVisible is how many result rows the overlay shows at once.
	searchVisible = 8
)

// searchResultsMsg carries the matches for one query. The query rides
// along so a slow reply for an older keystroke cannot overwrite the
// results of the current one.
type searchResultsMsg struct {
	query   string
	results []task.Task
	err     error
}

func (a *app) openSearch() {
	a.searchOpen = true
	a.searchQuery = ""
	a.searchResults = nil
	a.searchSel = 0
}

func (a *app) closeSearch() {
	a.searchOpen = false
	a.searchQuery = ""
	a.searchResults = nil
	a.searchSel = 0
}

// searchCmd runs the query off the update loop.
func (a app) searchCmd(query string) tea.Cmd {
	return func() tea.Msg {
		res, err := a.store.SearchTasks(a.ctx, query, searchLimit)
		return searchResultsMsg{query: query, results: res, err: err}
	}
}

// applySearchResults installs a reply if it still answers the query on
// screen.
func (a app) applySearchResults(msg searchResultsMsg) (tea.Model, tea.Cmd) {
	if !a.searchOpen || msg.query != a.searchQuery {
		return a, nil
	}
	if msg.err != nil {
		a.status = flash{text: msg.err.Error(), isErr: true}
		return a, nil
	}
	a.searchResults = msg.results
	a.searchSel = min(a.searchSel, max(len(msg.results)-1, 0))
	return a, nil
}

// requery re-runs the search after the query text changed.
func (a app) requery() (tea.Model, tea.Cmd) {
	a.searchSel = 0
	if strings.TrimSpace(a.searchQuery) == "" {
		a.searchResults = nil
		return a, nil
	}
	return a, a.searchCmd(a.searchQuery)
}

// handleSearchKey owns the keyboard while the overlay is open: type to
// search, arrows or ctrl-n/ctrl-p move, enter jumps to the highlight, esc
// dismisses. No digit shortcuts here -- digits are search text.
func (a app) handleSearchKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		a.closeSearch()
		return a, nil
	case "enter":
		if a.searchSel < 0 || a.searchSel >= len(a.searchResults) {
			return a, nil
		}
		id := a.searchResults[a.searchSel].ID
		a.closeSearch()
		return a, a.gotoTaskCmd(id)
	case "up", "ctrl+p":
		if a.searchSel > 0 {
			a.searchSel--
		}
		return a, nil
	case "down", "ctrl+n":
		if a.searchSel < len(a.searchResults)-1 {
			a.searchSel++
		}
		return a, nil
	case "backspace":
		if r := []rune(a.searchQuery); len(r) > 0 {
			a.searchQuery = string(r[:len(r)-1])
			return a.requery()
		}
		return a, nil
	}
	if msg.Text != "" {
		a.searchQuery += msg.Text
		return a.requery()
	}
	return a, nil
}

// bodySnippet returns a one-line excerpt of body around the first term
// that occurs in it, or "" when no term does (the match was the title or
// a tag). Whitespace runs collapse so a multi-line body reads as a line.
func bodySnippet(body string, terms []string, width int) string {
	flat := strings.Join(strings.Fields(body), " ")
	r := []rune(flat)
	lower := []rune(strings.ToLower(flat))
	if len(lower) != len(r) { // case-folding changed the length; don't risk bad offsets
		lower = r
	}
	for _, term := range terms {
		tr := []rune(strings.ToLower(term))
		if len(tr) == 0 {
			continue
		}
		at := indexRunes(lower, tr)
		if at < 0 {
			continue
		}
		start := max(at-width/4, 0)
		end := min(start+width, len(r))
		start = max(end-width, 0)
		out := string(r[start:end])
		if start > 0 {
			out = "…" + out
		}
		if end < len(r) {
			out += "…"
		}
		return out
	}
	return ""
}

func indexRunes(hay, needle []rune) int {
	for i := 0; i+len(needle) <= len(hay); i++ {
		match := true
		for j := range needle {
			if hay[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

// searchView renders the overlay in the workflow picker's mould: a title,
// a divider, the query prompt, then the windowed result rows, each with
// id, state, project and title and, when the body matched, a snippet line.
func (a app) searchView() string {
	s, g := a.styles, a.styles.Glyphs
	w := max(a.width, 20)
	cb := s.CardBorder
	hbar := strings.Repeat(g.RuleH, w-4)
	inner := w - 5

	row := func(content string) string {
		gap := max(inner-lipgloss.Width(content), 0)
		return "  " + cb.Render(g.RuleV) + " " + content +
			strings.Repeat(" ", gap) + cb.Render(g.RuleV)
	}

	names := make(map[int64]string, len(a.projects))
	for _, p := range a.projects {
		names[p.ID] = p.Name
	}

	lines := []string{"  " + cb.Render(g.BoxTL+hbar+g.BoxTR)}
	lines = append(lines, row(s.Accent.Bold(true).Render("⌕ ")+
		s.Title.Render("search tasks")+
		s.Muted.Render("  title, tags and body · ⏎ jumps")))
	lines = append(lines, "  "+cb.Render(g.TeeRight+hbar+g.TeeLeft))
	lines = append(lines, row(s.Accent.Bold(true).Render("❯ ")+
		s.Title.Render(a.searchQuery)+s.Accent.Render("▏")))

	switch {
	case strings.TrimSpace(a.searchQuery) == "":
		lines = append(lines, row("  "+s.Muted.Render("type to search every project, done tasks included")))
	case len(a.searchResults) == 0:
		lines = append(lines, row("  "+s.Muted.Render("no matching tasks")))
	}

	terms := strings.Fields(a.searchQuery)
	n := len(a.searchResults)
	sel := min(a.searchSel, max(n-1, 0))
	// Scroll the window so the highlight stays inside it.
	first := max(min(sel-searchVisible+1, n-searchVisible), 0)
	if sel < first {
		first = sel
	}
	last := min(first+searchVisible, n)
	for i := first; i < last; i++ {
		t := a.searchResults[i]
		meta := fmt.Sprintf("#%d %s", t.ID, t.State)
		if name := names[t.ProjectID]; name != "" {
			meta += " · " + name
		}
		title := truncTail(t.Title, max(inner-lipgloss.Width(meta)-8, 10), g.Ellipsis)
		if i == sel {
			lines = append(lines, row(s.SelBar.Render(g.SelBar+" ")+s.Title.Render(title)+"  "+s.Muted.Render(meta)))
		} else {
			lines = append(lines, row("  "+s.Dimmed.Render(title)+"  "+s.Muted.Render(meta)))
		}
		if snip := bodySnippet(t.BodyMD, terms, max(inner-6, 10)); snip != "" {
			lines = append(lines, row("    "+s.Muted.Render(snip)))
		}
	}
	if n > last-first {
		lines = append(lines, row("  "+s.Muted.Render(fmt.Sprintf("%d–%d of %d", first+1, last, n))))
	}
	lines = append(lines, "  "+cb.Render(g.BoxBL+hbar+g.BoxBR))
	return strings.Join(lines, "\n")
}
