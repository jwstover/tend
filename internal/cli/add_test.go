package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/jwstover/tend/internal/task"
)

// fakeStore records captured tasks and session status writes. Its project
// surface is a real (if tiny) implementation rather than stubs: the
// project commands are mostly about resolving names and moving the
// capture target, so stubbing that out would leave the tests asserting
// nothing.
type fakeStore struct {
	tasks     []task.Task
	tags      map[int64][]string
	projects  []task.Project
	active    int64 // project new captures land in; 0 means the default
	statuses  map[string]task.SessionStatus
	statusErr error
}

// newFakeStore seeds the default project, the way migration 00007 does.
func newFakeStore(names ...string) *fakeStore {
	f := &fakeStore{projects: []task.Project{{ID: task.DefaultProjectID, Name: "Unsorted"}}}
	for _, n := range names {
		if _, err := f.CreateProject(context.Background(), n); err != nil {
			panic(err)
		}
	}
	return f
}

func (f *fakeStore) CreateProject(_ context.Context, name string) (task.Project, error) {
	n, err := task.NormalizeProjectName(name)
	if err != nil {
		return task.Project{}, err
	}
	if _, err := f.find(n); err == nil {
		return task.Project{}, errors.New("project already exists")
	}
	p := task.Project{ID: int64(len(f.projects) + 1), Name: n}
	f.projects = append(f.projects, p)
	return p, nil
}

func (f *fakeStore) find(name string) (task.Project, error) {
	for _, p := range f.projects {
		if strings.EqualFold(p.Name, name) { // the schema's NOCASE collation
			return p, nil
		}
	}
	return task.Project{}, task.ErrProjectNotFound
}

func (f *fakeStore) ProjectByName(_ context.Context, name string) (task.Project, error) {
	n, err := task.NormalizeProjectName(name)
	if err != nil {
		return task.Project{}, err
	}
	return f.find(n)
}

func (f *fakeStore) ListProjects(context.Context) ([]task.Project, error) {
	out := make([]task.Project, len(f.projects))
	copy(out, f.projects)
	for i := range out {
		for _, t := range f.tasks {
			if t.ProjectID == out[i].ID {
				out[i].LiveCount++
			}
		}
	}
	return out, nil
}

func (f *fakeStore) RenameProject(_ context.Context, id int64, name string) error {
	n, err := task.NormalizeProjectName(name)
	if err != nil {
		return err
	}
	for i := range f.projects {
		if f.projects[i].ID == id {
			f.projects[i].Name = n
			return nil
		}
	}
	return task.ErrProjectNotFound
}

func (f *fakeStore) SetProjectArchived(_ context.Context, id int64, archived bool) error {
	for i := range f.projects {
		if f.projects[i].ID == id {
			if archived {
				now := time.Now()
				f.projects[i].ArchivedAt = &now
			} else {
				f.projects[i].ArchivedAt = nil
			}
			return nil
		}
	}
	return task.ErrProjectNotFound
}

// DeleteProject mirrors the store's guarantee: the project goes, its
// tasks come back to the default project.
func (f *fakeStore) DeleteProject(_ context.Context, id int64) error {
	if id == task.DefaultProjectID {
		return task.ErrProtectedProject
	}
	kept := f.projects[:0]
	for _, p := range f.projects {
		if p.ID != id {
			kept = append(kept, p)
		}
	}
	f.projects = kept
	for i := range f.tasks {
		if f.tasks[i].ProjectID == id {
			f.tasks[i].ProjectID = task.DefaultProjectID
		}
	}
	return nil
}

func (f *fakeStore) ActiveProjectID(context.Context) (int64, error) {
	if f.active == 0 {
		return task.DefaultProjectID, nil
	}
	return f.active, nil
}

func (f *fakeStore) SetActiveProject(_ context.Context, id int64) error {
	f.active = id
	return nil
}

// AddTask mirrors the store: a capture with no project named lands in the
// default project, never in whatever the TUI last had selected.
func (f *fakeStore) AddTask(_ context.Context, title string) (task.Task, error) {
	return f.add(task.DefaultProjectID, title, "")
}

func (f *fakeStore) AddTaskIn(_ context.Context, projectID int64, title string) (task.Task, error) {
	return f.add(projectID, title, "")
}

