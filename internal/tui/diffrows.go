package tui

import (
	"fmt"

	"github.com/jwstover/tend/internal/vcs"
)

// RowKind is the role of one row in the flat diff list.
type RowKind int

// Row kinds.
const (
	RowSection    RowKind = iota // "Committed" / "Uncommitted (...)" heading
	RowFileHeader                // one per file; the only row of a folded file
	RowHunkHeader                // @@ -a,b +c,d @@ header
	RowContext
	RowAdd
	RowDel
	RowNotice // "No changes", "Binary file not shown", ...
)

// Section indexes: which of the two diffs a row came from.
const (
	SectionCommitted = iota
	SectionUncommitted
)

// Section headings, emitted only when both diffs have content.
const (
	committedHeading   = "Committed"
	uncommittedHeading = "Uncommitted (assigned in GitButler)"
)

// Row is one display line of the diff tab. Rows are derived data: a viewer
// keeps a cursor by Anchor (FindRow), never by index into a rebuilt slice.
type Row struct {
	Kind    RowKind
	File    int    // index of the file within its section's diff
	Hunk    int    // index of the hunk within the file (code and hunk rows)
	Section int    // SectionCommitted or SectionUncommitted
	Path    string // File.Path(): the fold key, and what an Anchor names
	OldNo   int    // line numbers; 0 where the line does not exist
	NewNo   int
	Text    string // line text, hunk header, section heading or notice

	// File header only.
	Status         vcs.Status
	OldPath        string
	Added, Removed int
	Folded         bool
}

// Code reports whether the row is a line of code (context, add or del).
func (r Row) Code() bool {
	return r.Kind == RowContext || r.Kind == RowAdd || r.Kind == RowDel
}

// BuildRows flattens the committed and uncommitted diffs into rows. A path
// in folded contributes only its file header, in both sections. When both
// diffs have content each gets a section heading; with one, there are none.
func BuildRows(committed, uncommitted vcs.Diff, folded map[string]bool) []Row {
	if committed.Empty() && uncommitted.Empty() {
		return []Row{{Kind: RowNotice, Text: "No changes"}}
	}
	both := !committed.Empty() && !uncommitted.Empty()
	n := 0
	for _, d := range []vcs.Diff{committed, uncommitted} {
		for _, f := range d.Files {
			n += 1 + countLines(f)
		}
	}
	rows := make([]Row, 0, n+4)
	add := func(sec int, heading string, d vcs.Diff) {
		if d.Empty() {
			return
		}
		if both {
			rows = append(rows, Row{Kind: RowSection, Section: sec, Text: heading})
		}
		for fi, f := range d.Files {
			rows = appendFileRows(rows, sec, fi, f, folded[f.Path()])
		}
	}
	add(SectionCommitted, committedHeading, committed)
	add(SectionUncommitted, uncommittedHeading, uncommitted)
	return rows
}

func countLines(f vcs.File) int {
	n := 1
	for _, h := range f.Hunks {
		n += 1 + len(h.Lines)
	}
	return n
}

func appendFileRows(rows []Row, sec, fi int, f vcs.File, fold bool) []Row {
	path := f.Path()
	rows = append(rows, Row{
		Kind: RowFileHeader, File: fi, Section: sec, Path: path,
		Status: f.Status, OldPath: f.OldPath, Added: f.Added, Removed: f.Removed,
		Folded: fold,
	})
	if fold {
		return rows
	}
	if f.Binary {
		return append(rows, Row{Kind: RowNotice, File: fi, Section: sec, Path: path, Text: "Binary file not shown"})
	}
	if len(f.Hunks) == 0 {
		return append(rows, Row{Kind: RowNotice, File: fi, Section: sec, Path: path, Text: "No textual changes"})
	}
	for hi, h := range f.Hunks {
		rows = append(rows, Row{
			Kind: RowHunkHeader, File: fi, Hunk: hi, Section: sec, Path: path,
			Text: hunkHeaderText(h),
		})
		for _, l := range h.Lines {
			k := RowContext
			switch l.Kind {
			case vcs.LineAdd:
				k = RowAdd
			case vcs.LineDel:
				k = RowDel
			}
			rows = append(rows, Row{
				Kind: k, File: fi, Hunk: hi, Section: sec, Path: path,
				OldNo: l.OldNo, NewNo: l.NewNo, Text: l.Text,
			})
		}
	}
	return rows
}

func hunkHeaderText(h vcs.Hunk) string {
	s := fmt.Sprintf("@@ -%d,%d +%d,%d @@", h.OldStart, h.OldLines, h.NewStart, h.NewLines)
	if h.Header != "" {
		s += " " + h.Header
	}
	return s
}

// Anchor names a spot in the diff independent of row indexes, so a cursor
// survives a reload that renumbers rows.
type Anchor struct {
	Path         string
	Section      int
	OldNo, NewNo int
	Kind         RowKind
}

