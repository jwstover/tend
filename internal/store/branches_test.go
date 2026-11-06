package store

import (
	"context"
	"errors"
	"testing"

	"github.com/jwstover/tend/internal/task"
)

func TestLinkBranchIsIdempotent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	tk, _ := s.AddTask(ctx, "linked")

	a, err := s.LinkBranch(ctx, tk.ID, "/repo/tend", "feat/x")
	if err != nil {
		t.Fatalf("LinkBranch: %v", err)
	}
	b, err := s.LinkBranch(ctx, tk.ID, "/repo/tend", "feat/x")
	if err != nil {
		t.Fatalf("LinkBranch again: %v", err)
	}
	if a.ID != b.ID || a.ID == 0 {
		t.Errorf("second link id = %d, want the first's %d", b.ID, a.ID)
	}
	if got, _ := s.TaskBranches(ctx, tk.ID); len(got) != 1 {
		t.Errorf("TaskBranches = %v, want one link", got)
	}
}

func TestLinkBranchNormalizesAndRefusesBlank(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	tk, _ := s.AddTask(ctx, "linked")

	l, err := s.LinkBranch(ctx, tk.ID, " /repo/a/../tend/ ", "  feat/x \n")
	if err != nil {
		t.Fatalf("LinkBranch: %v", err)
	}
	if l.RepoPath != "/repo/tend" || l.Branch != "feat/x" {
		t.Errorf("link = %q %q, want /repo/tend feat/x", l.RepoPath, l.Branch)
	}
	// The same link spelled differently is the same link.
	if l2, _ := s.LinkBranch(ctx, tk.ID, "/repo/tend", "feat/x"); l2.ID != l.ID {
		t.Errorf("normalized duplicate got a new row")
	}

	if _, err := s.LinkBranch(ctx, tk.ID, "/repo", "  "); !errors.Is(err, task.ErrEmptyBranch) {
		t.Errorf("blank branch err = %v, want ErrEmptyBranch", err)
	}
	if _, err := s.LinkBranch(ctx, tk.ID, " ", "b"); !errors.Is(err, task.ErrEmptyRepoPath) {
		t.Errorf("blank repo err = %v, want ErrEmptyRepoPath", err)
	}
	if _, err := s.LinkBranch(ctx, 9999, "/repo", "b"); err == nil {
		t.Errorf("linking a missing task succeeded")
	}
}

func TestUnlinkBranchAndOrdering(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	tk, _ := s.AddTask(ctx, "linked")

	for _, b := range []string{"one", "two", "three"} {
		if _, err := s.LinkBranch(ctx, tk.ID, "/repo", b); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.UnlinkBranch(ctx, tk.ID, "/repo", "two"); err != nil {
		t.Fatalf("UnlinkBranch: %v", err)
	}
	// Unlinking what is not there is fine.
	if err := s.UnlinkBranch(ctx, tk.ID, "/repo", "two"); err != nil {
		t.Errorf("second UnlinkBranch: %v", err)
	}
	got, _ := s.TaskBranches(ctx, tk.ID)
	if len(got) != 2 || got[0].Branch != "one" || got[1].Branch != "three" {
		t.Errorf("TaskBranches = %v, want [one three] oldest first", got)
	}
}

func TestSameBranchInTwoReposOrTasks(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	a, _ := s.AddTask(ctx, "a")
	b, _ := s.AddTask(ctx, "b")
	for _, c := range []struct {
		id   int64
		repo string
	}{{a.ID, "/r1"}, {a.ID, "/r2"}, {b.ID, "/r1"}} {
		if _, err := s.LinkBranch(ctx, c.id, c.repo, "main-work"); err != nil {
			t.Fatalf("LinkBranch %v: %v", c, err)
		}
	}
	if got, _ := s.TaskBranches(ctx, a.ID); len(got) != 2 {
		t.Errorf("task a links = %d, want 2", len(got))
	}
}

func TestRelinkBranch(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	tk, _ := s.AddTask(ctx, "linked")
	old, _ := s.LinkBranch(ctx, tk.ID, "/repo", "old")
	other, _ := s.LinkBranch(ctx, tk.ID, "/repo", "taken")

	if err := s.RelinkBranch(ctx, old.ID, " new "); err != nil {
		t.Fatalf("RelinkBranch: %v", err)
	}
	got, _ := s.TaskBranches(ctx, tk.ID)
	if got[0].ID != old.ID || got[0].Branch != "new" {
		t.Errorf("after relink = %v, want first link renamed to new", got)
	}
	if err := s.RelinkBranch(ctx, old.ID, "taken"); !errors.Is(err, task.ErrBranchLinkTaken) {
		t.Errorf("relink onto existing = %v, want ErrBranchLinkTaken", err)
	}
	if err := s.RelinkBranch(ctx, other.ID+100, "x"); !errors.Is(err, task.ErrBranchLinkGone) {
		t.Errorf("relink missing id = %v, want ErrBranchLinkGone", err)
	}
	if err := s.RelinkBranch(ctx, old.ID, " "); !errors.Is(err, task.ErrEmptyBranch) {
		t.Errorf("relink blank = %v, want ErrEmptyBranch", err)
	}
}

func TestBranchLinksCascadeWithTask(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	tk, _ := s.AddTask(ctx, "doomed")
	if _, err := s.LinkBranch(ctx, tk.ID, "/repo", "b"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteTask(ctx, tk.ID); err != nil {
		t.Fatalf("DeleteTask: %v", err)
	}
	if got, err := s.TaskBranches(ctx, tk.ID); err != nil || len(got) != 0 {
		t.Errorf("TaskBranches after delete = %v, %v; want none", got, err)
	}
}
