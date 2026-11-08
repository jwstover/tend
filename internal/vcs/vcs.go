// Package vcs is the only package that runs git or but. It turns a linked
// branch into a structured Diff. Both binaries are optional: a missing one
// surfaces as ErrNoGit / ErrNoBut, which callers show quietly.
//
// Only read-only commands are ever built here: never `but push` or any write.
package vcs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// Errors callers tell apart and show quietly.
var (
	ErrNoGit          = errors.New("git is not installed")
	ErrNoBut          = errors.New("but (GitButler) is not installed")
	ErrBranchNotFound = errors.New("branch not found")
	ErrNotARepo       = errors.New("not a git repository")
)

// Runner runs one command in dir and returns its stdout. It is the seam
// tests replace; the default runs the real binary.
type Runner func(ctx context.Context, dir, name string, args ...string) ([]byte, error)

// Client runs commands through a Runner.
type Client struct{ Run Runner }

// New returns a Client that execs the real binaries.
func New() *Client { return &Client{Run: execRun} }

func execRun(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return nil, fmt.Errorf("%s: %w", name, notFound(name))
		}
		return nil, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

func notFound(name string) error {
	if name == "but" {
		return ErrNoBut
	}
	return ErrNoGit
}

// Branch is a branch the link picker can offer.
type Branch struct {
	Name        string
	Commits     int  // commits on the branch (GitButler only)
	Uncommitted bool // has assigned uncommitted changes (GitButler only)
}

type butStatus struct {
	MergeBase struct {
		CommitID string `json:"commitId"`
	} `json:"mergeBase"`
	Stacks []struct {
		AssignedChanges []struct {
			CliID string `json:"cliId"`
		} `json:"assignedChanges"`
		Branches []struct {
			Name    string            `json:"name"`
			Commits []json.RawMessage `json:"commits"`
		} `json:"branches"`
	} `json:"stacks"`
}

func (c *Client) git(ctx context.Context, repo string, args ...string) ([]byte, error) {
	out, err := c.Run(ctx, repo, "git", args...)
	if err != nil && !errors.Is(err, ErrNoGit) && strings.Contains(err.Error(), "not a git repository") {
		return nil, fmt.Errorf("%s: %w", repo, ErrNotARepo)
	}
	return out, err
}

func (c *Client) butStatus(ctx context.Context, repo string) (butStatus, error) {
	out, err := c.Run(ctx, repo, "but", "status", "--json")
	if err != nil {
		return butStatus{}, err
	}
	var st butStatus
	if err := json.Unmarshal(out, &st); err != nil {
		return butStatus{}, fmt.Errorf("decoding but status: %w", err)
	}
	return st, nil
}

// IsGitButler reports whether repo is in GitButler mode: on the
// gitbutler/workspace branch with a working `but status --json`.
func (c *Client) IsGitButler(ctx context.Context, repo string) bool {
	out, err := c.git(ctx, repo, "symbolic-ref", "--short", "HEAD")
	if err != nil || strings.TrimSpace(string(out)) != "gitbutler/workspace" {
		return false
	}
	_, err = c.butStatus(ctx, repo)
	return err == nil
}

// Branches lists the branches a task can be linked to: applied branches in
// GitButler mode, local branches otherwise.
func (c *Client) Branches(ctx context.Context, repo string) ([]Branch, error) {
	if c.IsGitButler(ctx, repo) {
		st, err := c.butStatus(ctx, repo)
		if err != nil {
			return nil, err
		}
		var bs []Branch
		for _, s := range st.Stacks {
			for i, b := range s.Branches {
				// Assigned changes belong to the stack's top (first) branch.
				bs = append(bs, Branch{Name: b.Name, Commits: len(b.Commits), Uncommitted: i == 0 && len(s.AssignedChanges) > 0})
			}
		}
		return bs, nil
	}
	out, err := c.git(ctx, repo, "for-each-ref", "--format=%(refname:short)", "refs/heads")
	if err != nil {
		return nil, err
	}
	var bs []Branch
	for _, n := range strings.Fields(string(out)) {
		bs = append(bs, Branch{Name: n})
	}
	return bs, nil
}