func (f *fakeStore) AddTaskWithBody(_ context.Context, title, body string) (task.Task, error) {
	return f.add(task.DefaultProjectID, title, body)
}

func (f *fakeStore) AddTaskWithBodyIn(_ context.Context, projectID int64, title, body string) (task.Task, error) {
	return f.add(projectID, title, body)
}

func (f *fakeStore) add(projectID int64, title, body string) (task.Task, error) {
	t, err := task.NormalizeTitle(title)
	if err != nil {
		return task.Task{}, err
	}
	captured := task.Task{
		ID: int64(len(f.tasks) + 1), Title: t, BodyMD: body,
		ProjectID: projectID,
	}
	f.tasks = append(f.tasks, captured)
	return captured, nil
}

func (f *fakeStore) ListLive(_ context.Context, projectID *int64) ([]task.Task, error) {
	if projectID == nil {
		return f.tasks, nil
	}
	var out []task.Task
	for _, t := range f.tasks {
		if t.ProjectID == *projectID {
			out = append(out, t)
		}
	}
	return out, nil
}

func (f *fakeStore) TagsByTask(context.Context) (map[int64][]string, error) { return f.tags, nil }

func (f *fakeStore) GetProject(_ context.Context, id int64) (task.Project, error) {
	for _, p := range f.projects {
		if p.ID == id {
			return p, nil
		}
	}
	return task.Project{}, task.ErrProjectNotFound
}
func (f *fakeStore) ListEvents(context.Context, time.Time, time.Time) ([]task.Event, error) {
	return nil, nil
}
func (f *fakeStore) AddLogEntry(context.Context, *int64, string) (task.LogEntry, error) {
	return task.LogEntry{}, nil
}
func (f *fakeStore) ListLogEntries(context.Context, time.Time, time.Time) ([]task.LogEntry, error) {
	return nil, nil
}
func (f *fakeStore) SetSessionStatus(_ context.Context, externalID string, st task.SessionStatus) error {
	if f.statusErr != nil {
		return f.statusErr
	}
	if f.statuses == nil {
		f.statuses = map[string]task.SessionStatus{}
	}
	f.statuses[externalID] = st
	return nil
}
func (f *fakeStore) Close() error { return nil }

func runAdd(t *testing.T, s *fakeStore, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	cmd := newAddCmd(func(context.Context) (Store, error) { return s, nil })
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)
	err = cmd.ExecuteContext(context.Background())
	return out.String(), errOut.String(), err
}

func TestAddPlainTitleUnaffected(t *testing.T) {
	s := newFakeStore()
	stdout, _, err := runAdd(t, s, "buy", "milk")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if len(s.tasks) != 1 || s.tasks[0].Title != "buy milk" || s.tasks[0].BodyMD != "" {
		t.Errorf("tasks = %+v, want one bare task %q", s.tasks, "buy milk")
	}
	if !strings.Contains(stdout, "added #1 to Unsorted: buy milk") {
		t.Errorf("stdout = %q", stdout)
	}
}

func TestAddJiraURLDegradesWithoutCredentials(t *testing.T) {
	keyring.MockInit()

	s := newFakeStore()
	url := "https://example.atlassian.net/browse/PROJ-42"
	stdout, stderr, err := runAdd(t, s, url)
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if len(s.tasks) != 1 {
		t.Fatalf("captured %d tasks, want 1", len(s.tasks))
	}
	if s.tasks[0].Title != "PROJ-42" {
		t.Errorf("Title = %q, want the bare key", s.tasks[0].Title)
	}
	if !strings.Contains(s.tasks[0].BodyMD, url) {
		t.Errorf("BodyMD = %q, want it to contain the link", s.tasks[0].BodyMD)
	}
	if !strings.Contains(stdout, "added #1 to Unsorted: PROJ-42") {
		t.Errorf("stdout = %q", stdout)
	}
	if !strings.Contains(stderr, "tend auth jira login") {
		t.Errorf("stderr = %q, want a login hint", stderr)
	}
}

func TestAddJiraURLAmongWordsStaysLiteral(t *testing.T) {
	s := newFakeStore()
	if _, _, err := runAdd(t, s, "look", "at", "https://example.atlassian.net/browse/PROJ-1"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if len(s.tasks) != 1 || s.tasks[0].BodyMD != "" {
		t.Errorf("tasks = %+v, want one literal-title task", s.tasks)
	}
}
