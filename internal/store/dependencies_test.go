package store

import (
	"context"
	"errors"
	"testing"

	"github.com/jwstover/tend/internal/task"
)

func ids(tasks []task.Task) []int64 {
	out := make([]int64, 0, len(tasks))
	for _, t := range tasks {
		out = append(out, t.ID)
	}
	return out
}

func sameIDs(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestAddDependencyRecordsBothDirections(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	a := mustAdd(t, s, "a")
	b := mustAdd(t, s, "b")
	c := mustAdd(t, s, "c")

	if err := s.AddDependency(ctx, a.ID, b.ID); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}
	if err := s.AddDependency(ctx, a.ID, c.ID); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}
	// Recording the same edge twice is a no-op, not an error.
	if err := s.AddDependency(ctx, a.ID, b.ID); err != nil {
		t.Fatalf("AddDependency (again): %v", err)
	}

	blockers, err := s.Blockers(ctx, a.ID)
	if err != nil {
		t.Fatalf("Blockers: %v", err)
	}
	if got, want := ids(blockers), []int64{b.ID, c.ID}; !sameIDs(got, want) {
		t.Errorf("Blockers(a) = %v, want %v", got, want)
	}
	blocking, err := s.Blocking(ctx, b.ID)
	if err != nil {
		t.Fatalf("Blocking: %v", err)
	}
	if got, want := ids(blocking), []int64{a.ID}; !sameIDs(got, want) {
		t.Errorf("Blocking(b) = %v, want %v", got, want)
	}
	if blocking, _ := s.Blocking(ctx, a.ID); len(blocking) != 0 {
		t.Errorf("Blocking(a) = %v, want none", ids(blocking))
	}
}

func TestAddDependencyRefusesSelfAndCycles(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	a := mustAdd(t, s, "a")
	b := mustAdd(t, s, "b")
	c := mustAdd(t, s, "c")

	if err := s.AddDependency(ctx, a.ID, a.ID); !errors.Is(err, task.ErrSelfDependency) {
		t.Errorf("self dependency: err = %v, want ErrSelfDependency", err)
	}

	// a -> b -> c, then c -> a would close the loop.
	if err := s.AddDependency(ctx, a.ID, b.ID); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}
	if err := s.AddDependency(ctx, b.ID, c.ID); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}
	if err := s.AddDependency(ctx, c.ID, a.ID); !errors.Is(err, task.ErrDependencyCycle) {
		t.Errorf("transitive cycle: err = %v, want ErrDependencyCycle", err)
	}
	if err := s.AddDependency(ctx, b.ID, a.ID); !errors.Is(err, task.ErrDependencyCycle) {
		t.Errorf("direct cycle: err = %v, want ErrDependencyCycle", err)
	}
	// The refused edges left nothing behind.
	if blockers, _ := s.Blockers(ctx, c.ID); len(blockers) != 0 {
		t.Errorf("Blockers(c) = %v after refused edges, want none", ids(blockers))
	}
	// Siblings waiting on the same task is fine: a diamond is not a cycle.
	if err := s.AddDependency(ctx, a.ID, c.ID); err != nil {
		t.Errorf("diamond a -> c: %v", err)
	}
}

func TestAddDependencyUnknownTaskIsAnError(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	a := mustAdd(t, s, "a")
	if err := s.AddDependency(ctx, a.ID, 999); err == nil {
		t.Error("dependency on a missing task: want an error")
	}
	if err := s.AddDependency(ctx, 999, a.ID); err == nil {
		t.Error("dependency from a missing task: want an error")
	}
}

func TestRemoveDependency(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	a := mustAdd(t, s, "a")
	b := mustAdd(t, s, "b")
	if err := s.AddDependency(ctx, a.ID, b.ID); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}
	if err := s.RemoveDependency(ctx, a.ID, b.ID); err != nil {
		t.Fatalf("RemoveDependency: %v", err)
	}
	if blockers, _ := s.Blockers(ctx, a.ID); len(blockers) != 0 {
		t.Errorf("Blockers(a) = %v after removal, want none", ids(blockers))
	}
	// Removing what is not there is quiet.
	if err := s.RemoveDependency(ctx, a.ID, b.ID); err != nil {
		t.Errorf("RemoveDependency (again): %v", err)
	}
}

func TestSetDependenciesReplacesWholeList(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	a := mustAdd(t, s, "a")
	b := mustAdd(t, s, "b")
	c := mustAdd(t, s, "c")
	d := mustAdd(t, s, "d")

	if err := s.SetDependencies(ctx, a.ID, []int64{b.ID, c.ID}); err != nil {
		t.Fatalf("SetDependencies: %v", err)
	}
	if err := s.SetDependencies(ctx, a.ID, []int64{c.ID, d.ID}); err != nil {
		t.Fatalf("SetDependencies (replace): %v", err)
	}
	blockers, err := s.Blockers(ctx, a.ID)
	if err != nil {
		t.Fatalf("Blockers: %v", err)
	}
	if got, want := ids(blockers), []int64{c.ID, d.ID}; !sameIDs(got, want) {
		t.Errorf("Blockers(a) = %v, want %v", got, want)
	}

	// A refused id rolls the whole replacement back.
	if err := s.SetDependencies(ctx, a.ID, []int64{b.ID, a.ID}); !errors.Is(err, task.ErrSelfDependency) {
		t.Errorf("self in list: err = %v, want ErrSelfDependency", err)
	}
	if err := s.SetDependencies(ctx, a.ID, []int64{b.ID, 999}); err == nil {
		t.Error("missing id in list: want an error")
	}
	blockers, _ = s.Blockers(ctx, a.ID)
	if got, want := ids(blockers), []int64{c.ID, d.ID}; !sameIDs(got, want) {
		t.Errorf("Blockers(a) = %v after refused replacements, want %v untouched", got, want)
	}

	if err := s.SetDependencies(ctx, a.ID, nil); err != nil {
		t.Fatalf("SetDependencies (clear): %v", err)
	}
	if blockers, _ := s.Blockers(ctx, a.ID); len(blockers) != 0 {
		t.Errorf("Blockers(a) = %v after clear, want none", ids(blockers))
	}
}

