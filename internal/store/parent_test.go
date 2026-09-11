package store

import (
	"context"
	"strings"
	"testing"

	"github.com/jwstover/tend/internal/task"
)

// parentEvents filters the activity log down to the re-parent rows, so a
// test can assert on exactly what SetParent wrote.
func parentEvents(t *testing.T, s *Store) []task.Event {
	t.Helper()
	var out []task.Event
	for _, ev := range eventsSince(t, s) {
		if ev.Kind == task.EventParent {
			out = append(out, ev)
		}
	}
	return out
}

func mustChild(t *testing.T, s *Store, parentID int64, title string) task.Task {
	t.Helper()
	c, err := s.AddChild(context.Background(), parentID, title)
	if err != nil {
		t.Fatalf("AddChild(%d, %q): %v", parentID, title, err)
	}
	return c
}

func mustGet(t *testing.T, s *Store, id int64) task.Task {
	t.Helper()
	got, err := s.GetTask(context.Background(), id)
	if err != nil {
		t.Fatalf("GetTask(%d): %v", id, err)
	}
	return got
}

func TestSetParentPromotesChildToTopLevel(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	parent := mustAdd(t, s, "parent")
	child := mustChild(t, s, parent.ID, "child")

	if err := s.SetParent(ctx, child.ID, nil); err != nil {
		t.Fatalf("SetParent(nil): %v", err)
	}
	if got := mustGet(t, s, child.ID); got.ParentID != nil {
		t.Errorf("ParentID = %v, want nil", *got.ParentID)
	}

	events := parentEvents(t, s)
	if len(events) != 1 {
		t.Fatalf("got %d parent events, want 1: %+v", len(events), events)
	}
	ev := events[0]
	if ev.TaskID != child.ID || ev.TaskTitle != "child" {
		t.Errorf("event = %+v, want task %d %q", ev, child.ID, "child")
	}
	if ev.Old == nil || *ev.Old != "parent" || ev.New == nil || *ev.New != task.TopLevelLabel {
		t.Errorf("event old/new = %v/%v, want parent/%s", ev.Old, ev.New, task.TopLevelLabel)
	}
}

func TestSetParentDemotesTopLevelUnderAnother(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	host := mustAdd(t, s, "host")
	moved := mustAdd(t, s, "moved")

	if err := s.SetParent(ctx, moved.ID, &host.ID); err != nil {
		t.Fatalf("SetParent: %v", err)
	}
	if got := mustGet(t, s, moved.ID); got.ParentID == nil || *got.ParentID != host.ID {
		t.Errorf("ParentID = %v, want %d", got.ParentID, host.ID)
	}

	kids, err := s.ListChildren(ctx, host.ID)
	if err != nil {
		t.Fatalf("ListChildren: %v", err)
	}
	if len(kids) != 1 || kids[0].ID != moved.ID {
		t.Errorf("host's children = %+v, want just %d", kids, moved.ID)
	}

	events := parentEvents(t, s)
	if len(events) != 1 {
		t.Fatalf("got %d parent events, want 1", len(events))
	}
	if ev := events[0]; ev.Old == nil || *ev.Old != task.TopLevelLabel || ev.New == nil || *ev.New != "host" {
		t.Errorf("event old/new = %v/%v, want %s/host", ev.Old, ev.New, task.TopLevelLabel)
	}
}

func TestSetParentMovesBetweenParentsWithSubtree(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	a := mustAdd(t, s, "a")
	b := mustAdd(t, s, "b")
	mid := mustChild(t, s, a.ID, "mid")
	leaf := mustChild(t, s, mid.ID, "leaf")

	if err := s.SetParent(ctx, mid.ID, &b.ID); err != nil {
		t.Fatalf("SetParent: %v", err)
	}
	if got := mustGet(t, s, mid.ID); got.ParentID == nil || *got.ParentID != b.ID {
		t.Errorf("mid.ParentID = %v, want %d", got.ParentID, b.ID)
	}
	// The grandchild stays attached to mid: only the one edge changed.
	if got := mustGet(t, s, leaf.ID); got.ParentID == nil || *got.ParentID != mid.ID {
		t.Errorf("leaf.ParentID = %v, want %d (unchanged)", got.ParentID, mid.ID)
	}
	if kids, _ := s.ListChildren(ctx, a.ID); len(kids) != 0 {
		t.Errorf("a still has children: %+v", kids)
	}

	if ev := parentEvents(t, s); len(ev) != 1 || *ev[0].Old != "a" || *ev[0].New != "b" {
		t.Errorf("parent events = %+v, want one a -> b", ev)
	}
}

func TestSetParentCrossProjectReprojectsSubtree(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	other, err := s.CreateProject(ctx, "elsewhere")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	host, err := s.AddTaskIn(ctx, other.ID, "host")
	if err != nil {
		t.Fatalf("AddTaskIn: %v", err)
	}
	moved := mustAdd(t, s, "moved")
	kid := mustChild(t, s, moved.ID, "kid")
	grandkid := mustChild(t, s, kid.ID, "grandkid")

	if err := s.SetParent(ctx, moved.ID, &host.ID); err != nil {
		t.Fatalf("SetParent: %v", err)
	}
	for _, id := range []int64{moved.ID, kid.ID, grandkid.ID} {
		if got := mustGet(t, s, id); got.ProjectID != other.ID {
			t.Errorf("task %d ProjectID = %d, want %d (the new parent's project)", id, got.ProjectID, other.ID)
		}
	}

	// One parent event, and no project event: the project change is a
	// consequence of the move, not a second user action.
	var parents, projects int
	for _, ev := range eventsSince(t, s) {
		switch ev.Kind {
		case task.EventParent:
			parents++
		case task.EventProject:
			projects++
		}
	}
	if parents != 1 || projects != 0 {
		t.Errorf("events: %d parent, %d project; want 1 and 0", parents, projects)
	}
}

