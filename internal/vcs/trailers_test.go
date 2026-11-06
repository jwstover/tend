package vcs

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseTaskCommits(t *testing.T) {
	data, err := os.ReadFile("testdata/trailers_log.txt")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		id   int64
		want []string
	}{
		{790, []string{"aaa111", "ddd444", "eee555"}},
		{7900, []string{"ccc333"}},
		{12, []string{"ddd444", "eee555"}},
		{1, nil},
	}
	for _, tt := range tests {
		var got []string
		for _, c := range parseTaskCommits(string(data), tt.id) {
			got = append(got, c.SHA)
		}
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("id %d: got %v, want %v", tt.id, got, tt.want)
		}
	}
	cs := parseTaskCommits(string(data), 790)
	if cs[0].Subject != "feat: one" || cs[0].When.Unix() != 1700000000 {
		t.Errorf("fields = %+v", cs[0])
	}
}

func TestTaskCommitsArgvReadOnly(t *testing.T) {
	var calls [][]string
	c := &Client{Run: func(_ context.Context, _, name string, args ...string) ([]byte, error) {
		calls = append(calls, append([]string{name}, args...))
		return nil, nil
	}}
	if _, err := c.TaskCommits(context.Background(), "/r", 1); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0][0] != "git" || calls[0][1] != "log" {
		t.Fatalf("calls = %v", calls)
	}
	for _, a := range calls[0] {
		if a == "--all" {
			goto ok
		}
	}
	t.Errorf("missing --all: %v", calls[0])
ok:
}

func TestTaskCommitDiffsArgvReadOnly(t *testing.T) {
	logOut := "aaa\x001\x00s\x001\x1e"
	var calls [][]string
	c := &Client{Run: func(_ context.Context, _, name string, args ...string) ([]byte, error) {
		calls = append(calls, append([]string{name}, args...))
		if args[0] == "log" {
			return []byte(logOut), nil
		}
		return nil, nil
	}}
	if _, err := c.TaskCommitDiffs(context.Background(), "/r", 1); err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{"log": true, "show": true}
	for _, call := range calls {
		if call[0] != "git" || !allowed[call[1]] {
			t.Errorf("unexpected command %v", call)
		}
	}
	if len(calls) != 2 || !strings.Contains(strings.Join(calls[1], " "), "--first-parent aaa") {
		t.Errorf("calls = %v", calls)
	}
}

func TestTaskCommitsIntegration(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	commit := func(file, msg string) {
		write(file, msg+"\n")
		git("add", file)
		git("commit", "-q", "-m", msg)
	}
	git("init", "-q", "-b", "main")
	commit("base.txt", "base")
	commit("a.txt", "first\n\nTend-Task: 790")
	commit("b.txt", "other\n\nTend-Task: 7900")
	commit("c.txt", "untagged")
	commit("d.txt", "multi\n\nTend-Task: 12, 790")
	git("checkout", "-q", "-b", "side")
	commit("e.txt", "side\n\nTend-Task: 790")
	git("checkout", "-q", "main")
	git("merge", "-q", "--no-ff", "-m", "merge side\n\nTend-Task: 790", "side")

	c := New()
	cs, err := c.TaskCommits(context.Background(), dir, 790)
	if err != nil {
		t.Fatal(err)
	}
	var subjects []string
	for _, cm := range cs {
		subjects = append(subjects, cm.Subject)
	}
	// Topological, oldest first; "side" precedes the merge that contains it.
	if subjects[0] != "first" || subjects[1] != "multi" || subjects[len(subjects)-1] != "merge side" || len(subjects) != 4 {
		t.Fatalf("subjects = %v", subjects)
	}

	diffs, err := c.TaskCommitDiffs(context.Background(), dir, 790)
	if err != nil {
		t.Fatal(err)
	}
	if len(diffs) != 4 {
		t.Fatalf("got %d diffs", len(diffs))
	}
	if diffs[0].Diff.Files[0].Path() != "a.txt" {
		t.Errorf("first diff = %+v", diffs[0].Diff.Files)
	}
	merge := diffs[3]
	if len(merge.Diff.Files) != 1 || merge.Diff.Files[0].Path() != "e.txt" {
		t.Errorf("merge diff (vs first parent) = %+v", merge.Diff.Files)
	}
}
