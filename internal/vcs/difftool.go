package vcs

import (
	"errors"
	"os/exec"
	"strings"
)

// ErrNoDiffTool is returned when no preset's binary is on $PATH, not even the
// `less` fallback.
var ErrNoDiffTool = errors.New("no diff tool found on $PATH")

// DiffToolPreset is one named way of showing a branch diff in an external
// tool. External tools only see refs, so they show committed work only.
type DiffToolPreset struct {
	Name     string // what the diff_tool setting stores
	Binary   string // looked up on $PATH to decide whether the preset is usable
	Template string // run through `sh -c`; {base}, {head} and {repo} are substituted
}

// diffToolPresets is the single preset table, in the order an unset
// diff_tool tries them. `less` is last: the always-there fallback.
var diffToolPresets = []DiffToolPreset{
	{"delta", "delta", `git -c core.pager='delta --side-by-side' diff {base}...{head}`},
	{"difftastic", "difft", `git -c diff.external=difft diff {base}...{head}`},
	{"tig", "tig", `tig {base}..{head}`},
	{"diffnav", "diffnav", `git diff {base}...{head} | diffnav`},
	{"diffview", "nvim", `nvim -c 'DiffviewOpen {base}...{head}'`},
	{"lazygit", "lazygit", `lazygit -p {repo}`},
	{"but", "but", `but tui`},
	{"less", "less", `git diff --color {base}...{head} | less -R`},
}

// DiffToolPresets returns a copy of the preset table.
func DiffToolPresets() []DiffToolPreset {
	return append([]DiffToolPreset(nil), diffToolPresets...)
}

// DiffToolPresetNames lists the preset names in table order; the diff_tool
// setting offers exactly these.
func DiffToolPresetNames() []string {
	names := make([]string, len(diffToolPresets))
	for i, p := range diffToolPresets {
		names[i] = p.Name
	}
	return names
}

func presetByName(name string) (DiffToolPreset, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, p := range diffToolPresets {
		if p.Name == name {
			return p, true
		}
	}
	return DiffToolPreset{}, false
}

// DiffToolResult is a built command and how it was chosen.
type DiffToolResult struct {
	Cmd *exec.Cmd // never run by this package
	// Used names the preset that was picked, or "custom" for a template.
	Used string
	// FellBack is set when a configured preset's binary was missing and
	// another preset stood in. Missing names the one that was wanted.
	FellBack bool
	Missing  string
}

// shellQuote single-quotes s for sh.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// substitute replaces the placeholders with shell-quoted values. A template
// that quotes a placeholder itself would double-quote it, so templates use
// the bare placeholder.
func substitute(tmpl, repo, base, head string) string {
	return strings.NewReplacer(
		"{base}", shellQuote(base),
		"{head}", shellQuote(head),
		"{repo}", shellQuote(repo),
	).Replace(tmpl)
}

// DiffToolCmd builds the command that shows head against base in the tool
// the diff_tool setting names. value is the setting as stored: empty means
// auto-detect, a preset name selects that preset, anything else is a shell
// template. A preset whose binary is missing (lookPath fails) falls back to
// the first preset that resolves, as auto-detect does, with FellBack set.
// The command runs through `sh -c` in repo; it is returned, never run.
func DiffToolCmd(value, repo, base, head string, lookPath func(string) (string, error)) (DiffToolResult, error) {
	value = strings.TrimSpace(value)
	build := func(tmpl string) *exec.Cmd {
		c := exec.Command("sh", "-c", substitute(tmpl, repo, base, head))
		c.Dir = repo
		return c
	}
	var res DiffToolResult
	if value != "" {
		p, isPreset := presetByName(value)
		if !isPreset {
			res.Cmd, res.Used = build(value), "custom"
			return res, nil
		}
		if _, err := lookPath(p.Binary); err == nil {
			res.Cmd, res.Used = build(p.Template), p.Name
			return res, nil
		}
		res.FellBack, res.Missing = true, p.Binary
	}
	for _, p := range diffToolPresets {
		if _, err := lookPath(p.Binary); err == nil {
			res.Cmd, res.Used = build(p.Template), p.Name
			return res, nil
		}
	}
	return DiffToolResult{}, ErrNoDiffTool
}
