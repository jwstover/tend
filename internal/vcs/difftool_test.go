package vcs

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func fakeLook(have ...string) func(string) (string, error) {
	return func(b string) (string, error) {
		if slices.Contains(have, b) {
			return "/bin/" + b, nil
		}
		return "", errors.New("not found")
	}
}

func TestDiffToolPresetNames(t *testing.T) {
	want := []string{"delta", "difftastic", "tig", "diffnav", "diffview", "lazygit", "but", "less"}
	if got := DiffToolPresetNames(); !slices.Equal(got, want) {
		t.Fatalf("names = %v, want %v", got, want)
	}
}

func TestDiffToolCmdUnsetPicksFirstOnPath(t *testing.T) {
	r, err := DiffToolCmd("", "/repo", "origin/main", "feat", fakeLook("tig", "delta", "less"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Used != "delta" || r.FellBack {
		t.Fatalf("used=%q fellBack=%v", r.Used, r.FellBack)
	}
	if r.Cmd.Dir != "/repo" || r.Cmd.Args[0] != "sh" || r.Cmd.Args[1] != "-c" {
		t.Fatalf("cmd = %v dir=%q", r.Cmd.Args, r.Cmd.Dir)
	}
	want := `git -c core.pager='delta --side-by-side' diff 'origin/main'...'feat'`
	if r.Cmd.Args[2] != want {
		t.Fatalf("script = %q, want %q", r.Cmd.Args[2], want)
	}
}

func TestDiffToolCmdFallbackOrder(t *testing.T) {
	for _, tc := range []struct {
		have []string
		want string
	}{
		{[]string{"less", "tig", "nvim"}, "tig"},
		{[]string{"less", "nvim", "but"}, "diffview"},
		{[]string{"less"}, "less"},
	} {
		r, err := DiffToolCmd("", "/r", "b", "h", fakeLook(tc.have...))
		if err != nil || r.Used != tc.want {
			t.Errorf("have %v: used=%q err=%v, want %q", tc.have, r.Used, err, tc.want)
		}
	}
	if _, err := DiffToolCmd("", "/r", "b", "h", fakeLook()); !errors.Is(err, ErrNoDiffTool) {
		t.Fatalf("err = %v, want ErrNoDiffTool", err)
	}
}

func TestDiffToolCmdSetPreset(t *testing.T) {
	r, err := DiffToolCmd(" TIG ", "/r", "main", "feat", fakeLook("tig", "delta"))
	if err != nil || r.Used != "tig" || r.FellBack {
		t.Fatalf("r=%+v err=%v", r, err)
	}
	if got := r.Cmd.Args[2]; got != `tig 'main'..'feat'` {
		t.Fatalf("script = %q", got)
	}
}

func TestDiffToolCmdMissingPresetFallsBack(t *testing.T) {
	r, err := DiffToolCmd("lazygit", "/r", "main", "feat", fakeLook("tig", "less"))
	if err != nil {
		t.Fatal(err)
	}
	if !r.FellBack || r.Missing != "lazygit" || r.Used != "tig" {
		t.Fatalf("r = %+v", r)
	}
}

func TestDiffToolCmdCustomTemplateQuotes(t *testing.T) {
	r, err := DiffToolCmd(`mytool {repo} {base} {head}`, "/my repo", "origin/main", "it's a branch", fakeLook())
	if err != nil {
		t.Fatal(err)
	}
	if r.Used != "custom" || r.FellBack {
		t.Fatalf("r = %+v", r)
	}
	want := `mytool '/my repo' 'origin/main' 'it'\''s a branch'`
	if got := r.Cmd.Args[2]; got != want {
		t.Fatalf("script = %q, want %q", got, want)
	}
	if strings.Contains(r.Cmd.Args[2], "{") {
		t.Fatal("placeholder left behind")
	}
}
