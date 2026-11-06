package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/jwstover/tend/internal/store"
	"github.com/jwstover/tend/internal/task"
	"github.com/jwstover/tend/internal/vcs"
)

// fakeVCS answers from maps, so the picker is tested without git.
type fakeVCS struct {
	branches map[string][]vcs.Branch // by repo
	err      error
}

func (f *fakeVCS) IsGitButler(context.Context, string) bool { return false }
func (f *fakeVCS) Branches(_ context.Context, repo string) ([]vcs.Branch, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.branches[repo], nil
}
func (f *fakeVCS) BranchExists(_ context.Context, repo, branch string) bool {
	for _, b := range f.branches[repo] {
		if b.Name == branch {
			return true
		}
	}
	return false
}
func (f *fakeVCS) Base(context.Context, string, string) (string, error) { return "", nil }
func (f *fakeVCS) CommittedDiff(context.Context, string, string) (vcs.Diff, error) {
	return vcs.Diff{}, nil
}
func (f *fakeVCS) UncommittedDiff(context.Context, string, string) (vcs.Diff, error) {
	return vcs.Diff{}, nil
}

const testRepo = "/work/repo"

// linkApp opens the picker on one task whose project cwd is testRepo.
func linkApp(t *testing.T, f *fakeVCS) (tea.Model, *store.Store, task.Task) {
	t.Helper()
	ctx := context.Background()
	m, s := newTestApp(t)
	tk, err := s.AddTask(ctx, "link me")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetProjectCwd(ctx, task.DefaultProjectID, testRepo); err != nil {
		t.Fatal(err)
	}
	a := m.(app)
	a.vcs = f
	m = drive(t, a, refreshMsg{})
	return m, s, tk
}

func openLink(t *testing.T, m tea.Model) tea.Model {
	t.Helper()
	m = drive(t, m, keyPress('L'))
	if !m.(app).lp.open {
		t.Fatalf("picker did not open; status %q", m.(app).status.text)
	}
	return m
}

func rowNames(m tea.Model) []string {
	var out []string
	for _, r := range m.(app).linkPickerMatches() {
		out = append(out, r.name)
	}
	return out
}

func TestLinkPickerListsAndToggles(t *testing.T) {
	ctx := context.Background()
	f := &fakeVCS{}
	m, s, tk := linkApp(t, f)
	hint := fmt.Sprintf("task-%d-work", tk.ID)
	f.branches = map[string][]vcs.Branch{testRepo: {
		{Name: "alpha", Commits: 2}, {Name: hint, Commits: 1, Uncommitted: true}, {Name: "beta"},
	}}
	m = openLink(t, m)
	if got := strings.Join(rowNames(m), ","); got != hint+",alpha,beta" { // hinted branch sorts first
		t.Fatalf("rows = %s", got)
	}
	if !strings.Contains(m.View().Content, "uncommitted") {
		t.Error("view lacks the uncommitted marker")
	}

	// Digit 2 links alpha; the picker stays open.
	m = drive(t, m, keyPress('2'))
	links, _ := s.TaskBranches(ctx, tk.ID)
	if len(links) != 1 || links[0].Branch != "alpha" || links[0].RepoPath != testRepo {
		t.Fatalf("links = %+v", links)
	}
	if !m.(app).lp.open {
		t.Error("picker closed after toggle")
	}
	// Enter on the first (hinted) row links it too; the picker stays open.
	m = drive(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if links, _ = s.TaskBranches(ctx, tk.ID); len(links) != 2 {
		t.Fatalf("enter should link the first row: %+v", links)
	}
	// Enter again unlinks it.
	m = drive(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if links, _ = s.TaskBranches(ctx, tk.ID); len(links) != 1 {
		t.Fatalf("second enter should unlink: %+v", links)
	}
	if !m.(app).lp.open {
		t.Error("picker closed")
	}
}

func TestLinkPickerFuzzyFilter(t *testing.T) {
	f := &fakeVCS{branches: map[string][]vcs.Branch{testRepo: {{Name: "feature/login"}, {Name: "fix-lint"}, {Name: "main"}}}}
	m, _, _ := linkApp(t, f)
	m = openLink(t, m)
	for _, r := range "flg" {
		m = drive(t, m, keyPress(r))
	}
	if got := strings.Join(rowNames(m), ","); got != "feature/login" {
		t.Errorf("fuzzy rows = %q", got)
	}
}

func TestLinkPickerMissingAndRelink(t *testing.T) {
	ctx := context.Background()
	f := &fakeVCS{branches: map[string][]vcs.Branch{testRepo: {{Name: "new-name"}}}}
	m, s, tk := linkApp(t, f)
	if _, err := s.LinkBranch(ctx, tk.ID, testRepo, "old-name"); err != nil {
		t.Fatal(err)
	}
	m = openLink(t, m)
	rows := m.(app).linkPickerMatches()
	if len(rows) != 2 || rows[0].name != "old-name" || !rows[0].missing || !rows[0].linked {
		t.Fatalf("rows = %+v", rows)
	}
	if !strings.Contains(m.View().Content, "missing") {
		t.Error("view lacks the missing marker")
	}
	m = drive(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.(app).lp.relinkID == 0 || !strings.Contains(m.(app).status.text, "re-link") {
		t.Fatalf("not in relink mode; status %q", m.(app).status.text)
	}
	m = drive(t, m, keyPress('2')) // new-name
	links, _ := s.TaskBranches(ctx, tk.ID)
	if len(links) != 1 || links[0].Branch != "new-name" {
		t.Fatalf("links after relink = %+v", links)
	}
	if r := m.(app).linkPickerMatches(); r[0].name != "new-name" || r[0].missing || !r[0].linked {
		t.Errorf("rows after relink = %+v", r)
	}
}

func TestLinkPickerDegradesQuietly(t *testing.T) {
	for name, err := range map[string]error{
		"no git":     vcs.ErrNoGit,
		"not a repo": fmt.Errorf("x: %w", vcs.ErrNotARepo),
	} {
		t.Run(name, func(t *testing.T) {
			m, _, _ := linkApp(t, &fakeVCS{err: err})
			m = drive(t, m, keyPress('L'))
			a := m.(app)
			if a.lp.open || a.modal.Active() || a.status.text == "" {
				t.Errorf("open=%v status=%q", a.lp.open, a.status.text)
			}
		})
	}
	t.Run("no cwd", func(t *testing.T) {
		m, s := newTestApp(t)
		if _, err := s.AddTask(context.Background(), "x"); err != nil {
			t.Fatal(err)
		}
		a := m.(app)
		a.vcs, a.startCwd = &fakeVCS{}, ""
		m = drive(t, a, refreshMsg{})
		m = drive(t, m, keyPress('L'))
		if m.(app).lp.open || m.(app).status.text == "" {
			t.Errorf("open=%v status=%q", m.(app).lp.open, m.(app).status.text)
		}
	})
}

func TestHasTaskHint(t *testing.T) {
	h := []string{"task-79", "sub-79"}
	for name, want := range map[string]bool{
		"task-79/x": true, "x/sub-79": true, "task-790/x": false, "other": false,
	} {
		if got := hasTaskHint(name, h); got != want {
			t.Errorf("hasTaskHint(%q) = %v", name, got)
		}
	}
}
