package vcs

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseUnified(t *testing.T) {
	data, err := os.ReadFile("testdata/git.diff")
	if err != nil {
		t.Fatal(err)
	}
	d, err := ParseUnified(string(data))
	if err != nil {
		t.Fatal(err)
	}
	type want struct {
		old, new string
		status   Status
		binary   bool
		hunks    int
		add, del int
	}
	wants := []want{
		{"a.txt", "a.txt", StatusModified, false, 1, 1, 1},
		{"", "new.txt", StatusAdded, false, 1, 2, 0},
		{"gone.txt", "", StatusDeleted, false, 1, 0, 1},
		{"old name.txt", "new name.txt", StatusRenamed, false, 1, 1, 1},
		{"img.png", "img.png", StatusModified, true, 0, 0, 0},
		{"run.sh", "run.sh", StatusModified, false, 0, 0, 0},
	}
	if len(d.Files) != len(wants) {
		t.Fatalf("got %d files, want %d: %+v", len(d.Files), len(wants), d.Files)
	}
	for i, w := range wants {
		f := d.Files[i]
		got := want{f.OldPath, f.NewPath, f.Status, f.Binary, len(f.Hunks), f.Added, f.Removed}
		if got != w {
			t.Errorf("file %d: got %+v, want %+v", i, got, w)
		}
	}
	h := d.Files[0].Hunks[0]
	if h.Header != "func main" {
		t.Errorf("header = %q", h.Header)
	}
	wantLines := []Line{
		{LineContext, "one", 1, 1},
		{LineDel, "two", 2, 0},
		{LineAdd, "TWO", 0, 2},
		{LineContext, "three", 3, 3},
	}
	if !reflect.DeepEqual(h.Lines, wantLines) {
		t.Errorf("lines = %+v", h.Lines)
	}
}

func TestParseButDiff(t *testing.T) {
	data, err := os.ReadFile("testdata/but_diff.json")
	if err != nil {
		t.Fatal(err)
	}
	d, err := ParseButDiff(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Files) != 1 || d.Files[0].Path() != "AGENTS.md" || len(d.Files[0].Hunks) != 1 {
		t.Fatalf("unexpected diff: %+v", d)
	}
	f := d.Files[0]
	if f.Added != 1 || f.Removed != 1 || f.Hunks[0].OldStart != 464 {
		t.Errorf("counts/hunk wrong: %+v", f)
	}
	if _, err := ParseButDiff([]byte("nope")); err == nil {
		t.Error("expected decode error")
	}
}

type call struct{ dir, name, args string }

func fake(calls *[]call, reply func(name, args string) ([]byte, error)) *Client {
	return &Client{Run: func(_ context.Context, dir, name string, args ...string) ([]byte, error) {
		a := strings.Join(args, " ")
		*calls = append(*calls, call{dir, name, a})
		return reply(name, a)
	}}
}

const butStatusJSON = `{"mergeBase":{"commitId":"abc123"},"stacks":[{"cliId":"g0","assignedChanges":[{"cliId":"q1"}],"branches":[{"name":"feat/x","commits":[{},{}]}]}]}`

func TestUncommittedDiffArgv(t *testing.T) {
	var calls []call
	c := fake(&calls, func(name, args string) ([]byte, error) {
		switch args {
		case "symbolic-ref --short HEAD":
			return []byte("gitbutler/workspace\n"), nil
		case "status --json":
			return []byte(butStatusJSON), nil
		case "diff q1 --json":
			return os.ReadFile("testdata/but_diff.json")
		}
		return nil, errors.New("unexpected " + name + " " + args)
	})
	d, err := c.UncommittedDiff(context.Background(), "/repo", "feat/x")
	if err != nil || len(d.Files) != 1 {
		t.Fatalf("d=%+v err=%v", d, err)
	}
	for _, cl := range calls {
		if cl.dir != "/repo" {
			t.Errorf("command ran in %q", cl.dir)
		}
		if cl.name == "but" && !strings.HasPrefix(cl.args, "status") && !strings.HasPrefix(cl.args, "diff") {
			t.Errorf("non-read-only but command: %s", cl.args)
		}
	}
	if _, err := c.UncommittedDiff(context.Background(), "/repo", "missing"); !errors.Is(err, ErrBranchNotFound) {
		t.Errorf("err = %v, want ErrBranchNotFound", err)
	}
	bs, err := c.Branches(context.Background(), "/repo")
	if err != nil || len(bs) != 1 || bs[0] != (Branch{"feat/x", 2, true}) {
		t.Errorf("branches = %+v, %v", bs, err)
	}
	base, _ := c.Base(context.Background(), "/repo", "feat/x")
	if base != "abc123" {
		t.Errorf("base = %q", base)
	}
}

