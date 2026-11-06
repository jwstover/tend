package tui

import (
	"context"

	"github.com/jwstover/tend/internal/vcs"
)

// VCS is the slice of internal/vcs the TUI needs: the branch link picker
// (linkpicker.go) and the diff views that build on it. Declared here, with
// the signatures of *vcs.Client, so tests can swap in a fake.
type VCS interface {
	IsGitButler(ctx context.Context, repo string) bool
	Branches(ctx context.Context, repo string) ([]vcs.Branch, error)
	BranchExists(ctx context.Context, repo, branch string) bool
	Base(ctx context.Context, repo, branch string) (string, error)
	CommittedDiff(ctx context.Context, repo, branch string) (vcs.Diff, error)
	UncommittedDiff(ctx context.Context, repo, branch string) (vcs.Diff, error)
}
