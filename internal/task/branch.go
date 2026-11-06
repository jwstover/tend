package task

import (
	"errors"
	"path/filepath"
	"strings"
	"time"
)

// Branch link errors.
var (
	ErrEmptyBranch     = errors.New("branch name is empty")
	ErrEmptyRepoPath   = errors.New("repository path is empty")
	ErrBranchLinkGone  = errors.New("branch link not found")
	ErrBranchLinkTaken = errors.New("task is already linked to that branch")
)

// BranchLink is an explicit link between a task and a branch its work is
// on. It is the only source the diff views trust; nothing infers a link
// from a branch name.
type BranchLink struct {
	ID        int64
	TaskID    int64
	RepoPath  string // absolute path to the repo (or worktree) the branch lives in
	Branch    string
	CreatedAt time.Time
}

// NormalizeBranch trims a branch name, refusing a blank one.
func NormalizeBranch(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", ErrEmptyBranch
	}
	return s, nil
}

// NormalizeRepoPath trims and cleans a repository path, refusing a blank
// one. It does not touch the filesystem.
func NormalizeRepoPath(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", ErrEmptyRepoPath
	}
	return filepath.Clean(s), nil
}
