package task

import (
	"errors"
	"strings"
	"time"
)

// DefaultProjectID is the seeded "Unsorted" project (migration 00007). It
// is the fallback capture target when no active project is set, and the
// row every deleted project's tasks are reassigned to, so it must always
// exist — Store.DeleteProject refuses to remove it.
const DefaultProjectID int64 = 1

// ErrEmptyProjectName is returned when a project name is blank.
var ErrEmptyProjectName = errors.New("project name is empty")

// ErrProtectedProject is returned when a caller tries to delete the
// seeded default project.
var ErrProtectedProject = errors.New("the default project cannot be deleted")

// ErrProjectNotFound is returned when a project id or name doesn't
// resolve. Callers that take a name from the user match on this to report
// a typo rather than creating a project behind their back.
var ErrProjectNotFound = errors.New("project not found")

// Project groups tasks. Every task belongs to exactly one; the TUI's
// leftmost column lists them and scopes the task list to the selected
// one. Deliberately flat — a project has no parent (see
// docs/projects-plan.md §7).
type Project struct {
	ID         int64
	Name       string
	SortOrder  int64
	ArchivedAt *time.Time
	CreatedAt  time.Time
	UpdatedAt  time.Time

	// LiveCount is live top-level tasks in this project: the population
	// the list view renders as rows, so the number shown beside a project
	// matches what selecting it produces. Zero unless the project came
	// from ListProjects.
	LiveCount int64
}

// Archived reports whether the project is hidden from the projects column.
func (p Project) Archived() bool { return p.ArchivedAt != nil }

// NormalizeProjectName trims surrounding whitespace and rejects blank
// names. Case is preserved as typed; the schema's NOCASE collation is what
// makes "Work" and "work" the same project.
func NormalizeProjectName(s string) (string, error) {
	n := strings.TrimSpace(s)
	if n == "" {
		return "", ErrEmptyProjectName
	}
	return n, nil
}

// NormalizeTag canonicalizes one tag: surrounding whitespace and a leading
// "#" are dropped (the "#" is display sugar in the list row, not part of
// the stored name). Reports ok=false for anything that normalizes to
// nothing.
func NormalizeTag(s string) (string, bool) {
	t := strings.TrimSpace(s)
	t = strings.TrimPrefix(t, "#")
	t = strings.TrimSpace(t)
	if t == "" {
		return "", false
	}
	return t, true
}

// ParseTags splits a free-text tag prompt into normalized tag names.
// Commas and whitespace both separate, so "work, home" and "work home"
// are the same input. Duplicates are dropped case-insensitively, matching
// the schema's NOCASE uniqueness, and the first spelling of a tag wins.
// Returns an empty slice (never nil) so a cleared prompt is an explicit
// "no tags" rather than an absent value.
func ParseTags(s string) []string {
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n'
	})
	out := make([]string, 0, len(fields))
	seen := make(map[string]bool, len(fields))
	for _, f := range fields {
		t, ok := NormalizeTag(f)
		if !ok {
			continue
		}
		key := strings.ToLower(t)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, t)
	}
	return out
}

// FormatTags renders tags back into the space-separated form ParseTags
// accepts, for seeding the tag prompt with a task's current tags.
func FormatTags(tags []string) string { return strings.Join(tags, " ") }
