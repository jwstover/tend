package store

import (
	"context"
	"fmt"
	"strings"

	"github.com/jwstover/tend/internal/store/gen"
	"github.com/jwstover/tend/internal/task"
)

// LinkBranch records that a task's work is on branch in the repo at repo.
// Idempotent: linking an existing link returns it. The branch is trimmed
// and the repo path cleaned; blank values are refused.
//
// TODO(owner): linking writes no task_events row; a new kind means a
// table rebuild, so v1 leaves the activity log alone.
func (s *Store) LinkBranch(ctx context.Context, taskID int64, repo, branch string) (task.BranchLink, error) {
	repo, err := task.NormalizeRepoPath(repo)
	if err != nil {
		return task.BranchLink{}, err
	}
	branch, err = task.NormalizeBranch(branch)
	if err != nil {
		return task.BranchLink{}, err
	}
	var link task.BranchLink
	err = s.inTx(ctx, func(q *gen.Queries) error {
		if _, err := q.GetTask(ctx, taskID); err != nil {
			return fmt.Errorf("loading task %d: %w", taskID, err)
		}
		if err := q.LinkBranch(ctx, gen.LinkBranchParams{TaskID: taskID, RepoPath: repo, Branch: branch}); err != nil {
			return fmt.Errorf("linking task %d to branch %q: %w", taskID, branch, err)
		}
		row, err := q.GetTaskBranch(ctx, gen.GetTaskBranchParams{TaskID: taskID, RepoPath: repo, Branch: branch})
		if err != nil {
			return fmt.Errorf("reading back branch link: %w", err)
		}
		link, err = branchToDomain(row)
		return err
	})
	return link, err
}

// UnlinkBranch removes a link. Removing one that is not there is not an
// error.
func (s *Store) UnlinkBranch(ctx context.Context, taskID int64, repo, branch string) error {
	repo, err := task.NormalizeRepoPath(repo)
	if err != nil {
		return err
	}
	branch, err = task.NormalizeBranch(branch)
	if err != nil {
		return err
	}
	err = s.q.UnlinkBranch(ctx, gen.UnlinkBranchParams{TaskID: taskID, RepoPath: repo, Branch: branch})
	if err != nil {
		return fmt.Errorf("unlinking task %d from branch %q: %w", taskID, branch, err)
	}
	return nil
}

// TaskBranches returns a task's branch links, oldest first.
func (s *Store) TaskBranches(ctx context.Context, taskID int64) ([]task.BranchLink, error) {
	rows, err := s.q.ListTaskBranches(ctx, taskID)
	if err != nil {
		return nil, fmt.Errorf("listing branches of task %d: %w", taskID, err)
	}
	out := make([]task.BranchLink, 0, len(rows))
	for _, row := range rows {
		l, err := branchToDomain(row)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, nil
}

// RelinkBranch points an existing link at a different branch, for the
// "branch is missing, re-link" flow. task.ErrBranchLinkGone if the id
// does not exist; task.ErrBranchLinkTaken if the task already links the
// new name in the same repo.
func (s *Store) RelinkBranch(ctx context.Context, id int64, newBranch string) error {
	newBranch, err := task.NormalizeBranch(newBranch)
	if err != nil {
		return err
	}
	n, err := s.q.RelinkBranch(ctx, gen.RelinkBranchParams{Branch: newBranch, ID: id})
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("relinking branch link %d to %q: %w", id, newBranch, task.ErrBranchLinkTaken)
		}
		return fmt.Errorf("relinking branch link %d: %w", id, err)
	}
	if n == 0 {
		return fmt.Errorf("branch link %d: %w", id, task.ErrBranchLinkGone)
	}
	return nil
}

func branchToDomain(row gen.TaskBranch) (task.BranchLink, error) {
	created, err := parseTime(row.CreatedAt)
	if err != nil {
		return task.BranchLink{}, fmt.Errorf("branch link %d created_at: %w", row.ID, err)
	}
	return task.BranchLink{
		ID: row.ID, TaskID: row.TaskID, RepoPath: row.RepoPath,
		Branch: row.Branch, CreatedAt: created,
	}, nil
}

// isUniqueViolation reports whether err is SQLite's UNIQUE constraint
// failure (modernc surfaces it only as text).
func isUniqueViolation(err error) bool {
	return strings.Contains(err.Error(), "UNIQUE constraint failed")
}
