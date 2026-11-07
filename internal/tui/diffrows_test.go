package tui

import (
	"fmt"
	"testing"

	"github.com/jwstover/tend/internal/vcs"
)

func codeHunk(oldStart, newStart int, lines ...vcs.Line) vcs.Hunk {
	return vcs.Hunk{OldStart: oldStart, OldLines: len(lines), NewStart: newStart, NewLines: len(lines), Lines: lines}
}

func modFile(path string) vcs.File {
	return vcs.File{
		OldPath: path, NewPath: path, Status: vcs.StatusModified, Added: 1, Removed: 1,
		Hunks: []vcs.Hunk{codeHunk(10, 10,
			vcs.Line{Kind: vcs.LineContext, Text: "a", OldNo: 10, NewNo: 10},
			vcs.Line{Kind: vcs.LineDel, Text: "b", OldNo: 11},
			vcs.Line{Kind: vcs.LineAdd, Text: "B", NewNo: 11},
			vcs.Line{Kind: vcs.LineContext, Text: "c", OldNo: 12, NewNo: 12},
		)},
	}
}

func kinds(rows []Row) []RowKind {
	out := make([]RowKind, len(rows))
	for i, r := range rows {
		out[i] = r.Kind
	}
	return out
}

func TestBuildRows(t *testing.T) {
	mod := vcs.Diff{Files: []vcs.File{modFile("a.go")}}
	body := []RowKind{RowFileHeader, RowHunkHeader, RowContext, RowDel, RowAdd, RowContext}
	tests := []struct {
		name        string
		committed   vcs.Diff
		uncommitted vcs.Diff
		folded      map[string]bool
		want        []RowKind
	}{
		{"modified", mod, vcs.Diff{}, nil, body},
		{"folded", mod, vcs.Diff{}, map[string]bool{"a.go": true}, []RowKind{RowFileHeader}},
		{"binary", vcs.Diff{Files: []vcs.File{{NewPath: "x.png", Status: vcs.StatusAdded, Binary: true}}}, vcs.Diff{}, nil,
			[]RowKind{RowFileHeader, RowNotice}},
		{"empty", vcs.Diff{}, vcs.Diff{}, nil, []RowKind{RowNotice}},
		{"both sections", mod, mod, nil,
			append(append([]RowKind{RowSection}, body...), append([]RowKind{RowSection}, body...)...)},
		{"fold applies to both sections", mod, mod, map[string]bool{"a.go": true},
			[]RowKind{RowSection, RowFileHeader, RowSection, RowFileHeader}},
		{"only uncommitted has no section row", vcs.Diff{}, mod, nil, body},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := kinds(BuildRows(tc.committed, tc.uncommitted, tc.folded))
			if fmt.Sprint(got) != fmt.Sprint(tc.want) {
				t.Fatalf("kinds = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestBuildRowsRenameAndDelete(t *testing.T) {
	d := vcs.Diff{Files: []vcs.File{
		{OldPath: "old.go", NewPath: "new.go", Status: vcs.StatusRenamed},
		{OldPath: "gone.go", Status: vcs.StatusDeleted, Hunks: []vcs.Hunk{codeHunk(1, 0, vcs.Line{Kind: vcs.LineDel, Text: "x", OldNo: 1})}},
	}}
	rows := BuildRows(d, vcs.Diff{}, map[string]bool{"gone.go": true})
	if rows[0].Path != "new.go" || rows[0].OldPath != "old.go" {
		t.Fatalf("rename header = %+v", rows[0])
	}
	if rows[1].Kind != RowNotice {
		t.Fatalf("hunkless rename should carry a notice, got %v", rows[1].Kind)
	}
	if rows[2].Path != "gone.go" || !rows[2].Folded || len(rows) != 3 {
		t.Fatalf("deleted file keyed by old path and folded: %+v", rows[2:])
	}
}

func TestFindRow(t *testing.T) {
	a := modFile("a.go")
	b := vcs.File{OldPath: "b.go", NewPath: "b.go", Status: vcs.StatusModified, Hunks: []vcs.Hunk{
		codeHunk(1, 1, vcs.Line{Kind: vcs.LineContext, Text: "x", OldNo: 1, NewNo: 1}),
		codeHunk(50, 50, vcs.Line{Kind: vcs.LineContext, Text: "y", OldNo: 50, NewNo: 50},
			vcs.Line{Kind: vcs.LineContext, Text: "z", OldNo: 51, NewNo: 51}),
	}}
	rows := BuildRows(vcs.Diff{Files: []vcs.File{a, b}}, vcs.Diff{}, nil)
	idx := func(path string, kind RowKind, newNo int) int {
		for i, r := range rows {
			if r.Path == path && r.Kind == kind && r.NewNo == newNo {
				return i
			}
		}
		t.Fatalf("no row %s %v %d", path, kind, newNo)
		return -1
	}
	oldIdx := func(path string, oldNo int) int {
		for i, r := range rows {
			if r.Path == path && r.Code() && r.OldNo == oldNo {
				return i
			}
		}
		t.Fatalf("no row %s old %d", path, oldNo)
		return -1
	}
	hdr := func(path string) int { return idx(path, RowFileHeader, 0) }

	tests := []struct {
		name string
		a    Anchor
		want int
	}{
		{"exact add", Anchor{Path: "a.go", NewNo: 11, Kind: RowAdd}, idx("a.go", RowAdd, 11)},
		{"exact del", Anchor{Path: "a.go", OldNo: 11, Kind: RowDel}, idx("a.go", RowDel, 0)},
		{"nearest in hunk", Anchor{Path: "a.go", OldNo: 11, NewNo: 13, Kind: RowContext}, idx("a.go", RowContext, 12)},
		{"context line found by its old number after an edit above", Anchor{Path: "a.go", OldNo: 12, NewNo: 14, Kind: RowContext}, oldIdx("a.go", 12)},
		{"nearest in the covering hunk only", Anchor{Path: "b.go", NewNo: 51, Kind: RowAdd}, idx("b.go", RowContext, 51)},
		{"no covering hunk goes to header", Anchor{Path: "b.go", OldNo: 20, NewNo: 20, Kind: RowAdd}, hdr("b.go")},
		{"file gone goes to 0", Anchor{Path: "zzz.go", NewNo: 3, Kind: RowAdd}, 0},
		{"empty anchor", Anchor{}, 0},
		{"file header anchor", Anchor{Path: "b.go", Kind: RowFileHeader}, hdr("b.go")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := FindRow(rows, tc.a); got != tc.want {
				t.Fatalf("FindRow = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestFindRowRoundTripsAnchorAt(t *testing.T) {
	rows := BuildRows(vcs.Diff{Files: []vcs.File{modFile("a.go")}}, vcs.Diff{Files: []vcs.File{modFile("a.go")}}, nil)
	for i, r := range rows {
		if r.Kind == RowSection || r.Kind == RowHunkHeader { // hunk headers anchor to their file
			continue
		}
		if got := FindRow(rows, AnchorAt(rows, i)); got != i {
			t.Fatalf("row %d (%v) found at %d", i, r.Kind, got)
		}
	}
}

func bigDiff(files, linesPerFile int) vcs.Diff {
	var d vcs.Diff
	for f := 0; f < files; f++ {
		file := vcs.File{OldPath: fmt.Sprintf("f%d.go", f), NewPath: fmt.Sprintf("f%d.go", f), Status: vcs.StatusModified}
		var lines []vcs.Line
		for i := 1; i <= linesPerFile; i++ {
			switch i % 3 {
			case 0:
				lines = append(lines, vcs.Line{Kind: vcs.LineAdd, Text: "added := compute(i)", NewNo: i})
			case 1:
				lines = append(lines, vcs.Line{Kind: vcs.LineDel, Text: "removed := compute(i)", OldNo: i})
			default:
				lines = append(lines, vcs.Line{Kind: vcs.LineContext, Text: "unchanged()", OldNo: i, NewNo: i})
			}
		}
		file.Hunks = []vcs.Hunk{codeHunk(1, 1, lines...)}
		d.Files = append(d.Files, file)
	}
	return d
}

func BenchmarkBuildRows(b *testing.B) {
	d := bigDiff(10, 500) // ~5,000 lines
	b.ReportAllocs()
	for b.Loop() {
		BuildRows(d, vcs.Diff{}, nil)
	}
}

func BenchmarkRenderRows(b *testing.B) {
	rows := BuildRows(bigDiff(10, 500), vcs.Diff{}, nil)
	opts := DiffRenderOpts{Styles: DefaultStyles(), Highlight: true}
	b.ReportAllocs()
	for b.Loop() {
		RenderRows(rows, 2000, 50, 120, 2010, opts)
	}
}
