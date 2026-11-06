package mcpserver

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jwstover/tend/internal/task"
)

// The branch-link surface (tend task #793): agents create most branches,
// so they record the task <-> branch link themselves. Registered for every
// session, defaulting to the bound task. Like the workflow tools, every
// call returns the task's whole link list so the agent sees the effect of
// the change without a second call. The tools store what they are told:
// nothing here asks git whether the branch exists (mcpserver must not
// import internal/vcs; the UI shows a missing branch).

// getwd is the directory a link's repo_path defaults to, and the base a
// relative repo_path resolves against. `tend mcp` runs in claude's own
// cwd, so this is the session's working directory. A var so tests can pin
// it.
var getwd = os.Getwd

// branchOut is one task_branches row as the tools show it.
type branchOut struct {
	ID        int64  `json:"id"`
	RepoPath  string `json:"repo_path"`
	Branch    string `json:"branch"`
	CreatedAt string `json:"created_at"`
}

// branchesOut is the answer of every branch tool: the task's full link
// list, oldest first, and a note when there is something to say (an
// unlink of a link that was not there).
type branchesOut struct {
	TaskID   int64       `json:"task_id"`
	Branches []branchOut `json:"branches"`
	Note     string      `json:"note,omitempty"`
}

// branchArgs are the arguments link_branch and unlink_branch share.
type branchArgs struct {
	Branch   string `json:"branch" jsonschema:"the branch name, e.g. task-790/feature (a git branch or a GitButler branch)"`
	RepoPath string `json:"repo_path,omitempty" jsonschema:"absolute path of the repo or worktree the branch lives in; defaults to this session's working directory"`
	TaskID   *int64 `json:"task_id,omitempty" jsonschema:"task to link; defaults to the current task"`
}

// resolveRepoPath turns the optional repo_path into an absolute, cleaned
// path: blank means the session's cwd, relative means relative to it.
func resolveRepoPath(in string) (string, error) {
	if in == "" {
		wd, err := getwd()
		if err != nil {
			return "", fmt.Errorf("repo_path not given and the working directory is unknown: %w", err)
		}
		return filepath.Clean(wd), nil
	}
	if filepath.IsAbs(in) {
		return filepath.Clean(in), nil
	}
	wd, err := getwd()
	if err != nil {
		return "", fmt.Errorf("resolving relative repo_path %q: %w", in, err)
	}
	return filepath.Join(wd, in), nil
}

func listBranches(ctx context.Context, store Store, taskID int64) (branchesOut, error) {
	links, err := store.TaskBranches(ctx, taskID)
	if err != nil {
		return branchesOut{}, err
	}
	out := branchesOut{TaskID: taskID, Branches: make([]branchOut, 0, len(links))}
	for _, l := range links {
		out.Branches = append(out.Branches, branchOut{
			ID: l.ID, RepoPath: l.RepoPath, Branch: l.Branch, CreatedAt: fmtTime(l.CreatedAt),
		})
	}
	return out, nil
}

// registerBranchTools wires the branch-link tools onto srv. taskID is the
// session's bound task, the default for all three.
func registerBranchTools(srv *mcp.Server, store Store, taskID int64) {
	resolveTask := func(ctx context.Context, in *int64) (int64, error) {
		id := taskID
		if in != nil {
			id = *in
		}
		// Checked first so an unknown task reads as "not found".
		if _, err := store.GetTask(ctx, id); err != nil {
			return 0, err
		}
		return id, nil
	}

	mcp.AddTool(srv, &mcp.Tool{
		Name: "link_branch",
		Description: "Record that a task's work is on a branch (a git branch, or a GitButler branch " +
			"made with `but branch new`), so tend's diff view can show it. Call it right after " +
			"creating the branch. repo_path defaults to this session's working directory; pass it " +
			"when the branch lives in another repo or worktree. Idempotent. Defaults to the current " +
			"task. Returns the task's full list of linked branches. tend does not check the branch exists.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in branchArgs) (*mcp.CallToolResult, branchesOut, error) {
		id, err := resolveTask(ctx, in.TaskID)
		if err != nil {
			return nil, branchesOut{}, err
		}
		repo, err := resolveRepoPath(in.RepoPath)
		if err != nil {
			return nil, branchesOut{}, err
		}
		if _, err := store.LinkBranch(ctx, id, repo, in.Branch); err != nil {
			return nil, branchesOut{}, err
		}
		out, err := listBranches(ctx, store, id)
		return nil, out, err
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name: "unlink_branch",
		Description: "Remove a task's link to a branch (the branch itself is untouched). repo_path " +
			"defaults to this session's working directory, as in link_branch. Unlinking something " +
			"that is not linked is not an error: the result carries a note. Returns the task's " +
			"full list of linked branches.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in branchArgs) (*mcp.CallToolResult, branchesOut, error) {
		id, err := resolveTask(ctx, in.TaskID)
		if err != nil {
			return nil, branchesOut{}, err
		}
		repo, err := resolveRepoPath(in.RepoPath)
		if err != nil {
			return nil, branchesOut{}, err
		}
		branch, err := task.NormalizeBranch(in.Branch)
		if err != nil {
			return nil, branchesOut{}, err
		}
		before, err := store.TaskBranches(ctx, id)
		if err != nil {
			return nil, branchesOut{}, err
		}
		linked := false
		for _, l := range before {
			if l.RepoPath == repo && l.Branch == branch {
				linked = true
			}
		}
		if err := store.UnlinkBranch(ctx, id, repo, branch); err != nil {
			return nil, branchesOut{}, err
		}
		out, err := listBranches(ctx, store, id)
		if err != nil {
			return nil, branchesOut{}, err
		}
		if !linked {
			out.Note = fmt.Sprintf("task %d was not linked to branch %q in %s; nothing removed", id, branch, repo)
		}
		return nil, out, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name: "list_task_branches",
		Description: "List the branches linked to a task, oldest first: id, repo_path, branch. " +
			"Defaults to the current task.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in struct {
		TaskID *int64 `json:"task_id,omitempty" jsonschema:"task whose branches to list; defaults to the current task"`
	}) (*mcp.CallToolResult, branchesOut, error) {
		id, err := resolveTask(ctx, in.TaskID)
		if err != nil {
			return nil, branchesOut{}, err
		}
		out, err := listBranches(ctx, store, id)
		return nil, out, err
	})
}
