package store

import (
	"context"
	"fmt"
	"slices"

	"github.com/jwstover/tend/internal/store/gen"
	"github.com/jwstover/tend/internal/task"
)

// AddDependency records that taskID waits on dependsOnID: the blocker
// must be done before the task can be worked on. Recording an edge that
// already exists is a no-op.
//
// Refused: a task waiting on itself (task.ErrSelfDependency), and an
// edge that would close a cycle (task.ErrDependencyCycle) -- if
// dependsOnID already waits, directly or through others, on taskID,
// neither could ever start. The check walks the whole edge list in Go
// for the same sqlc reason SetParent walks the tree itself. Both tasks
// are loaded first so a bad id reads as "loading task N" rather than a
// foreign-key failure.
func (s *Store) AddDependency(ctx context.Context, taskID, dependsOnID int64) error {
	if taskID == dependsOnID {
		return fmt.Errorf("task %d: %w", taskID, task.ErrSelfDependency)
	}
	return s.inTx(ctx, func(q *gen.Queries) error {
		if _, err := q.GetTask(ctx, taskID); err != nil {
			return fmt.Errorf("loading task %d: %w", taskID, err)
		}
		if _, err := q.GetTask(ctx, dependsOnID); err != nil {
			return fmt.Errorf("loading task %d: %w", dependsOnID, err)
		}
		reach, err := dependsOnSet(ctx, q, dependsOnID)
		if err != nil {
			return err
		}
		if reach[taskID] {
			return fmt.Errorf("task %d cannot depend on %d, which already waits on it: %w",
				taskID, dependsOnID, task.ErrDependencyCycle)
		}
		if err := q.AddDependency(ctx, gen.AddDependencyParams{TaskID: taskID, DependsOnID: dependsOnID}); err != nil {
			return fmt.Errorf("adding dependency %d -> %d: %w", taskID, dependsOnID, err)
		}
		return nil
	})
}

// RemoveDependency forgets that taskID waits on dependsOnID. Removing an
// edge that is not there is not an error.
func (s *Store) RemoveDependency(ctx context.Context, taskID, dependsOnID int64) error {
	err := s.q.RemoveDependency(ctx, gen.RemoveDependencyParams{TaskID: taskID, DependsOnID: dependsOnID})
	if err != nil {
		return fmt.Errorf("removing dependency %d -> %d: %w", taskID, dependsOnID, err)
	}
	return nil
}

// SetDependencies replaces everything a task waits on with the given
// ids; an empty list clears them. The wholesale form mirrors SetTags,
// for a caller that hands over the complete list every time (the MCP
// set_task_dependencies tool). Each id goes through the same checks as
// AddDependency, and a refused id rolls the whole replacement back, so
// the task never ends up with half a list.
func (s *Store) SetDependencies(ctx context.Context, taskID int64, dependsOn []int64) error {
	if slices.Contains(dependsOn, taskID) {
		return fmt.Errorf("task %d: %w", taskID, task.ErrSelfDependency)
	}
	return s.inTx(ctx, func(q *gen.Queries) error {
		if _, err := q.GetTask(ctx, taskID); err != nil {
			return fmt.Errorf("loading task %d: %w", taskID, err)
		}
		if err := q.ClearDependencies(ctx, taskID); err != nil {
			return fmt.Errorf("clearing dependencies of task %d: %w", taskID, err)
		}
		for _, id := range dependsOn {
			if _, err := q.GetTask(ctx, id); err != nil {
				return fmt.Errorf("loading task %d: %w", id, err)
			}
			reach, err := dependsOnSet(ctx, q, id)
			if err != nil {
				return err
			}
			if reach[taskID] {
				return fmt.Errorf("task %d cannot depend on %d, which already waits on it: %w",
					taskID, id, task.ErrDependencyCycle)
			}
			if err := q.AddDependency(ctx, gen.AddDependencyParams{TaskID: taskID, DependsOnID: id}); err != nil {
				return fmt.Errorf("adding dependency %d -> %d: %w", taskID, id, err)
			}
		}
		return nil
	})
}

// dependsOnSet returns every task that root waits on, directly or
// transitively, as a set. The seen set doubles as the result and as the
// guard against a cycle from some other source looping forever.
func dependsOnSet(ctx context.Context, q *gen.Queries, root int64) (map[int64]bool, error) {
	edges, err := q.ListAllDependencies(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing dependencies: %w", err)
	}
	next := make(map[int64][]int64, len(edges))
	for _, e := range edges {
		next[e.TaskID] = append(next[e.TaskID], e.DependsOnID)
	}
	seen := map[int64]bool{}
	queue := []int64{root}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		for _, d := range next[id] {
			if !seen[d] {
				seen[d] = true
				queue = append(queue, d)
			}
		}
	}
	return seen, nil
}

// Blockers returns the tasks a task waits on, oldest edge first -- the
// detail pane's BLOCKED BY section and the MCP depends_on list.
func (s *Store) Blockers(ctx context.Context, taskID int64) ([]task.Task, error) {
	rows, err := s.q.ListBlockers(ctx, taskID)
	if err != nil {
		return nil, fmt.Errorf("listing blockers of task %d: %w", taskID, err)
	}
	return toDomainSlice(rows)
}

// Blocking returns the tasks that wait on a task -- the reverse edge,
// for the detail pane's BLOCKS section.
func (s *Store) Blocking(ctx context.Context, taskID int64) ([]task.Task, error) {
	rows, err := s.q.ListBlocking(ctx, taskID)
	if err != nil {
		return nil, fmt.Errorf("listing tasks blocked by task %d: %w", taskID, err)
	}
	return toDomainSlice(rows)
}

// BlockerCounts returns, for every task that waits on anything, how many
// tasks it depends on and how many are still open, keyed by task id.
// One query for the whole list view, the ChildCounts idiom.
func (s *Store) BlockerCounts(ctx context.Context) (map[int64]task.BlockerCount, error) {
	rows, err := s.q.ListBlockerCounts(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing blocker counts: %w", err)
	}
	counts := make(map[int64]task.BlockerCount, len(rows))
	for _, r := range rows {
		counts[r.TaskID] = task.BlockerCount{Open: r.Open, Total: r.Total}
	}
	return counts, nil
}

// ListOpenTasks returns every task not yet done, across every project
// and at any depth, by project then id: the dependency picker's
// candidates. Unlike the parent picker's ListProjectTasks it is not
// scoped to a project, since a task may well wait on one elsewhere.
func (s *Store) ListOpenTasks(ctx context.Context) ([]task.Task, error) {
	rows, err := s.q.ListOpenTasks(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing open tasks: %w", err)
	}
	return toDomainSlice(rows)
}
