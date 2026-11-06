package store

import (
	"context"
	"testing"

	"github.com/jwstover/tend/internal/task"
)

func TestSearchTasks(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	other, err := s.CreateProject(ctx, "other")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	title, err := s.AddTask(ctx, "Fix the Widget")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	body, err := s.AddTaskWithBodyIn(ctx, other.ID, "unrelated", "the widget leaks\nunder load")
	if err != nil {
		t.Fatalf("AddTaskWithBodyIn: %v", err)
	}
	tagged, err := s.AddTask(ctx, "tagged one")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	if err := s.SetTags(ctx, tagged.ID, []string{"widgetry"}); err != nil {
		t.Fatalf("SetTags: %v", err)
	}
	closed, err := s.AddTaskWithBodyIn(ctx, other.ID, "old", "widget history, load tested")
	if err != nil {
		t.Fatalf("AddTaskWithBodyIn: %v", err)
	}
	if err := s.SetState(ctx, closed.ID, task.StateDone); err != nil {
		t.Fatalf("SetState: %v", err)
	}
	if _, err := s.AddTask(ctx, "100% sure"); err != nil {
		t.Fatalf("AddTask: %v", err)
	}

	ids := func(ts []task.Task) []int64 {
		out := make([]int64, len(ts))
		for i, x := range ts {
			out[i] = x.ID
		}
		return out
	}
	search := func(q string) []int64 {
		t.Helper()
		got, err := s.SearchTasks(ctx, q, 50)
		if err != nil {
			t.Fatalf("SearchTasks(%q): %v", q, err)
		}
		return ids(got)
	}
	has := func(got []int64, want ...int64) bool {
		for _, w := range want {
			found := false
			for _, g := range got {
				found = found || g == w
			}
			if !found {
				return false
			}
		}
		return true
	}

	got := search("WIDGET")
	if len(got) != 4 || !has(got, title.ID, body.ID, tagged.ID, closed.ID) {
		t.Errorf("widget: got %v, want title, body, tag and the done task", got)
	}
	if got[0] != title.ID {
		t.Errorf("title hit should sort first, got %v", got)
	}
	if got := search("widget load"); len(got) != 2 || !has(got, body.ID, closed.ID) {
		t.Errorf("all words must match: got %v", got)
	}
	if got := search("%"); len(got) != 1 {
		t.Errorf("a typed %% is literal: got %v", got)
	}
	if got := search("   "); len(got) != 0 {
		t.Errorf("blank query should match nothing, got %v", got)
	}
	if got, _ := s.SearchTasks(ctx, "widget", 2); len(got) != 2 {
		t.Errorf("limit not applied: %d rows", len(got))
	}
}
