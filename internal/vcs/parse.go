package vcs

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var hunkRe = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@ ?(.*)$`)

// ParseUnified parses `git diff --no-color` output into a Diff.
func ParseUnified(out string) (Diff, error) {
	var (
		d    Diff
		cur  *File
		hunk *Hunk
		oldN int
		newN int
		oldR int // lines the current hunk still owes, per its header
		newR int
	)
	flush := func() {
		if hunk != nil && cur != nil {
			cur.Hunks = append(cur.Hunks, *hunk)
		}
		hunk = nil
	}
	finish := func() {
		flush()
		if cur != nil {
			cur.count()
			d.Files = append(d.Files, *cur)
		}
		cur = nil
	}
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if hunk != nil && (oldR > 0 || newR > 0) {
			switch {
			case strings.HasPrefix(line, "+"):
				hunk.Lines = append(hunk.Lines, Line{Kind: LineAdd, Text: line[1:], NewNo: newN})
				newN++
				newR--
				continue
			case strings.HasPrefix(line, "-"):
				hunk.Lines = append(hunk.Lines, Line{Kind: LineDel, Text: line[1:], OldNo: oldN})
				oldN++
				oldR--
				continue
			case strings.HasPrefix(line, " "), line == "":
				text := ""
				if line != "" {
					text = line[1:]
				}
				hunk.Lines = append(hunk.Lines, Line{Kind: LineContext, Text: text, OldNo: oldN, NewNo: newN})
				oldN++
				newN++
				oldR--
				newR--
				continue
			}
		}
		switch {
		case strings.HasPrefix(line, "diff --git "):
			finish()
			cur = &File{Status: StatusModified}
			cur.OldPath, cur.NewPath = splitGitHeader(strings.TrimPrefix(line, "diff --git "))
		case cur == nil:
			// Preamble before the first file; nothing to record.
		case strings.HasPrefix(line, `\`):
			// "\ No newline at end of file" annotates the previous line.
		case strings.HasPrefix(line, "new file mode"):
			cur.Status = StatusAdded
			cur.OldPath = ""
		case strings.HasPrefix(line, "deleted file mode"):
			cur.Status = StatusDeleted
			cur.NewPath = ""
		case strings.HasPrefix(line, "rename from "):
			cur.Status = StatusRenamed
			cur.OldPath = strings.TrimPrefix(line, "rename from ")
		case strings.HasPrefix(line, "rename to "):
			cur.Status = StatusRenamed
			cur.NewPath = strings.TrimPrefix(line, "rename to ")
		case strings.HasPrefix(line, "Binary files "), line == "GIT binary patch":
			cur.Binary = true
		case strings.HasPrefix(line, "--- "):
			if p := stripPrefixPath(strings.TrimPrefix(line, "--- ")); p != "" {
				cur.OldPath = p
			}
		case strings.HasPrefix(line, "+++ "):
			if p := stripPrefixPath(strings.TrimPrefix(line, "+++ ")); p != "" {
				cur.NewPath = p
			}
		case strings.HasPrefix(line, "@@"):
			flush()
			h, err := parseHunkHeader(line)
			if err != nil {
				return Diff{}, err
			}
			hunk = &h
			oldN, newN, oldR, newR = h.OldStart, h.NewStart, h.OldLines, h.NewLines
		}
	}
	finish()
	return d, nil
}

func parseHunkHeader(line string) (Hunk, error) {
	m := hunkRe.FindStringSubmatch(line)
	if m == nil {
		return Hunk{}, fmt.Errorf("parsing hunk header %q", line)
	}
	num := func(s string, def int) int {
		if s == "" {
			return def
		}
		n, _ := strconv.Atoi(s)
		return n
	}
	return Hunk{
		OldStart: num(m[1], 0), OldLines: num(m[2], 1),
		NewStart: num(m[3], 0), NewLines: num(m[4], 1),
		Header: m[5],
	}, nil
}

// splitGitHeader splits "a/x b/y". Paths with spaces are ambiguous, so when
// the halves are equal in length it splits in the middle; a later ---/+++
// or rename line overrides this anyway.
func splitGitHeader(s string) (string, string) {
	if i := (len(s) - 1) / 2; len(s)%2 == 1 && s[i] == ' ' {
		return strings.TrimPrefix(s[:i], "a/"), strings.TrimPrefix(s[i+1:], "b/")
	}
	a, b, _ := strings.Cut(s, " ")
	return strings.TrimPrefix(a, "a/"), strings.TrimPrefix(b, "b/")
}

// stripPrefixPath turns "a/x" into "x" and "/dev/null" into "".
func stripPrefixPath(p string) string {
	p, _, _ = strings.Cut(p, "\t")
	if p == "/dev/null" {
		return ""
	}
	if strings.HasPrefix(p, "a/") || strings.HasPrefix(p, "b/") {
		return p[2:]
	}
	return p
}