func TestBaseFallbacks(t *testing.T) {
	cases := []struct {
		name     string
		branch   string
		upstream string          // reply to the @{upstream} lookup; "" = no upstream
		originH  string          // reply to symbolic-ref origin/HEAD; "" = unset
		refs     map[string]bool // refs that resolve
		want     string
	}{
		{"dangling origin/HEAD at the workspace falls through", "feat/x", "", "origin/gitbutler/workspace",
			map[string]bool{"origin/master": true}, "origin/master"},
		{"origin/HEAD that does not resolve falls through", "feat/x", "", "origin/gone",
			map[string]bool{"main": true}, "main"},
		{"good origin/HEAD is used", "feat/x", "", "origin/develop",
			map[string]bool{"origin/develop": true, "origin/main": true}, "origin/develop"},
		{"integration upstream is the base", "task-737/sub-809", "origin/gui/integration", "origin/master",
			map[string]bool{"origin/gui/integration": true, "origin/master": true}, "origin/gui/integration"},
		{"same-named upstream is a push target, skipped", "task-790/x", "origin/task-790/x", "origin/main",
			map[string]bool{"origin/task-790/x": true, "origin/main": true}, "origin/main"},
		{"unfetched upstream is skipped", "feat/x", "origin/gone", "",
			map[string]bool{"origin/main": true}, "origin/main"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls []call
			c := fake(&calls, func(_, args string) ([]byte, error) {
				switch {
				case args == "symbolic-ref --short HEAD":
					return nil, errors.New("fatal: ref HEAD is not a symbolic ref")
				case strings.HasSuffix(args, "@{upstream}"):
					if tc.upstream == "" {
						return nil, errors.New("no upstream")
					}
					return []byte(tc.upstream + "\n"), nil
				case args == "symbolic-ref --short refs/remotes/origin/HEAD":
					if tc.originH == "" {
						return nil, errors.New("not a symbolic ref")
					}
					return []byte(tc.originH + "\n"), nil
				case strings.HasPrefix(args, "rev-parse --verify --quiet "):
					ref := strings.TrimSuffix(strings.TrimPrefix(args, "rev-parse --verify --quiet "), "^{commit}")
					if tc.refs[ref] {
						return []byte("sha\n"), nil
					}
					return nil, errors.New("exit 1")
				}
				return nil, errors.New("unexpected " + args)
			})
			got, err := c.Base(context.Background(), "/repo", tc.branch)
			if err != nil || got != tc.want {
				t.Errorf("Base = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestMissingBinaries(t *testing.T) {
	c := &Client{Run: func(_ context.Context, _, name string, _ ...string) ([]byte, error) {
		return nil, notFound(name)
	}}
	if c.IsGitButler(context.Background(), "/r") {
		t.Error("IsGitButler with no git")
	}
	if _, err := c.Branches(context.Background(), "/r"); !errors.Is(err, ErrNoGit) {
		t.Errorf("err = %v", err)
	}
	if !errors.Is(notFound("but"), ErrNoBut) {
		t.Error("but not mapped")
	}
}

func TestGitRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run("init", "-q", "-b", "main")
	write("a.txt", "one\ntwo\n")
	run("add", ".")
	run("commit", "-q", "-m", "init")
	run("checkout", "-q", "-b", "feat")
	write("a.txt", "one\nTWO\n")
	write("b.txt", "new\n")
	run("add", ".")
	run("commit", "-q", "-m", "change")

	c := New()
	ctx := context.Background()
	if !c.BranchExists(ctx, dir, "feat") || c.BranchExists(ctx, dir, "nope") {
		t.Error("BranchExists wrong")
	}
	d, err := c.CommittedDiff(ctx, dir, "feat")
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Files) != 2 || d.Files[0].Path() != "a.txt" || d.Files[1].Status != StatusAdded {
		t.Errorf("diff = %+v", d)
	}
	if _, err := c.CommittedDiff(ctx, dir, "nope"); !errors.Is(err, ErrBranchNotFound) {
		t.Errorf("err = %v", err)
	}
	if ud, err := c.UncommittedDiff(ctx, dir, "feat"); err != nil || !ud.Empty() {
		t.Errorf("uncommitted outside GitButler: %+v %v", ud, err)
	}
	if _, err := c.Branches(ctx, t.TempDir()); !errors.Is(err, ErrNotARepo) {
		t.Errorf("err = %v, want ErrNotARepo", err)
	}
}
