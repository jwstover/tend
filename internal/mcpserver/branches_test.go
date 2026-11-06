package mcpserver

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jwstover/tend/internal/task"
)

func (s *fakeStore) LinkBranch(_ context.Context, taskID int64, repo, branch string) (task.BranchLink, error) {
	repo, err := task.NormalizeRepoPath(repo)
	if err != nil {
		return task.BranchLink{}, err
	}
	branch, err = task.NormalizeBranch(branch)
	if err != nil {
		return task.BranchLink{}, err
	}
	for _, l := range s.branches {
		if l.TaskID == taskID && l.RepoPath == repo && l.Branch == branch {
			return l, nil
		}
	}
	l := task.BranchLink{ID: int64(len(s.branches) + 1), TaskID: taskID, RepoPath: repo, Branch: branch, CreatedAt: time.Now()}
	s.branches = append(s.branches, l)
	return l, nil
}

func (s *fakeStore) UnlinkBranch(_ context.Context, taskID int64, repo, branch string) error {
	var keep []task.BranchLink
	for _, l := range s.branches {
		if l.TaskID == taskID && l.RepoPath == repo && l.Branch == branch {
			continue
		}
		keep = append(keep, l)
	}
	s.branches = keep
	return nil
}

func (s *fakeStore) TaskBranches(_ context.Context, taskID int64) ([]task.BranchLink, error) {
	var out []task.BranchLink
	for _, l := range s.branches {
		if l.TaskID == taskID {
			out = append(out, l)
		}
	}
	return out, nil
}

func pinCwd(t *testing.T, dir string) {
	t.Helper()
	old := getwd
	getwd = func() (string, error) { return dir, nil }
	t.Cleanup(func() { getwd = old })
}

func TestBranchToolsAreListed(t *testing.T) {
	cs := dial(t, newFakeStore(task.Task{ID: 7, Title: "bound"}), 7)
	tools, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, tl := range tools.Tools {
		have[tl.Name] = true
	}
	for _, n := range []string{"link_branch", "unlink_branch", "list_task_branches"} {
		if !have[n] {
			t.Errorf("tool %s not registered", n)
		}
	}
}

func TestLinkBranchDefaultsToCwdAndIsIdempotent(t *testing.T) {
	pinCwd(t, "/work/repo/")
	cs := dial(t, newFakeStore(task.Task{ID: 7, Title: "bound"}), 7)

	for i := 0; i < 2; i++ {
		got := callTool[branchesOut](t, cs, "link_branch", map[string]any{"branch": "  task-7/x "})
		if got.TaskID != 7 || len(got.Branches) != 1 {
			t.Fatalf("link_branch #%d = %+v, want one link on task 7", i, got)
		}
		b := got.Branches[0]
		if b.RepoPath != "/work/repo" || b.Branch != "task-7/x" {
			t.Errorf("link = %+v, want /work/repo task-7/x", b)
		}
	}
}

func TestLinkBranchRelativeRepoPathAndTaskID(t *testing.T) {
	pinCwd(t, "/work")
	store := newFakeStore(task.Task{ID: 7, Title: "bound"}, task.Task{ID: 8, Title: "other"})
	cs := dial(t, store, 7)

	got := callTool[branchesOut](t, cs, "link_branch", map[string]any{"branch": "b", "repo_path": "sub/../repo", "task_id": 8})
	if got.TaskID != 8 || len(got.Branches) != 1 || got.Branches[0].RepoPath != filepath.Join("/work", "repo") {
		t.Errorf("got %+v, want task 8 linked in /work/repo", got)
	}
	if mine := callTool[branchesOut](t, cs, "list_task_branches", nil); len(mine.Branches) != 0 {
		t.Errorf("bound task has links %+v, want none", mine.Branches)
	}
}

func TestLinkBranchBlankIsToolError(t *testing.T) {
	pinCwd(t, "/work")
	cs := dial(t, newFakeStore(task.Task{ID: 7, Title: "bound"}), 7)
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "link_branch", Arguments: map[string]any{"branch": "  "}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("blank branch should be a tool error")
	}
	if !strings.Contains(res.Content[0].(*mcp.TextContent).Text, task.ErrEmptyBranch.Error()) {
		t.Errorf("error text = %+v", res.Content)
	}
}

func TestUnlinkBranch(t *testing.T) {
	pinCwd(t, "/work/repo")
	cs := dial(t, newFakeStore(task.Task{ID: 7, Title: "bound"}), 7)
	callTool[branchesOut](t, cs, "link_branch", map[string]any{"branch": "a"})
	callTool[branchesOut](t, cs, "link_branch", map[string]any{"branch": "b"})

	got := callTool[branchesOut](t, cs, "unlink_branch", map[string]any{"branch": "a"})
	if len(got.Branches) != 1 || got.Branches[0].Branch != "b" || got.Note != "" {
		t.Errorf("after unlink = %+v, want only b, no note", got)
	}
	got = callTool[branchesOut](t, cs, "unlink_branch", map[string]any{"branch": "a"})
	if len(got.Branches) != 1 || got.Note == "" {
		t.Errorf("unlinking an unlinked branch = %+v, want the list plus a note", got)
	}
}

func TestListTaskBranchesUnknownTask(t *testing.T) {
	cs := dial(t, newFakeStore(task.Task{ID: 7, Title: "bound"}), 7)
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "list_task_branches", Arguments: map[string]any{"task_id": 99}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Error("unknown task should be a tool error")
	}
}
