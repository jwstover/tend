package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/jwstover/tend/internal/task"
)

// depEdge is one task_dependencies row in the fake: taskID waits on
// dependsOnID.
type depEdge struct{ taskID, dependsOnID int64 }

// ---- fakeStore: dependencies -----------------------------------------------
//
// The fake mirrors internal/store's rules: no self-dependency, no cycle
// (checked transitively), both tasks must exist, adding an existing edge
// is a no-op, SetDependencies is all-or-nothing, and Blockers/Blocking
// come back in insertion order.

func (s *fakeStore) waitsOn(root int64) map[int64]bool {
	seen := map[int64]bool{}
	queue := []int64{root}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		for _, e := range s.deps {
			if e.taskID == id && !seen[e.dependsOnID] {
				seen[e.dependsOnID] = true
				queue = append(queue, e.dependsOnID)
			}
		}
	}
	return seen
}

func (s *fakeStore) checkDependency(taskID, dependsOnID int64) error {
	if taskID == dependsOnID {
		return task.ErrSelfDependency
	}
	if _, ok := s.tasks[taskID]; !ok {
		return fmt.Errorf("loading task %d: no such task", taskID)
	}
	if _, ok := s.tasks[dependsOnID]; !ok {
		return fmt.Errorf("loading task %d: no such task", dependsOnID)
	}
	if s.waitsOn(dependsOnID)[taskID] {
		return task.ErrDependencyCycle
	}
	return nil
}

func (s *fakeStore) AddDependency(_ context.Context, taskID, dependsOnID int64) error {
	if err := s.checkDependency(taskID, dependsOnID); err != nil {
		return err
	}
	if slices.Contains(s.deps, depEdge{taskID, dependsOnID}) {
		return nil
	}
	s.deps = append(s.deps, depEdge{taskID, dependsOnID})
	return nil
}

func (s *fakeStore) RemoveDependency(_ context.Context, taskID, dependsOnID int64) error {
	s.deps = slices.DeleteFunc(s.deps, func(e depEdge) bool {
		return e == depEdge{taskID, dependsOnID}
	})
	return nil
}

func (s *fakeStore) SetDependencies(_ context.Context, taskID int64, dependsOn []int64) error {
	if _, ok := s.tasks[taskID]; !ok {
		return fmt.Errorf("loading task %d: no such task", taskID)
	}
	// Validate against the graph as it would be after the clear, then
	// swap in one go, so a refused id leaves the old list standing.
	kept := slices.DeleteFunc(slices.Clone(s.deps), func(e depEdge) bool { return e.taskID == taskID })
	old := s.deps
	s.deps = kept
	for _, id := range dependsOn {
		if err := s.AddDependency(context.Background(), taskID, id); err != nil {
			s.deps = old
			return err
		}
	}
	return nil
}

func (s *fakeStore) Blockers(_ context.Context, taskID int64) ([]task.Task, error) {
	var out []task.Task
	for _, e := range s.deps {
		if e.taskID == taskID {
			out = append(out, s.tasks[e.dependsOnID])
		}
	}
	return out, nil
}

func (s *fakeStore) Blocking(_ context.Context, taskID int64) ([]task.Task, error) {
	var out []task.Task
	for _, e := range s.deps {
		if e.dependsOnID == taskID {
			out = append(out, s.tasks[e.taskID])
		}
	}
	return out, nil
}

// ---- tests -----------------------------------------------------------------

func depIDs(deps []depOut) []int64 {
	out := make([]int64, 0, len(deps))
	for _, d := range deps {
		out = append(out, d.ID)
	}
	return out
}

