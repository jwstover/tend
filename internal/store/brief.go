package store

import (
	"context"

	"github.com/jwstover/tend/internal/task"
)

// SessionBrief gathers what task.SessionSystemPrompt renders for a
// session bound to taskID: the task, its project, tags, parent, children,
// blockers both ways and its log. Only the task itself is required; any
// other piece that fails to load is left out, since the brief is a
// convenience on a launch, never a condition of it. Shared by the TUI's
// launches and `tend session prepare`.
func (s *Store) SessionBrief(ctx context.Context, taskID int64) (task.SessionBrief, error) {
	t, err := s.GetTask(ctx, taskID)
	if err != nil {
		return task.SessionBrief{}, err
	}
	b := task.SessionBrief{Task: t}
	if projects, err := s.ListProjects(ctx); err == nil {
		for _, p := range projects {
			if p.ID == t.ProjectID {
				b.Project = p
				break
			}
		}
	}
	b.Tags, _ = s.TagsForTask(ctx, taskID)
	if t.ParentID != nil {
		if p, err := s.GetTask(ctx, *t.ParentID); err == nil {
			b.Parent = &p
		}
	}
	b.Children, _ = s.ListChildren(ctx, taskID)
	if len(b.Children) > 0 {
		b.ChildBlockers, _ = s.BlockerCounts(ctx)
	}
	b.Blockers, _ = s.Blockers(ctx, taskID)
	b.Blocking, _ = s.Blocking(ctx, taskID)
	b.Log, _ = s.ListTaskLog(ctx, taskID)
	return b, nil
}
