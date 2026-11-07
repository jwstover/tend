package tui

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jwstover/tend/internal/store"
	"github.com/jwstover/tend/internal/task"
)

type baseVCS struct{ fakeVCS }

func (baseVCS) Base(context.Context, string, string) (string, error) { return "origin/main", nil }

func diffToolApp(t *testing.T, have ...string) (app, *store.Store, task.Task) {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(ctx, filepath.Join(t.TempDir(), "tend.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	a := newApp(ctx, s, "")
	a.vcs = &baseVCS{}
	tk, err := s.AddTask(ctx, "t")
	if err != nil {
		t.Fatal(err)
	}
	old := diffToolLookPath
	t.Cleanup(func() { diffToolLookPath = old })
	diffToolLookPath = func(b string) (string, error) {
		for _, h := range have {
			if h == b {
				return b, nil
			}
		}
		return "", exec.ErrNotFound
	}
	return a, s, tk
}

func TestDiffToolNoLink(t *testing.T) {
	a, _, tk := diffToolApp(t, "delta")
	st, ok := a.openDiffToolCmd(tk)().(statusMsg)
	if !ok || !strings.Contains(st.text, "no branch linked") {
		t.Fatalf("got %+v", st)
	}
}

func TestDiffToolUnsetUsesDelta(t *testing.T) {
	a, s, tk := diffToolApp(t, "delta", "less")
	if _, err := s.LinkBranch(context.Background(), tk.ID, "/repo", "feat"); err != nil {
		t.Fatal(err)
	}
	msg, ok := a.openDiffToolCmd(tk)().(diffToolReadyMsg)
	if !ok {
		t.Fatal("no ready msg")
	}
	want := `git -c core.pager='delta --side-by-side' diff 'origin/main'...'feat'`
	if msg.cmd.Args[2] != want || msg.cmd.Dir != "/repo" || msg.note.text != "" {
		t.Fatalf("cmd=%v dir=%q note=%q", msg.cmd.Args, msg.cmd.Dir, msg.note.text)
	}
}

func TestDiffToolMissingPresetFlashesAndSeveralLinks(t *testing.T) {
	a, s, tk := diffToolApp(t, "less")
	ctx := context.Background()
	for _, b := range []string{"one", "two"} {
		if _, err := s.LinkBranch(ctx, tk.ID, "/repo", b); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetSetting(ctx, "diff_tool", "delta"); err != nil {
		t.Fatal(err)
	}
	msg := a.openDiffToolCmd(tk)().(diffToolReadyMsg)
	if !strings.Contains(msg.note.text, "delta not found; using less") {
		t.Fatalf("note = %q", msg.note.text)
	}
	if !strings.Contains(msg.cmd.Args[2], "'two'") {
		t.Fatalf("expected newest link: %v", msg.cmd.Args)
	}
}