// AnchorAt returns the anchor for rows[i], or the zero Anchor out of range.
func AnchorAt(rows []Row, i int) Anchor {
	if i < 0 || i >= len(rows) {
		return Anchor{}
	}
	r := rows[i]
	return Anchor{Path: r.Path, Section: r.Section, OldNo: r.OldNo, NewNo: r.NewNo, Kind: r.Kind}
}

// FindRow locates a again: the exact line first, then the nearest line in
// the hunk that now covers it, then the file header, then 0.
func FindRow(rows []Row, a Anchor) int {
	if a.Path == "" {
		return 0
	}
	if a.Kind == RowFileHeader || a.Kind == RowHunkHeader || a.Kind == RowNotice || a.Kind == RowSection {
		if i := findHeader(rows, a, true); i >= 0 {
			return i
		}
		if i := findHeader(rows, a, false); i >= 0 {
			return i
		}
		return 0
	}
	for _, sameSec := range []bool{true, false} {
		if i := findExact(rows, a, sameSec); i >= 0 {
			return i
		}
		if i := findByOldNo(rows, a, sameSec); i >= 0 {
			return i
		}
		if i := findNearest(rows, a, sameSec); i >= 0 {
			return i
		}
		if i := findHeader(rows, a, sameSec); i >= 0 {
			return i
		}
	}
	return 0
}

func secOK(r Row, a Anchor, sameSec bool) bool {
	return r.Path == a.Path && (!sameSec || r.Section == a.Section)
}

func findHeader(rows []Row, a Anchor, sameSec bool) int {
	for i, r := range rows {
		if r.Kind == RowFileHeader && secOK(r, a, sameSec) {
			return i
		}
	}
	return -1
}

func findExact(rows []Row, a Anchor, sameSec bool) int {
	for i, r := range rows {
		if r.Code() && secOK(r, a, sameSec) && r.Kind == a.Kind && r.OldNo == a.OldNo && r.NewNo == a.NewNo {
			return i
		}
	}
	return -1
}

// findByOldNo finds a context or removed line by its old number alone. Both
// are lines of the base, so an edit above them shifts their new numbers but
// not their old ones: the common reload, where the cursor must not slide.
func findByOldNo(rows []Row, a Anchor, sameSec bool) int {
	if a.OldNo == 0 || (a.Kind != RowContext && a.Kind != RowDel) {
		return -1
	}
	for i, r := range rows {
		if r.Kind == a.Kind && secOK(r, a, sameSec) && r.OldNo == a.OldNo {
			return i
		}
	}
	return -1
}

// findNearest finds the code row closest to a within the one hunk whose
// line range covers a. A hunk covers a when a's new number lies in the
// hunk's new numbers, or its old number in the hunk's old numbers.
func findNearest(rows []Row, a Anchor, sameSec bool) int {
	type span struct{ lo, hi int }
	type hk struct {
		sec, file, hunk int
	}
	newSp := map[hk]span{}
	oldSp := map[hk]span{}
	widen := func(m map[hk]span, k hk, n int) {
		if n == 0 {
			return
		}
		s, ok := m[k]
		if !ok {
			m[k] = span{n, n}
			return
		}
		if n < s.lo {
			s.lo = n
		}
		if n > s.hi {
			s.hi = n
		}
		m[k] = s
	}
	for _, r := range rows {
		if r.Code() && secOK(r, a, sameSec) {
			k := hk{r.Section, r.File, r.Hunk}
			widen(newSp, k, r.NewNo)
			widen(oldSp, k, r.OldNo)
		}
	}
	var target hk
	found := false
	for _, r := range rows {
		if !r.Code() || !secOK(r, a, sameSec) {
			continue
		}
		k := hk{r.Section, r.File, r.Hunk}
		if s, ok := newSp[k]; ok && a.NewNo > 0 && a.NewNo >= s.lo && a.NewNo <= s.hi {
			target, found = k, true
			break
		}
		if s, ok := oldSp[k]; ok && a.OldNo > 0 && a.OldNo >= s.lo && a.OldNo <= s.hi {
			target, found = k, true
			break
		}
	}
	if !found {
		return -1
	}
	best, bestD := -1, 0
	for i, r := range rows {
		if !r.Code() || !secOK(r, a, sameSec) || (hk{r.Section, r.File, r.Hunk}) != target {
			continue
		}
		var d int
		switch {
		case a.NewNo > 0:
			if r.NewNo == 0 {
				continue
			}
			d = abs(a.NewNo - r.NewNo)
		case a.OldNo > 0:
			if r.OldNo == 0 {
				continue
			}
			d = abs(a.OldNo - r.OldNo)
		default:
			continue
		}
		if best < 0 || d < bestD {
			best, bestD = i, d
		}
	}
	return best
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