// BranchExists reports whether branch can be diffed in repo.
func (c *Client) BranchExists(ctx context.Context, repo, branch string) bool {
	_, err := c.git(ctx, repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	return err == nil
}

// Base is the ref a branch is diffed against, the first of:
//   - GitButler's target merge base, in GitButler mode;
//   - the branch's upstream, when it tracks a differently named branch (an
//     integration branch it is stacked on), not its own push target;
//   - origin/HEAD, when it resolves and is not a GitButler workspace ref (a
//     clone of a repo someone pushed gitbutler/workspace to can have its
//     origin/HEAD dangling at origin/gitbutler/workspace);
//   - origin/main, origin/master, main, master, whichever resolves first.
//
// Every candidate is verified to resolve, so a stale ref falls through
// rather than failing the diff.
func (c *Client) Base(ctx context.Context, repo, branch string) (string, error) {
	if c.IsGitButler(ctx, repo) {
		if st, err := c.butStatus(ctx, repo); err == nil && st.MergeBase.CommitID != "" {
			return st.MergeBase.CommitID, nil
		}
	}
	resolves := func(ref string) bool {
		_, err := c.git(ctx, repo, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
		return err == nil
	}
	if out, err := c.git(ctx, repo, "rev-parse", "--abbrev-ref", "--symbolic-full-name", branch+"@{upstream}"); err == nil {
		up := strings.TrimSpace(string(out))
		_, name, _ := strings.Cut(up, "/")
		if up != "" && name != branch && !isWorkspaceRef(up) && resolves(up) {
			return up, nil
		}
	}
	if out, err := c.git(ctx, repo, "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); err == nil {
		if b := strings.TrimSpace(string(out)); b != "" && !isWorkspaceRef(b) && resolves(b) {
			return b, nil
		}
	}
	for _, b := range []string{"origin/main", "origin/master", "main", "master"} {
		if resolves(b) {
			return b, nil
		}
	}
	return "", fmt.Errorf("no default branch in %s: %w", repo, ErrBranchNotFound)
}

// isWorkspaceRef reports whether ref names GitButler's workspace branch,
// locally or on a remote: never a meaningful base.
func isWorkspaceRef(ref string) bool {
	return ref == "gitbutler/workspace" || strings.HasSuffix(ref, "/gitbutler/workspace")
}

// CommittedDiff is `git diff <base>...<branch>`.
func (c *Client) CommittedDiff(ctx context.Context, repo, branch string) (Diff, error) {
	if !c.BranchExists(ctx, repo, branch) {
		return Diff{}, fmt.Errorf("%s: %w", branch, ErrBranchNotFound)
	}
	base, err := c.Base(ctx, repo, branch)
	if err != nil {
		return Diff{}, err
	}
	out, err := c.git(ctx, repo, "diff", "--no-color", "--no-ext-diff", "-U3", "-M", base+"..."+branch)
	if err != nil {
		return Diff{}, err
	}
	return ParseUnified(string(out))
}

// UncommittedDiff is the changes assigned to branch in GitButler. The CLI
// ids are looked up by branch name on every call, since they are
// short-lived. Outside GitButler it is an empty diff.
func (c *Client) UncommittedDiff(ctx context.Context, repo, branch string) (Diff, error) {
	if !c.IsGitButler(ctx, repo) {
		return Diff{}, nil
	}
	st, err := c.butStatus(ctx, repo)
	if err != nil {
		return Diff{}, err
	}
	var d Diff
	for _, s := range st.Stacks {
		if len(s.Branches) == 0 || s.Branches[0].Name != branch {
			continue
		}
		for _, ch := range s.AssignedChanges {
			out, err := c.Run(ctx, repo, "but", "diff", ch.CliID, "--json")
			if err != nil {
				return Diff{}, err
			}
			fd, err := ParseButDiff(out)
			if err != nil {
				return Diff{}, err
			}
			d.Files = append(d.Files, fd.Files...)
		}
		return d, nil
	}
	for _, s := range st.Stacks {
		for _, b := range s.Branches {
			if b.Name == branch {
				return Diff{}, nil // applied, but not the stack's top: no assigned changes
			}
		}
	}
	return Diff{}, fmt.Errorf("%s: %w", branch, ErrBranchNotFound)
}
