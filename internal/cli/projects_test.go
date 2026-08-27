package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/jwstover/tend/internal/task"
)

func runProjects(t *testing.T, s *fakeStore, args ...string) (string, error) {
	t.Helper()
	return runOn(newProjectsCmd(func(context.Context) (Store, error) { return s, nil }), args)
}

func runLs(t *testing.T, s *fakeStore, args ...string) (string, error) {
	t.Helper()
	return runOn(newLsCmd(func(context.Context) (Store, error) { return s, nil }), args)
}

func runOn(cmd *cobra.Command, args []string) (string, error) {
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func TestProjectsListShowsCounts(t *testing.T) {
	ctx := context.Background()
	s := newFakeStore("tend", "hapi")
	if _, err := s.AddTask(ctx, "in unsorted"); err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	tend, _ := s.ProjectByName(ctx, "tend")
	if _, err := s.AddTaskIn(ctx, tend.ID, "in tend"); err != nil {
		t.Fatalf("AddTaskIn: %v", err)
	}

	out, err := runProjects(t, s)
	if err != nil {
		t.Fatalf("projects: %v", err)
	}
	for _, want := range []string{"Unsorted", "tend", "hapi"} {
		if !strings.Contains(out, want) {
			t.Errorf("listing missing %q:\n%s", want, out)
		}
	}
}

func TestProjectsAdd(t *testing.T) {
	ctx := context.Background()
	s := newFakeStore()

	if _, err := runProjects(t, s, "add", "gardening"); err != nil {
		t.Fatalf("projects add: %v", err)
	}
	if _, err := s.ProjectByName(ctx, "gardening"); err != nil {
		t.Fatalf("project was not created: %v", err)
	}
}

// The shell reads no stored project state: a bare capture always lands in
// the default project, whatever the TUI happens to be looking at.
func TestBareAddAlwaysLandsInTheDefaultProject(t *testing.T) {
	ctx := context.Background()
	s := newFakeStore("tend")
	tend, _ := s.ProjectByName(ctx, "tend")
	// Even with the TUI's remembered project pointing elsewhere.
	if err := s.SetActiveProject(ctx, tend.ID); err != nil {
		t.Fatalf("SetActiveProject: %v", err)
	}

	stdout, _, err := runAdd(t, s, "buy milk")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if s.tasks[0].ProjectID != task.DefaultProjectID {
		t.Errorf("captured into %d, want the default project %d",
			s.tasks[0].ProjectID, task.DefaultProjectID)
	}
	if !strings.Contains(stdout, "to Unsorted:") {
		t.Errorf("stdout = %q, want the destination named", stdout)
	}
}

// A name that doesn't resolve is an error with a hint, never a silently
// created project -- a typo must not spawn one.
func TestUnknownNameErrorsWithAHint(t *testing.T) {
	s := newFakeStore("tend")
	out, err := runProjects(t, s, "rename", "tned", "whatever")
	if err == nil {
		t.Fatal("renaming an unknown project should fail")
	}
	msg := err.Error() + out
	if !strings.Contains(msg, "tend projects add") {
		t.Errorf("error should hint at creating it: %q", msg)
	}
	if len(s.projects) != 2 {
		t.Errorf("a failed lookup created a project: %+v", s.projects)
	}
}

// Deleting a project must never delete work.
func TestProjectsRmKeepsItsTasks(t *testing.T) {
	ctx := context.Background()
	s := newFakeStore("doomed")
	doomed, _ := s.ProjectByName(ctx, "doomed")
	stranded, err := s.AddTaskIn(ctx, doomed.ID, "keep me")
	if err != nil {
		t.Fatalf("AddTaskIn: %v", err)
	}

	out, err := runProjects(t, s, "rm", "doomed")
	if err != nil {
		t.Fatalf("projects rm: %v", err)
	}
	if !strings.Contains(out, "moved to Unsorted") {
		t.Errorf("rm should say where the tasks went: %q", out)
	}
	if _, err := s.ProjectByName(ctx, "doomed"); err == nil {
		t.Error("project should be gone")
	}
	for _, tk := range s.tasks {
		if tk.ID == stranded.ID && tk.ProjectID != task.DefaultProjectID {
			t.Errorf("task landed in %d, want the default project", tk.ProjectID)
		}
	}
}

func TestProjectsRenameAndArchive(t *testing.T) {
	ctx := context.Background()
	s := newFakeStore("alpha")

	if _, err := runProjects(t, s, "rename", "alpha", "omega"); err != nil {
		t.Fatalf("projects rename: %v", err)
	}
	if _, err := s.ProjectByName(ctx, "omega"); err != nil {
		t.Errorf("rename did not take: %v", err)
	}

	if _, err := runProjects(t, s, "archive", "omega"); err != nil {
		t.Fatalf("projects archive: %v", err)
	}
	p, _ := s.ProjectByName(ctx, "omega")
	if !p.Archived() {
		t.Error("project should read archived")
	}
	if _, err := runProjects(t, s, "unarchive", "omega"); err != nil {
		t.Fatalf("projects unarchive: %v", err)
	}
	p, _ = s.ProjectByName(ctx, "omega")
	if p.Archived() {
		t.Error("project should read unarchived")
	}
}

// The default project is the reassignment target and the capture
// fallback; deleting it would strand every path that leans on it.
func TestProjectsRmDefaultRefused(t *testing.T) {
	s := newFakeStore()
	if _, err := runProjects(t, s, "rm", "Unsorted"); err == nil {
		t.Fatal("deleting the default project should fail")
	}
}

// A project name can be typed unquoted, the way `tend add` takes a title.
func TestProjectNameJoinsUnquotedArgs(t *testing.T) {
	ctx := context.Background()
	s := newFakeStore()
	if _, err := runProjects(t, s, "add", "customer", "onboarding"); err != nil {
		t.Fatalf("projects add: %v", err)
	}
	if _, err := s.ProjectByName(ctx, "customer onboarding"); err != nil {
		t.Errorf("unquoted name did not join: %v", err)
	}
}

// `tend ls` dumps every project by default; --project narrows it.
func TestLsShowsEveryProjectUntilNarrowed(t *testing.T) {
	ctx := context.Background()
	s := newFakeStore("tend")
	tend, _ := s.ProjectByName(ctx, "tend")
	if _, err := s.AddTask(ctx, "in unsorted"); err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	if _, err := s.AddTaskIn(ctx, tend.ID, "in tend"); err != nil {
		t.Fatalf("AddTaskIn: %v", err)
	}

	// Pointing the TUI's remembered project elsewhere must not change what
	// the shell prints.
	if err := s.SetActiveProject(ctx, tend.ID); err != nil {
		t.Fatalf("SetActiveProject: %v", err)
	}

	out, err := runLs(t, s)
	if err != nil {
		t.Fatalf("ls: %v", err)
	}
	if !strings.Contains(out, "in unsorted") || !strings.Contains(out, "in tend") {
		t.Errorf("ls should show every project by default:\n%s", out)
	}

	if out, err = runLs(t, s, "--project", "tend"); err != nil {
		t.Fatalf("ls --project: %v", err)
	}
	if strings.Contains(out, "in unsorted") || !strings.Contains(out, "in tend") {
		t.Errorf("ls --project should scope to the named one:\n%s", out)
	}
}

func TestLsUnknownProjectErrors(t *testing.T) {
	s := newFakeStore("tend")
	if _, err := runLs(t, s, "--project", "nope"); err == nil {
		t.Fatal("ls with an unknown project should fail")
	}
}

// -p captures into a named project without changing where later captures
// land: it is an override for one invocation, not `projects use`.
func TestAddProjectFlagIsAOneShotOverride(t *testing.T) {
	ctx := context.Background()
	s := newFakeStore("tend")
	tend, _ := s.ProjectByName(ctx, "tend")

	stdout, _, err := runAdd(t, s, "-p", "tend", "ship it")
	if err != nil {
		t.Fatalf("add -p: %v", err)
	}
	if !strings.Contains(stdout, "to tend:") {
		t.Errorf("stdout = %q, want the named project echoed", stdout)
	}
	if s.tasks[0].ProjectID != tend.ID {
		t.Errorf("captured into %d, want tend %d", s.tasks[0].ProjectID, tend.ID)
	}

	// A later bare capture is unaffected: -p is one invocation, not a mode.
	if _, _, err := runAdd(t, s, "later"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if s.tasks[1].ProjectID != task.DefaultProjectID {
		t.Errorf("the next bare capture went to %d; -p should be a one-shot",
			s.tasks[1].ProjectID)
	}
}

// An unknown -p fails the whole command rather than capturing some of the
// lines and erroring on the rest.
func TestAddUnknownProjectCapturesNothing(t *testing.T) {
	s := newFakeStore("tend")
	if _, _, err := runAdd(t, s, "-p", "tned", "one"); err == nil {
		t.Fatal("add with an unknown project should fail")
	}
	if len(s.tasks) != 0 {
		t.Errorf("captured %d tasks despite the bad project", len(s.tasks))
	}
}