func TestCreateTaskWithDependsOn(t *testing.T) {
	store := newFakeStore(
		task.Task{ID: 1, Title: "bound", State: task.StateDoing},
		task.Task{ID: 2, Title: "the schema", State: task.StateTodo},
	)
	cs := dial(t, store, 1)

	got := callTool[taskOut](t, cs, "create_task", map[string]any{
		"title": "the endpoint", "depends_on": []int64{2},
	})
	if !slices.Equal(depIDs(got.DependsOn), []int64{2}) {
		t.Errorf("depends_on = %v, want [2]", got.DependsOn)
	}
	if !got.IsBlocked {
		t.Error("is_blocked = false with an open blocker, want true")
	}
	if len(got.DependsOn) == 1 && (got.DependsOn[0].Title != "the schema" || got.DependsOn[0].State != "todo") {
		t.Errorf("depends_on[0] = %+v, want the blocker's title and state", got.DependsOn[0])
	}

	// The blocker sees the reverse edge.
	blocker := callTool[taskOut](t, cs, "get_task", map[string]any{"task_id": 2})
	if !slices.Equal(depIDs(blocker.Blocks), []int64{got.ID}) {
		t.Errorf("blocker.blocks = %v, want [%d]", blocker.Blocks, got.ID)
	}
	if blocker.IsBlocked {
		t.Error("the blocker itself waits on nothing; is_blocked should be false")
	}
}

func TestCreateSubtaskWithDependsOn(t *testing.T) {
	store := newFakeStore(task.Task{ID: 1, Title: "parent"})
	cs := dial(t, store, 1)

	first := callTool[taskOut](t, cs, "create_subtask", map[string]any{"title": "phase one"})
	second := callTool[taskOut](t, cs, "create_subtask", map[string]any{
		"title": "phase two", "depends_on": []int64{first.ID},
	})
	if second.ParentID == nil || *second.ParentID != 1 {
		t.Errorf("parent_id = %v, want 1", second.ParentID)
	}
	if !slices.Equal(depIDs(second.DependsOn), []int64{first.ID}) || !second.IsBlocked {
		t.Errorf("phase two = %+v, want it waiting on phase one #%d", second, first.ID)
	}
}

// A refused depends_on on create still leaves the task created; the
// error says so rather than letting the agent create it twice.
func TestCreateTaskBadDependsOnKeepsTask(t *testing.T) {
	store := newFakeStore(task.Task{ID: 1, Title: "bound"})
	cs := dial(t, store, 1)

	msg := callToolErr(t, cs, "create_task", map[string]any{"title": "x", "depends_on": []int64{999}})
	if len(store.tasks) != 2 {
		t.Errorf("store holds %d tasks, want 2: the task is created even when its dependencies are refused", len(store.tasks))
	}
	if !strings.Contains(msg, "was created") {
		t.Errorf("error = %q, want it to say the task was created", msg)
	}
}

func TestSetTaskDependenciesReplacesAndClears(t *testing.T) {
	store := newFakeStore(
		task.Task{ID: 1, Title: "bound"},
		task.Task{ID: 2, Title: "a"},
		task.Task{ID: 3, Title: "b"},
		task.Task{ID: 4, Title: "c"},
	)
	store.deps = []depEdge{{1, 2}}
	cs := dial(t, store, 1)

	got := callTool[taskOut](t, cs, "set_task_dependencies", map[string]any{"depends_on": []int64{3, 4}})
	if !slices.Equal(depIDs(got.DependsOn), []int64{3, 4}) {
		t.Errorf("depends_on = %v, want [3 4] replacing the old list", depIDs(got.DependsOn))
	}
	if got.ID != 1 {
		t.Errorf("set_task_dependencies acted on #%d, want the bound task #1", got.ID)
	}

	got = callTool[taskOut](t, cs, "set_task_dependencies", map[string]any{"depends_on": []int64{}})
	if len(got.DependsOn) != 0 || got.IsBlocked {
		t.Errorf("after clearing: depends_on = %v is_blocked = %v, want none and false", got.DependsOn, got.IsBlocked)
	}
}

