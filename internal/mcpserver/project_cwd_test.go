package mcpserver

import (
	"testing"

	"github.com/jwstover/tend/internal/task"
)

// A session can learn its project's default working directory, and a
// project without one reports no cwd at all rather than an empty string.
func TestGetCurrentProjectReportsCwd(t *testing.T) {
	store := newFakeStore(task.Task{ID: 1, Title: "bound", ProjectID: 2})
	store.projects = append(store.projects, task.Project{ID: 2, Name: "app", Cwd: "/tmp/app"})
	cs := dial(t, store, 1)

	got := callTool[projectOut](t, cs, "get_current_project", map[string]any{})
	if got.Cwd != "/tmp/app" {
		t.Errorf("get_current_project cwd = %q, want /tmp/app", got.Cwd)
	}

	all := callTool[projectsOut](t, cs, "list_projects", map[string]any{})
	for _, p := range all.Projects {
		switch p.Name {
		case "app":
			if p.Cwd != "/tmp/app" {
				t.Errorf("list_projects app cwd = %q, want /tmp/app", p.Cwd)
			}
		case "Unsorted":
			if p.Cwd != "" {
				t.Errorf("list_projects Unsorted cwd = %q, want unset", p.Cwd)
			}
		}
	}
}
