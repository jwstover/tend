package store

import (
	"context"
	"fmt"

	"github.com/jwstover/tend/internal/store/gen"
	"github.com/jwstover/tend/internal/task"
)

// SetTags replaces a task's tags wholesale. The prompt that feeds it hands
// over the complete list every time, so clear-then-attach is the honest
// operation; both halves plus the orphan sweep run in one transaction.
//
// Unknown tag names are created on the fly — tags are implicit, unlike
// projects, which is why there is no CreateTag.
func (s *Store) SetTags(ctx context.Context, taskID int64, tags []string) error {
	return s.inTx(ctx, func(q *gen.Queries) error {
		if err := q.ClearTaskTags(ctx, taskID); err != nil {
			return fmt.Errorf("clearing tags on task %d: %w", taskID, err)
		}
		for _, name := range tags {
			n, ok := task.NormalizeTag(name)
			if !ok {
				continue
			}
			row, err := q.UpsertTag(ctx, n)
			if err != nil {
				return fmt.Errorf("upserting tag %q: %w", n, err)
			}
			if err := q.AttachTag(ctx, gen.AttachTagParams{TaskID: taskID, TagID: row.ID}); err != nil {
				return fmt.Errorf("attaching tag %q to task %d: %w", n, taskID, err)
			}
		}
		// A tag exists because a task carries it; dropping the last
		// reference drops the tag, so the tag list can't collect ghosts.
		if err := q.DeleteOrphanTags(ctx); err != nil {
			return fmt.Errorf("sweeping orphan tags: %w", err)
		}
		return nil
	})
}

// TagsForTask returns one task's tags, alphabetically. For the detail pane
// and the MCP surface; the list view uses TagsByTask instead.
func (s *Store) TagsForTask(ctx context.Context, taskID int64) ([]string, error) {
	names, err := s.q.ListTagsForTask(ctx, taskID)
	if err != nil {
		return nil, fmt.Errorf("listing tags for task %d: %w", taskID, err)
	}
	return names, nil
}

// TagsByTask returns every task's tags keyed by task id. One query for the
// whole list view rather than N+1 per-row lookups — the same batch idiom
// as ChildCounts and SessionStatuses.
func (s *Store) TagsByTask(ctx context.Context) (map[int64][]string, error) {
	rows, err := s.q.ListAllTaskTags(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing task tags: %w", err)
	}
	out := make(map[int64][]string)
	for _, r := range rows {
		out[r.TaskID] = append(out[r.TaskID], r.Name)
	}
	return out, nil
}

// ListTags returns every tag name in use, alphabetically.
func (s *Store) ListTags(ctx context.Context) ([]string, error) {
	rows, err := s.q.ListTags(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing tags: %w", err)
	}
	names := make([]string, 0, len(rows))
	for _, r := range rows {
		names = append(names, r.Name)
	}
	return names, nil
}
