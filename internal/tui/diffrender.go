package tui

import (
	"fmt"
	"image/color"
	"strconv"
	"strings"
	"sync"

	"charm.land/lipgloss/v2"
	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	chromastyles "github.com/alecthomas/chroma/v2/styles"
	"github.com/charmbracelet/x/ansi"

	"github.com/jwstover/tend/internal/vcs"
)

// DiffRenderOpts configures RenderRows.
type DiffRenderOpts struct {
	Styles Styles
	// Highlight turns chroma syntax coloring on. Off gives plain code text
	// (the add/remove backgrounds still apply), which is stable for goldens.
	Highlight bool
}

// lexerCache holds the chroma lexer per file path; a nil entry records "no
// lexer matches", so the lookup is done once per file rather than per line.
var lexerCache sync.Map // path -> chroma.Lexer (possibly nil)

func lexerFor(path string) chroma.Lexer {
	if v, ok := lexerCache.Load(path); ok {
		l, _ := v.(chroma.Lexer)
		return l
	}
	l := lexers.Match(path)
	if l != nil {
		l = chroma.Coalesce(l)
	}
	lexerCache.Store(path, l)
	return l
}

// RenderRows renders rows[from:from+height] at the given width, one screen
// line per row, truncated with an ellipsis and never wrapped. cursor is the
// row index to highlight (-1 for none). Only the window is touched, so cost
// is independent of the diff's size.
func RenderRows(rows []Row, from, height, width, cursor int, opts DiffRenderOpts) string {
	if from < 0 {
		from = 0
	}
	end := min(from+height, len(rows))
	if from >= end || width <= 0 {
		return ""
	}
	gw := gutterWidth(rows)
	st := opts.Styles
	chStyle := chromastyles.Get(st.DiffChroma)
	var sb strings.Builder
	for i := from; i < end; i++ {
		if i > from {
			sb.WriteByte('\n')
		}
		sb.WriteString(renderRow(rows[i], i == cursor, gw, width, opts, chStyle))
	}
	return sb.String()
}

// gutterWidth is the digit width of the largest line number in rows.
func gutterWidth(rows []Row) int {
	m := 0
	for _, r := range rows {
		m = max(m, r.OldNo, r.NewNo)
	}
	return max(len(strconv.Itoa(m)), 1)
}

func renderRow(r Row, cur bool, gw, width int, opts DiffRenderOpts, chStyle *chroma.Style) string {
	st := opts.Styles
	g := st.Glyphs
	bar := " "
	if cur {
		bar = st.SelBar.Render(g.SelBar)
	}
	body := width - 1 // the cursor bar column
	var line string
	switch r.Kind {
	case RowSection:
		line = st.DiffSection.Render(ansi.Truncate(r.Text, body, g.Ellipsis))
	case RowNotice:
		line = st.Muted.Render(ansi.Truncate("  "+r.Text, body, g.Ellipsis))
	case RowHunkHeader:
		line = st.DiffHunk.Render(ansi.Truncate(r.Text, body, g.Ellipsis))
	case RowFileHeader:
		line = renderFileHeader(r, body, st)
	default:
		line = renderCode(r, cur, gw, body, opts, chStyle)
	}
	if cur && !r.Code() {
		// Headers have no add/remove color to keep, so tint the whole row.
		line = st.DiffCursor.Render(padTo(line, body))
	}
	return bar + line
}

func renderFileHeader(r Row, width int, st Styles) string {
	g := st.Glyphs
	caret := g.CaretOpen
	if r.Folded {
		caret = g.CaretClosed
	}
	name := r.Path
	if r.Status == vcs.StatusRenamed && r.OldPath != "" && r.OldPath != r.Path {
		name = r.OldPath + " → " + r.Path
	}
	letter := map[vcs.Status]string{
		vcs.StatusAdded: "A", vcs.StatusModified: "M", vcs.StatusDeleted: "D", vcs.StatusRenamed: "R",
	}[r.Status]
	if letter == "" {
		letter = "?"
	}
	counts := fmt.Sprintf("+%d −%d", r.Added, r.Removed)
	// Truncate the name, not the counts: they are what the header is for.
	room := width - ansi.StringWidth(caret+" "+letter+"  ") - ansi.StringWidth("  "+counts)
	name = ansi.Truncate(name, max(room, 1), g.Ellipsis)
	left := caret + " " + letter + " " + name
	return ansi.Truncate(st.DiffFileHeader.Render(left)+"  "+
		st.SubFull.Render("+"+strconv.Itoa(r.Added))+" "+st.Priority[1].Render("−"+strconv.Itoa(r.Removed)), width, g.Ellipsis)
}

func renderCode(r Row, cur bool, gw, width int, opts DiffRenderOpts, chStyle *chroma.Style) string {
	st := opts.Styles
	var base lipgloss.Style
	marker := " "
	switch r.Kind {
	case RowAdd:
		base, marker = st.DiffAdd, "+"
	case RowDel:
		base, marker = st.DiffDel, "-"
	default:
		base = lipgloss.NewStyle()
		if cur {
			base = st.DiffCursor
		}
	}
	num := func(n int) string {
		if n == 0 {
			return strings.Repeat(" ", gw)
		}
		return fmt.Sprintf("%*d", gw, n)
	}
	gutter := base.Inherit(st.DiffGutter).Render(num(r.OldNo) + " " + num(r.NewNo) + " ")
	mk := base.Render(marker + " ")
	prefix := gutter + mk
	room := width - ansi.StringWidth(num(0)+" "+num(0)+" ") - 2
	if room < 1 {
		return ansi.Truncate(prefix, width, "")
	}
	text := strings.ReplaceAll(r.Text, "\t", "    ")
	var code string
	if opts.Highlight {
		code = highlightLine(text, r.Path, base, chStyle)
	} else {
		code = base.Render(text)
	}
	code = ansi.Truncate(code, room, st.Glyphs.Ellipsis)
	// Extend the add/remove (or cursor) background to the full width.
	if pad := room - ansi.StringWidth(code); pad > 0 {
		code += base.Render(strings.Repeat(" ", pad))
	}
	return prefix + code
}

// highlightLine tokenizes one line with the lexer for path and styles each
// token over base's background. Per-line tokenizing loses multi-line
// context (block comments, raw strings), which is good enough for a diff.
// TODO(owner): word-level highlighting within changed lines (go-udiff).
func highlightLine(text, path string, base lipgloss.Style, chStyle *chroma.Style) string {
	lx := lexerFor(path)
	if lx == nil || text == "" {
		return base.Render(text)
	}
	it, err := lx.Tokenise(nil, text)
	if err != nil {
		return base.Render(text)
	}
	var sb strings.Builder
	for _, t := range it.Tokens() {
		v := strings.TrimRight(t.Value, "\n")
		if v == "" {
			continue
		}
		s := base
		e := chStyle.Get(t.Type)
		if e.Colour.IsSet() {
			s = s.Foreground(chromaColor(e.Colour))
		}
		if e.Bold == chroma.Yes {
			s = s.Bold(true)
		}
		if e.Italic == chroma.Yes {
			s = s.Italic(true)
		}
		sb.WriteString(s.Render(v))
	}
	return sb.String()
}

func chromaColor(c chroma.Colour) color.Color { return lipgloss.Color(c.String()) }

func padTo(s string, width int) string {
	if pad := width - ansi.StringWidth(s); pad > 0 {
		return s + strings.Repeat(" ", pad)
	}
	return s
}
