package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jwstover/tend/internal/store/gen"
	"github.com/jwstover/tend/internal/task"
)

func TestProjectCRUD(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	p, err := s.CreateProject(ctx, "  tend  ")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if p.Name != "tend" {
		t.Errorf("Name = %q, want the trimmed %q", p.Name, "tend")
	}

	if _, err := s.CreateProject(ctx, "   "); !errors.Is(err, task.ErrEmptyProjectName) {
		t.Errorf("CreateProject(blank) = %v, want ErrEmptyProjectName", err)
	}
	// Unique is NOCASE, so this is the same project.
	if _, err := s.CreateProject(ctx, "TEND"); err == nil {
		t.Error("CreateProject with a case-variant duplicate should fail")
	}

	// ...and lookup is case-insensitive in the same way.
	found, err := s.ProjectByName(ctx, "TeNd")
	if err != nil {
		t.Fatalf("ProjectByName: %v", err)
	}
	if found.ID != p.ID {
		t.Errorf("ProjectByName resolved to %d, want %d", found.ID, p.ID)
	}
	if _, err := s.ProjectByName(ctx, "nope"); !errors.Is(err, task.ErrProjectNotFound) {
		t.Errorf("ProjectByName(unknown) = %v, want ErrProjectNotFound", err)
	}

	if err := s.RenameProject(ctx, p.ID, "tend-cli"); err != nil {
		t.Fatalf("RenameProject: %v", err)
	}
	got, err := s.GetProject(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if got.Name != "tend-cli" {
		t.Errorf("Name after rename = %q", got.Name)
	}

	if err := s.SetProjectArchived(ctx, p.ID, true); err != nil {
		t.Fatalf("SetProjectArchived: %v", err)
	}
	if got, _ = s.GetProject(ctx, p.ID); !got.Archived() {
		t.Error("project should read archived")
	}
	if err := s.SetProjectArchived(ctx, p.ID, false); err != nil {
		t.Fatalf("SetProjectArchived(false): %v", err)
	}
	if got, _ = s.GetProject(ctx, p.ID); got.Archived() {
		t.Error("project should read unarchived")
	}
}

// project_id carries no foreign key (migration 00007), so the store is
// what stops a delete from orphaning tasks. This is that guarantee.
func TestDeleteProjectReassignsItsTasks(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	p, err := s.CreateProject(ctx, "doomed")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	stranded, err := s.AddTaskIn(ctx, p.ID, "do not orphan me")
	if err != nil {
		t.Fatalf("AddTaskIn: %v", err)
	}

	if err := s.DeleteProject(ctx, p.ID); err != nil {
		t.Fatalf("DeleteProject: %v", err)
	}
	got, err := s.GetTask(ctx, stranded.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.ProjectID != task.DefaultProjectID {
		t.Errorf("ProjectID after its project was deleted = %d, want the default %d",
			got.ProjectID, task.DefaultProjectID)
	}
	if _, err := s.GetProject(ctx, p.ID); !errors.Is(err, task.ErrProjectNotFound) {
		t.Errorf("GetProject(deleted) = %v, want ErrProjectNotFound", err)
	}
}

func TestDeleteDefaultProjectRefused(t *testing.T) {
	s := newTestStore(t)
	if err := s.DeleteProject(context.Background(), task.DefaultProjectID); !errors.Is(err, task.ErrProtectedProject) {
		t.Errorf("DeleteProject(default) = %v, want ErrProtectedProject", err)
	}
}

func TestListProjectsCountsLiveTopLevelTasks(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	p, err := s.CreateProject(ctx, "counted")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	parent, err := s.AddTaskIn(ctx, p.ID, "top level")
	if err != nil {
		t.Fatalf("AddTaskIn: %v", err)
	}
	// A sub-task must not inflate the count: the number beside a project
	// is what selecting it puts on screen as rows.
	if _, err := s.AddChild(ctx, parent.ID, "sub"); err != nil {
		t.Fatalf("AddChild: %v", err)
	}
	// Nor should a completed one, which the live view filters out.
	done, err := s.AddTaskIn(ctx, p.ID, "finished")
	if err != nil {
		t.Fatalf("AddTaskIn: %v", err)
	}
	if err := s.SetState(ctx, done.ID, task.StateDone); err != nil {
		t.Fatalf("SetState: %v", err)
	}

	projects, err := s.ListProjects(ctx)
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	for _, got := range projects {
		if got.ID != p.ID {
			continue
		}
		if got.LiveCount != 1 {
			t.Errorf("LiveCount = %d, want 1 (top-level and live only)", got.LiveCount)
		}
		return
	}
	t.Fatalf("project %d missing from ListProjects", p.ID)
}

// The setting is the TUI's memory of where it was, not a capture target.
func TestActiveProjectRoundTripAndFallbacks(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Never set: the default, not an error.
	got, err := s.ActiveProjectID(ctx)
	if err != nil || got != task.DefaultProjectID {
		t.Fatalf("ActiveProjectID on a fresh db = (%d, %v), want the default", got, err)
	}

	p, err := s.CreateProject(ctx, "active")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if err := s.SetActiveProject(ctx, p.ID); err != nil {
		t.Fatalf("SetActiveProject: %v", err)
	}
	if got, _ = s.ActiveProjectID(ctx); got != p.ID {
		t.Errorf("ActiveProjectID = %d, want %d", got, p.ID)
	}

	// Deleting the remembered project must not strand the TUI's restore.
	if err := s.DeleteProject(ctx, p.ID); err != nil {
		t.Fatalf("DeleteProject: %v", err)
	}
	if got, err = s.ActiveProjectID(ctx); err != nil || got != task.DefaultProjectID {
		t.Errorf("ActiveProjectID after its project was deleted = (%d, %v), want the default", got, err)
	}

	// A corrupt stored value degrades the same way rather than failing: a
	// bad UI preference must not stop the TUI from starting.
	if err := s.q.SetSetting(ctx, gen.SetSettingParams{
		Key: settingActiveProject, Value: "not a number",
	}); err != nil {
		t.Fatalf("seeding a corrupt setting: %v", err)
	}
	if got, err = s.ActiveProjectID(ctx); err != nil || got != task.DefaultProjectID {
		t.Errorf("ActiveProjectID with a corrupt value = (%d, %v), want the default", got, err)
	}
}

func TestSetProjectMovesWholeSubtree(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	dest, err := s.CreateProject(ctx, "destination")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	root, err := s.AddTask(ctx, "root")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	child, err := s.AddChild(ctx, root.ID, "child")
	if err != nil {
		t.Fatalf("AddChild: %v", err)
	}
	// Three levels deep: the walk replaces a recursive CTE, so depth is
	// exactly what could regress.
	grandchild, err := s.AddChild(ctx, child.ID, "grandchild")
	if err != nil {
		t.Fatalf("AddChild: %v", err)
	}

	if err := s.SetProject(ctx, root.ID, dest.ID); err != nil {
		t.Fatalf("SetProject: %v", err)
	}
	for _, id := range []int64{root.ID, child.ID, grandchild.ID} {
		got, err := s.GetTask(ctx, id)
		if err != nil {
			t.Fatalf("GetTask(%d): %v", id, err)
		}
		if got.ProjectID != dest.ID {
			t.Errorf("task %d ProjectID = %d, want %d (the whole sub-tree moves)",
				id, got.ProjectID, dest.ID)
		}
	}
}

// A sub-task can never sit in a different project from its parent, so the
// insert copies the parent's project rather than defaulting.
func TestAddChildInheritsParentProject(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	p, err := s.CreateProject(ctx, "parent project")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	parent, err := s.AddTaskIn(ctx, p.ID, "parent")
	if err != nil {
		t.Fatalf("AddTaskIn: %v", err)
	}
	child, err := s.AddChild(ctx, parent.ID, "child")
	if err != nil {
		t.Fatalf("AddChild: %v", err)
	}
	if child.ProjectID != p.ID {
		t.Errorf("child ProjectID = %d, want the parent's %d", child.ProjectID, p.ID)
	}
}

func TestListLiveScopesToProject(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	p, err := s.CreateProject(ctx, "scoped")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if _, err := s.AddTaskIn(ctx, p.ID, "in scope"); err != nil {
		t.Fatalf("AddTaskIn: %v", err)
	}
	if _, err := s.AddTaskIn(ctx, task.DefaultProjectID, "out of scope"); err != nil {
		t.Fatalf("AddTaskIn: %v", err)
	}

	all, err := s.ListLive(ctx, nil)
	if err != nil {
		t.Fatalf("ListLive(nil): %v", err)
	}
	if len(all) != 2 {
		t.Errorf("ListLive(nil) returned %d tasks, want both", len(all))
	}

	scoped, err := s.ListLive(ctx, &p.ID)
	if err != nil {
		t.Fatalf("ListLive(scoped): %v", err)
	}
	if len(scoped) != 1 || scoped[0].Title != "in scope" {
		t.Errorf("ListLive(scoped) = %+v, want only the in-scope task", scoped)
	}

	n, err := s.CountInbox(ctx, &p.ID)
	if err != nil {
		t.Fatalf("CountInbox: %v", err)
	}
	if n != 1 {
		t.Errorf("CountInbox(scoped) = %d, want 1", n)
	}
}

// A bare capture never consults the remembered project. The shell has no
// hidden state steering it, so `tend add` behaves identically in every
// terminal regardless of what the TUI is looking at.
func TestAddTaskIgnoresTheRememberedProject(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	p, err := s.CreateProject(ctx, "elsewhere")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if err := s.SetActiveProject(ctx, p.ID); err != nil {
		t.Fatalf("SetActiveProject: %v", err)
	}

	captured, err := s.AddTask(ctx, "bare capture")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	if captured.ProjectID != task.DefaultProjectID {
		t.Errorf("AddTask landed in %d, want the default project %d",
			captured.ProjectID, task.DefaultProjectID)
	}

	withBody, err := s.AddTaskWithBody(ctx, "with a body", "context")
	if err != nil {
		t.Fatalf("AddTaskWithBody: %v", err)
	}
	if withBody.ProjectID != task.DefaultProjectID {
		t.Errorf("AddTaskWithBody landed in %d, want the default project %d",
			withBody.ProjectID, task.DefaultProjectID)
	}
}

// Moving a task logs exactly one event, not one per row in its sub-tree:
// the whole point of writing it in the store rather than in a trigger.
func TestSetProjectLogsOneEventForTheWholeSubtree(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	dest, err := s.CreateProject(ctx, "destination")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	root, err := s.AddTask(ctx, "root")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	child, err := s.AddChild(ctx, root.ID, "child")
	if err != nil {
		t.Fatalf("AddChild: %v", err)
	}
	if _, err := s.AddChild(ctx, child.ID, "grandchild"); err != nil {
		t.Fatalf("AddChild: %v", err)
	}

	before := projectEvents(t, s)
	if err := s.SetProject(ctx, root.ID, dest.ID); err != nil {
		t.Fatalf("SetProject: %v", err)
	}
	got := projectEvents(t, s)
	if len(got) != len(before)+1 {
		t.Fatalf("logged %d project events for one move, want 1", len(got)-len(before))
	}

	ev := got[len(got)-1]
	if ev.TaskID != root.ID {
		t.Errorf("event is for task %d, want the moved root %d", ev.TaskID, root.ID)
	}
	if ev.Old == nil || *ev.Old != "Unsorted" || ev.New == nil || *ev.New != "destination" {
		t.Errorf("event = (%v -> %v), want Unsorted -> destination", ev.Old, ev.New)
	}
}

// Moving a task to where it already is changes nothing and logs nothing,
// mirroring the state trigger's OLD <> NEW guard.
func TestSetProjectToTheSameProjectIsANoop(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	created, err := s.AddTask(ctx, "staying put")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	before := projectEvents(t, s)
	if err := s.SetProject(ctx, created.ID, task.DefaultProjectID); err != nil {
		t.Fatalf("SetProject: %v", err)
	}
	if got := projectEvents(t, s); len(got) != len(before) {
		t.Errorf("a no-op move logged %d event(s)", len(got)-len(before))
	}
}

// The event snapshots project names, so the log stays readable after the
// project it names is renamed or deleted -- the same reason task_events
// snapshots task_title.
func TestProjectEventSurvivesRenamingTheProject(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	dest, err := s.CreateProject(ctx, "original name")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	created, err := s.AddTask(ctx, "moved")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	if err := s.SetProject(ctx, created.ID, dest.ID); err != nil {
		t.Fatalf("SetProject: %v", err)
	}
	if err := s.RenameProject(ctx, dest.ID, "renamed later"); err != nil {
		t.Fatalf("RenameProject: %v", err)
	}

	got := projectEvents(t, s)
	last := got[len(got)-1]
	if last.New == nil || *last.New != "original name" {
		t.Errorf("event New = %v, want the name snapshotted at move time", last.New)
	}
}

// projectEvents returns every project-kind event in a wide window.
func projectEvents(t *testing.T, s *Store) []task.Event {
	t.Helper()
	events, err := s.ListEvents(context.Background(),
		time.Now().Add(-24*time.Hour), time.Now().Add(24*time.Hour))
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	var out []task.Event
	for _, ev := range events {
		if ev.Kind == task.EventProject {
			out = append(out, ev)
		}
	}
	return out
}