func TestAddAndRemoveTaskDependency(t *testing.T) {
	store := newFakeStore(
		task.Task{ID: 1, Title: "bound"},
		task.Task{ID: 2, Title: "a"},
		task.Task{ID: 3, Title: "b"},
	)
	store.deps = []depEdge{{1, 2}}
	cs := dial(t, store, 1)

	got := callTool[taskOut](t, cs, "add_task_dependency", map[string]any{"depends_on": 3})
	if !slices.Equal(depIDs(got.DependsOn), []int64{2, 3}) {
		t.Errorf("after add: depends_on = %v, want [2 3] (the existing edge kept)", depIDs(got.DependsOn))
	}
	got = callTool[taskOut](t, cs, "remove_task_dependency", map[string]any{"depends_on": 2})
	if !slices.Equal(depIDs(got.DependsOn), []int64{3}) {
		t.Errorf("after remove: depends_on = %v, want [3]", depIDs(got.DependsOn))
	}
	// Removing what is not there is quiet, and the override reaches
	// another task.
	got = callTool[taskOut](t, cs, "remove_task_dependency", map[string]any{"depends_on": 2, "task_id": 3})
	if got.ID != 3 || len(got.DependsOn) != 0 {
		t.Errorf("remove on #3 = %+v, want #3 with no dependencies", got)
	}
}

func TestDependencyToolsRefuseSelfAndCycles(t *testing.T) {
	store := newFakeStore(
		task.Task{ID: 1, Title: "bound"},
		task.Task{ID: 2, Title: "a"},
		task.Task{ID: 3, Title: "b"},
	)
	store.deps = []depEdge{{1, 2}, {2, 3}}
	cs := dial(t, store, 1)

	// callToolErr fails the test unless the tool returns an error result.
	callToolErr(t, cs, "add_task_dependency", map[string]any{"depends_on": 1}) // self
	// 1 -> 2 -> 3, so 3 -> 1 closes the loop.
	callToolErr(t, cs, "add_task_dependency", map[string]any{"depends_on": 1, "task_id": 3})
	callToolErr(t, cs, "set_task_dependencies", map[string]any{"depends_on": []int64{1}, "task_id": 3})
	if len(store.deps) != 2 {
		t.Errorf("refused edges left the graph at %v, want the original two", store.deps)
	}
	if !errors.Is(store.checkDependency(3, 1), task.ErrDependencyCycle) {
		t.Error("fake's own cycle check disagrees with the tools; the test is not exercising the rule")
	}
}

func TestIsBlockedClearsWhenBlockerIsDone(t *testing.T) {
	store := newFakeStore(
		task.Task{ID: 1, Title: "bound"},
		task.Task{ID: 2, Title: "blocker", State: task.StateTodo},
	)
	store.deps = []depEdge{{1, 2}}
	cs := dial(t, store, 1)

	if got := callTool[taskOut](t, cs, "get_current_task", nil); !got.IsBlocked {
		t.Fatal("is_blocked = false with an open blocker, want true")
	}
	callTool[taskOut](t, cs, "set_task_state", map[string]any{"state": "done", "task_id": 2})
	got := callTool[taskOut](t, cs, "get_current_task", nil)
	if got.IsBlocked {
		t.Error("is_blocked = true after the blocker was done, want false")
	}
	// The edge itself stays: the history of what it waited on is kept.
	if !slices.Equal(depIDs(got.DependsOn), []int64{2}) || got.DependsOn[0].State != "done" {
		t.Errorf("depends_on = %+v, want the done blocker still listed", got.DependsOn)
	}
}

// list_subtasks renders each child through the same tail as get_task, so
// a sub-task's dependencies show there too.
func TestListSubtasksCarriesDependencies(t *testing.T) {
	parentID := int64(1)
	store := newFakeStore(
		task.Task{ID: 1, Title: "parent"},
		task.Task{ID: 2, Title: "one", ParentID: &parentID},
		task.Task{ID: 3, Title: "two", ParentID: &parentID},
	)
	store.deps = []depEdge{{3, 2}}
	cs := dial(t, store, 1)

	got := callTool[subtasksOut](t, cs, "list_subtasks", nil)
	var two *taskOut
	for i := range got.Tasks {
		if got.Tasks[i].ID == 3 {
			two = &got.Tasks[i]
		}
	}
	if two == nil || !slices.Equal(depIDs(two.DependsOn), []int64{2}) || !two.IsBlocked {
		t.Errorf("list_subtasks task #3 = %+v, want it waiting on #2", two)
	}
}