func TestSetDependenciesRefusesCycle(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	a := mustAdd(t, s, "a")
	b := mustAdd(t, s, "b")
	if err := s.AddDependency(ctx, b.ID, a.ID); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}
	if err := s.SetDependencies(ctx, a.ID, []int64{b.ID}); !errors.Is(err, task.ErrDependencyCycle) {
		t.Errorf("err = %v, want ErrDependencyCycle", err)
	}
}

func TestBlockerCountsTrackOpenBlockers(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	a := mustAdd(t, s, "a")
	b := mustAdd(t, s, "b")
	c := mustAdd(t, s, "c")
	free := mustAdd(t, s, "free")
	if err := s.SetDependencies(ctx, a.ID, []int64{b.ID, c.ID}); err != nil {
		t.Fatalf("SetDependencies: %v", err)
	}

	counts, err := s.BlockerCounts(ctx)
	if err != nil {
		t.Fatalf("BlockerCounts: %v", err)
	}
	if got := counts[a.ID]; got != (task.BlockerCount{Open: 2, Total: 2}) {
		t.Errorf("counts[a] = %+v, want 2 open of 2", got)
	}
	if _, ok := counts[free.ID]; ok {
		t.Error("a task with no dependencies should have no entry")
	}

	if err := s.SetState(ctx, b.ID, task.StateDone); err != nil {
		t.Fatalf("SetState: %v", err)
	}
	counts, _ = s.BlockerCounts(ctx)
	if got := counts[a.ID]; got != (task.BlockerCount{Open: 1, Total: 2}) || !got.Blocked() {
		t.Errorf("counts[a] = %+v after one blocker done, want 1 open of 2", got)
	}
	if err := s.SetState(ctx, c.ID, task.StateDone); err != nil {
		t.Fatalf("SetState: %v", err)
	}
	counts, _ = s.BlockerCounts(ctx)
	if got := counts[a.ID]; got != (task.BlockerCount{Open: 0, Total: 2}) || got.Blocked() {
		t.Errorf("counts[a] = %+v after every blocker done, want 0 open of 2", got)
	}
}

func TestDependenciesCascadeWithEitherTask(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	a := mustAdd(t, s, "a")
	b := mustAdd(t, s, "b")
	c := mustAdd(t, s, "c")
	if err := s.AddDependency(ctx, a.ID, b.ID); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}
	if err := s.AddDependency(ctx, c.ID, a.ID); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}

	// Deleting the blocker frees the task; deleting the task frees what
	// waited on it.
	if err := s.DeleteTask(ctx, b.ID); err != nil {
		t.Fatalf("DeleteTask: %v", err)
	}
	if blockers, _ := s.Blockers(ctx, a.ID); len(blockers) != 0 {
		t.Errorf("Blockers(a) = %v after its blocker was deleted, want none", ids(blockers))
	}
	if err := s.DeleteTask(ctx, a.ID); err != nil {
		t.Fatalf("DeleteTask: %v", err)
	}
	if blockers, _ := s.Blockers(ctx, c.ID); len(blockers) != 0 {
		t.Errorf("Blockers(c) = %v after its blocker was deleted, want none", ids(blockers))
	}
	counts, _ := s.BlockerCounts(ctx)
	if len(counts) != 0 {
		t.Errorf("BlockerCounts = %v after the deletes, want empty", counts)
	}
}

func TestListOpenTasksSpansProjectsAndSkipsDone(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	p, err := s.CreateProject(ctx, "elsewhere")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	a := mustAdd(t, s, "a")
	done := mustAdd(t, s, "done")
	someday := mustAdd(t, s, "someday")
	child, err := s.AddChild(ctx, a.ID, "child")
	if err != nil {
		t.Fatalf("AddChild: %v", err)
	}
	far, err := s.AddTaskIn(ctx, p.ID, "far")
	if err != nil {
		t.Fatalf("AddTaskIn: %v", err)
	}
	if err := s.SetState(ctx, done.ID, task.StateDone); err != nil {
		t.Fatalf("SetState: %v", err)
	}
	if err := s.SetState(ctx, someday.ID, task.StateSomeday); err != nil {
		t.Fatalf("SetState: %v", err)
	}

	open, err := s.ListOpenTasks(ctx)
	if err != nil {
		t.Fatalf("ListOpenTasks: %v", err)
	}
	if got, want := ids(open), []int64{a.ID, someday.ID, child.ID, far.ID}; !sameIDs(got, want) {
		t.Errorf("ListOpenTasks = %v, want %v (by project then id, done left out)", got, want)
	}
}
