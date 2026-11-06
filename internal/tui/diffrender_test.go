package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/jwstover/tend/internal/vcs"
)

func plainOpts() DiffRenderOpts {
	st := DefaultStyles()
	st.Glyphs = unicodeGlyphs()
	return DiffRenderOpts{Styles: st}
}

func renderPlain(rows []Row, from, height, width, cursor int) string {
	return ansi.Strip(RenderRows(rows, from, height, width, cursor, plainOpts()))
}

func TestRenderRowsGolden(t *testing.T) {
	f := modFile("a.go")
	f.Hunks[0].Header = "func main()"
	f.Hunks[0].Lines[1].Text = "this removed line is far too long to fit"
	rows := BuildRows(vcs.Diff{Files: []vcs.File{f}}, vcs.Diff{}, nil)

	got := renderPlain(rows, 0, 10, 30, 3)
	want := strings.Join([]string{
		" ▾ M a.go  +1 −1",
		" @@ -10,4 +10,4 @@ func main()",
		" 10 10   a",
		"▌11    - this removed line is…",
		"    11 + B",
		" 12 12   c",
	}, "\n")
	// Compare after trimming trailing padding, which the goldens do not pin.
	trim := func(s string) string {
		ls := strings.Split(s, "\n")
		for i := range ls {
			ls[i] = strings.TrimRight(ls[i], " ")
		}
		return strings.Join(ls, "\n")
	}
	if trim(got) != trim(want) {
		t.Fatalf("got:\n%s\nwant:\n%s", trim(got), trim(want))
	}
}

func TestRenderRowsWindowAndWidth(t *testing.T) {
	rows := BuildRows(bigDiff(2, 50), vcs.Diff{}, nil)
	out := renderPlain(rows, 5, 7, 20, -1)
	lines := strings.Split(out, "\n")
	if len(lines) != 7 {
		t.Fatalf("window rendered %d lines, want 7", len(lines))
	}
	for _, l := range lines {
		if w := ansi.StringWidth(l); w > 20 {
			t.Fatalf("line %q is %d wide, exceeds 20", l, w)
		}
	}
	if RenderRows(rows, len(rows)+5, 5, 20, 0, plainOpts()) != "" {
		t.Fatal("window past the end should be empty")
	}
}

func TestRenderRowsWideCharsTruncate(t *testing.T) {
	d := vcs.Diff{Files: []vcs.File{{NewPath: "a.txt", Status: vcs.StatusAdded, Hunks: []vcs.Hunk{
		codeHunk(0, 1, vcs.Line{Kind: vcs.LineAdd, Text: strings.Repeat("日本語", 20), NewNo: 1})}}}}
	rows := BuildRows(d, vcs.Diff{}, nil)
	for _, l := range strings.Split(renderPlain(rows, 0, 5, 25, -1), "\n") {
		if ansi.StringWidth(l) > 25 {
			t.Fatalf("%q overflows", l)
		}
	}
}

func TestRenderRowsFoldedAndRename(t *testing.T) {
	d := vcs.Diff{Files: []vcs.File{{OldPath: "o.go", NewPath: "n.go", Status: vcs.StatusRenamed}}}
	rows := BuildRows(d, vcs.Diff{}, map[string]bool{"n.go": true})
	out := renderPlain(rows, 0, 5, 60, -1)
	if !strings.Contains(out, "▸ R o.go → n.go") {
		t.Fatalf("folded rename header = %q", out)
	}
	ascii := plainOpts()
	ascii.Styles.Glyphs = asciiGlyphs()
	if out := ansi.Strip(RenderRows(rows, 0, 5, 60, -1, ascii)); !strings.Contains(out, "> R o.go") {
		t.Fatalf("ascii caret missing: %q", out)
	}
}

func TestRenderRowsHighlightSmoke(t *testing.T) {
	rows := BuildRows(vcs.Diff{Files: []vcs.File{modFile("a.go")}}, vcs.Diff{}, nil)
	rows[2].Text = `func main() { return "x" }`
	on := DiffRenderOpts{Styles: DefaultStyles(), Highlight: true}
	out := RenderRows(rows, 0, 10, 80, 2, on)
	if ansi.Strip(out) == out {
		t.Fatal("expected ANSI styling in highlighted output")
	}
	if !strings.Contains(ansi.Strip(out), `func main() { return "x" }`) {
		t.Fatalf("highlighting altered the text: %q", ansi.Strip(out))
	}
	// An unknown extension falls back to plain text without panicking.
	rows[2].Path = "weird.zzzz"
	_ = RenderRows(rows, 0, 10, 80, 2, on)
}