func TestSetParentPromoteKeepsProject(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	other, err := s.CreateProject(ctx, "elsewhere")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	parent, err := s.AddTaskIn(ctx, other.ID, "parent")
	if err != nil {
		t.Fatalf("AddTaskIn: %v", err)
	}
	child := mustChild(t, s, parent.ID, "child")

	if err := s.SetParent(ctx, child.ID, nil); err != nil {
		t.Fatalf("SetParent(nil): %v", err)
	}
	if got := mustGet(t, s, child.ID); got.ProjectID != other.ID {
		t.Errorf("promoted task ProjectID = %d, want %d (unchanged)", got.ProjectID, other.ID)
	}
}

func TestSetParentRejectsSelf(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	a := mustAdd(t, s, "a")
	err := s.SetParent(ctx, a.ID, &a.ID)
	if err == nil || !strings.Contains(err.Error(), "own parent") {
		t.Fatalf("SetParent(self) error = %v, want an own-parent refusal", err)
	}
	if got := mustGet(t, s, a.ID); got.ParentID != nil {
		t.Errorf("ParentID = %v after a refused move, want nil", *got.ParentID)
	}
	if ev := parentEvents(t, s); len(ev) != 0 {
		t.Errorf("a refused move wrote events: %+v", ev)
	}
}

func TestSetParentRejectsDescendant(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	root := mustAdd(t, s, "root")
	mid := mustChild(t, s, root.ID, "mid")
	leaf := mustChild(t, s, mid.ID, "leaf")

	// Direct child and deeper descendant alike.
	for _, bad := range []task.Task{mid, leaf} {
		err := s.SetParent(ctx, root.ID, &bad.ID)
		if err == nil || !strings.Contains(err.Error(), "own sub-task") {
			t.Errorf("SetParent(root under %q) error = %v, want a cycle refusal", bad.Title, err)
		}
	}
	if got := mustGet(t, s, root.ID); got.ParentID != nil {
		t.Errorf("root.ParentID = %v after refused moves, want nil", *got.ParentID)
	}
	if ev := parentEvents(t, s); len(ev) != 0 {
		t.Errorf("refused moves wrote events: %+v", ev)
	}
}

func TestSetParentUnchangedWritesNothing(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	parent := mustAdd(t, s, "parent")
	child := mustChild(t, s, parent.ID, "child")
	top := mustAdd(t, s, "top")

	if err := s.SetParent(ctx, child.ID, &parent.ID); err != nil {
		t.Fatalf("SetParent (same parent): %v", err)
	}
	if err := s.SetParent(ctx, top.ID, nil); err != nil {
		t.Fatalf("SetParent (already top level): %v", err)
	}
	if ev := parentEvents(t, s); len(ev) != 0 {
		t.Errorf("no-op moves wrote events: %+v", ev)
	}
}

func TestSetParentMissingParentErrors(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	a := mustAdd(t, s, "a")
	missing := a.ID + 1000
	if err := s.SetParent(ctx, a.ID, &missing); err == nil {
		t.Fatal("SetParent(missing parent) = nil, want an error")
	}
	if got := mustGet(t, s, a.ID); got.ParentID != nil {
		t.Errorf("ParentID = %v after a failed move, want nil", *got.ParentID)
	}
}

func TestSetParentMissingTaskErrors(t *testing.T) {
	s := newTestStore(t)
	if err := s.SetParent(context.Background(), 9999, nil); err == nil {
		t.Fatal("SetParent(missing task) = nil, want an error")
	}
}

func TestListProjectTasksSpansDepthAndState(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	other, err := s.CreateProject(ctx, "elsewhere")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	root := mustAdd(t, s, "root")
	kid := mustChild(t, s, root.ID, "kid")
	grandkid := mustChild(t, s, kid.ID, "grandkid")
	done := mustAdd(t, s, "done")
	if err := s.SetState(ctx, done.ID, task.StateDone); err != nil {
		t.Fatalf("SetState: %v", err)
	}
	if _, err := s.AddTaskIn(ctx, other.ID, "not here"); err != nil {
		t.Fatalf("AddTaskIn: %v", err)
	}

	got, err := s.ListProjectTasks(ctx, task.DefaultProjectID)
	if err != nil {
		t.Fatalf("ListProjectTasks: %v", err)
	}
	want := []int64{root.ID, kid.ID, grandkid.ID, done.ID}
	if len(got) != len(want) {
		t.Fatalf("got %d tasks, want %d: %+v", len(got), len(want), got)
	}
	for i, id := range want {
		if got[i].ID != id {
			t.Errorf("tasks[%d].ID = %d, want %d (ordered by id)", i, got[i].ID, id)
		}
	}
}
