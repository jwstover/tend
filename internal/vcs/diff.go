package vcs

// Status is how a file changed.
type Status string

// File statuses.
const (
	StatusAdded    Status = "added"
	StatusModified Status = "modified"
	StatusDeleted  Status = "deleted"
	StatusRenamed  Status = "renamed"
)

// LineKind is the role of one diff line.
type LineKind string

// Line kinds.
const (
	LineContext LineKind = "context"
	LineAdd     LineKind = "add"
	LineDel     LineKind = "del"
)

// Diff is a set of changed files.
type Diff struct {
	Files []File
}

// File is one changed file. OldPath is empty for an added file and NewPath
// for a deleted one.
type File struct {
	OldPath, NewPath string
	Status           Status
	Binary           bool
	Hunks            []Hunk
	Added, Removed   int
}

// Path is the name to show for the file: the new path, else the old.
func (f File) Path() string {
	if f.NewPath != "" {
		return f.NewPath
	}
	return f.OldPath
}

// Hunk is one @@ section of a file's diff.
type Hunk struct {
	OldStart, OldLines int
	NewStart, NewLines int
	Header             string // the text after the closing @@, usually a function name
	Lines              []Line
}

// Line is one line of a hunk. OldNo is 0 for an added line and NewNo is 0
// for a removed one.
type Line struct {
	Kind         LineKind
	Text         string
	OldNo, NewNo int
}

// Empty reports whether the diff changes nothing.
func (d Diff) Empty() bool { return len(d.Files) == 0 }

// count fills Added/Removed from the hunks.
func (f *File) count() {
	f.Added, f.Removed = 0, 0
	for _, h := range f.Hunks {
		for _, l := range h.Lines {
			switch l.Kind {
			case LineAdd:
				f.Added++
			case LineDel:
				f.Removed++
			}
		}
	}
}
