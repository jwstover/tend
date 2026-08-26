package store

import (
	"context"
	"testing"

	"github.com/jwstover/tend/internal/task"
)

func TestSetTagsReplacesTheWholeList(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	created, err := s.AddTask(ctx, "tag me")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}

	if err := s.SetTags(ctx, created.ID, []string{"alpha", "beta", "gamma"}); err != nil {
		t.Fatalf("SetTags: %v", err)
	}
	got, err := s.TagsForTask(ctx, created.ID)
	if err != nil {
		t.Fatalf("TagsForTask: %v", err)
	}
	if len(got) != 3 || got[0] != "alpha" || got[1] != "beta" || got[2] != "gamma" {
		t.Fatalf("tags = %v, want [alpha beta gamma] in order", got)
	}

	// The prompt hands over the complete list every time, so this is a
	// replacement, not a merge.
	if err := s.SetTags(ctx, created.ID, []string{"beta", "delta"}); err != nil {
		t.Fatalf("SetTags (replace): %v", err)
	}
	if got, _ = s.TagsForTask(ctx, created.ID); len(got) != 2 || got[0] != "beta" || got[1] != "delta" {
		t.Errorf("tags after replace = %v, want [beta delta]", got)
	}

	// A tag exists because a task carries it: alpha and gamma lost their
	// last reference and should be gone, not lingering in the tag list.
	all, err := s.ListTags(ctx)
	if err != nil {
		t.Fatalf("ListTags: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("ListTags = %v, want only the two still in use", all)
	}
}

// A tag shared by two tasks must survive one of them dropping it.
func TestSharedTagSurvivesOneTaskDroppingIt(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	keeps, err := s.AddTask(ctx, "keeps the tag")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	drops, err := s.AddTask(ctx, "drops the tag")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	for _, id := range []int64{keeps.ID, drops.ID} {
		if err := s.SetTags(ctx, id, []string{"shared"}); err != nil {
			t.Fatalf("SetTags: %v", err)
		}
	}

	if err := s.SetTags(ctx, drops.ID, nil); err != nil {
		t.Fatalf("SetTags(nil): %v", err)
	}
	if got, _ := s.TagsForTask(ctx, keeps.ID); len(got) != 1 || got[0] != "shared" {
		t.Errorf("surviving task's tags = %v, want [shared]", got)
	}
	if all, _ := s.ListTags(ctx); len(all) != 1 {
		t.Errorf("ListTags = %v, want the still-referenced tag", all)
	}
}

// Case variants fold into one tag, matching the schema's NOCASE unique.
func TestSetTagsFoldsCaseVariants(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	created, err := s.AddTask(ctx, "case test")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	if err := s.SetTags(ctx, created.ID, task.ParseTags("Work work WORK")); err != nil {
		t.Fatalf("SetTags: %v", err)
	}
	got, err := s.TagsForTask(ctx, created.ID)
	if err != nil {
		t.Fatalf("TagsForTask: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("tags = %v, want a single folded tag", got)
	}
}

func TestTagsByTaskBatchesEveryTask(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	one, err := s.AddTask(ctx, "one")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	two, err := s.AddTask(ctx, "two")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	untagged, err := s.AddTask(ctx, "untagged")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	if err := s.SetTags(ctx, one.ID, []string{"a", "b"}); err != nil {
		t.Fatalf("SetTags: %v", err)
	}
	if err := s.SetTags(ctx, two.ID, []string{"b"}); err != nil {
		t.Fatalf("SetTags: %v", err)
	}

	byTask, err := s.TagsByTask(ctx)
	if err != nil {
		t.Fatalf("TagsByTask: %v", err)
	}
	if len(byTask[one.ID]) != 2 {
		t.Errorf("task one tags = %v, want two", byTask[one.ID])
	}
	if len(byTask[two.ID]) != 1 || byTask[two.ID][0] != "b" {
		t.Errorf("task two tags = %v, want [b]", byTask[two.ID])
	}
	// An untagged task is simply absent, not an empty slice.
	if _, present := byTask[untagged.ID]; present {
		t.Errorf("untagged task should not appear in the map, got %v", byTask[untagged.ID])
	}
}

// Deleting a task takes its tag links with it (ON DELETE CASCADE), the way
// sub-tasks already cascade.
func TestDeletingATaskDropsItsTagLinks(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	created, err := s.AddTask(ctx, "doomed")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	if err := s.SetTags(ctx, created.ID, []string{"only-here"}); err != nil {
		t.Fatalf("SetTags: %v", err)
	}
	if err := s.DeleteTask(ctx, created.ID); err != nil {
		t.Fatalf("DeleteTask: %v", err)
	}
	if got, _ := s.TagsForTask(ctx, created.ID); len(got) != 0 {
		t.Errorf("tags for a deleted task = %v, want none", got)
	}
}
