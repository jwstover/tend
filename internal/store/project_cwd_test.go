package store

import (
	"context"
	"testing"
)

// A project's default cwd round-trips through every read path the TUI and
// CLI use, is normalized on the way in, and a blank value clears it (tend
// task #190).
func TestProjectCwdRoundTripAndClear(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	p, err := s.CreateProject(ctx, "app")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if p.Cwd != "" {
		t.Errorf("a new project has Cwd %q, want unset", p.Cwd)
	}

	// Trailing whitespace and a redundant slash are typing noise, not
	// part of the path.
	if err := s.SetProjectCwd(ctx, p.ID, "  /tmp/app/  "); err != nil {
		t.Fatalf("SetProjectCwd: %v", err)
	}
	got, err := s.GetProject(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if got.Cwd != "/tmp/app" {
		t.Errorf("GetProject Cwd = %q, want the normalized /tmp/app", got.Cwd)
	}
	if byName, _ := s.ProjectByName(ctx, "app"); byName.Cwd != "/tmp/app" {
		t.Errorf("ProjectByName Cwd = %q, want /tmp/app", byName.Cwd)
	}

	projects, err := s.ListProjects(ctx)
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	found := false
	for _, lp := range projects {
		if lp.ID == p.ID {
			found = true
			if lp.Cwd != "/tmp/app" {
				t.Errorf("ListProjects Cwd = %q, want /tmp/app", lp.Cwd)
			}
		}
	}
	if !found {
		t.Fatalf("project %d missing from ListProjects", p.ID)
	}

	if err := s.SetProjectCwd(ctx, p.ID, "   "); err != nil {
		t.Fatalf("SetProjectCwd(blank): %v", err)
	}
	if got, _ = s.GetProject(ctx, p.ID); got.Cwd != "" {
		t.Errorf("Cwd after clearing = %q, want unset", got.Cwd)
	}
}

// The seeded default project predates the column; it must read as unset
// rather than fail, since every fresh database goes through this path.
func TestDefaultProjectHasNoCwd(t *testing.T) {
	s := newTestStore(t)
	p, err := s.GetProject(context.Background(), 1)
	if err != nil {
		t.Fatalf("GetProject(default): %v", err)
	}
	if p.Cwd != "" {
		t.Errorf("default project Cwd = %q, want unset", p.Cwd)
	}
}
