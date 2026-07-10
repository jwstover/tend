package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/jwstover/tend/internal/task"
)

// fakeStore records captured tasks; the other Store methods are unused
// by the add command.
type fakeStore struct {
	tasks []task.Task
}

func (f *fakeStore) AddTask(_ context.Context, title string) (task.Task, error) {
	return f.add(title, "")
}

func (f *fakeStore) AddTaskWithBody(_ context.Context, title, body string) (task.Task, error) {
	return f.add(title, body)
}

func (f *fakeStore) add(title, body string) (task.Task, error) {
	t, err := task.NormalizeTitle(title)
	if err != nil {
		return task.Task{}, err
	}
	captured := task.Task{ID: int64(len(f.tasks) + 1), Title: t, BodyMD: body}
	f.tasks = append(f.tasks, captured)
	return captured, nil
}

func (f *fakeStore) ListLive(context.Context) ([]task.Task, error) { return f.tasks, nil }
func (f *fakeStore) ListEvents(context.Context, time.Time, time.Time) ([]task.Event, error) {
	return nil, nil
}
func (f *fakeStore) AddLogEntry(context.Context, *int64, string) (task.LogEntry, error) {
	return task.LogEntry{}, nil
}
func (f *fakeStore) ListLogEntries(context.Context, time.Time, time.Time) ([]task.LogEntry, error) {
	return nil, nil
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
	s := &fakeStore{}
	stdout, _, err := runAdd(t, s, "buy", "milk")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if len(s.tasks) != 1 || s.tasks[0].Title != "buy milk" || s.tasks[0].BodyMD != "" {
		t.Errorf("tasks = %+v, want one bare task %q", s.tasks, "buy milk")
	}
	if !strings.Contains(stdout, "added #1: buy milk") {
		t.Errorf("stdout = %q", stdout)
	}
}

func TestAddJiraURLDegradesWithoutCredentials(t *testing.T) {
	keyring.MockInit()

	s := &fakeStore{}
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
	if !strings.Contains(stdout, "added #1: PROJ-42") {
		t.Errorf("stdout = %q", stdout)
	}
	if !strings.Contains(stderr, "tend auth jira login") {
		t.Errorf("stderr = %q, want a login hint", stderr)
	}
}

func TestAddJiraURLAmongWordsStaysLiteral(t *testing.T) {
	s := &fakeStore{}
	if _, _, err := runAdd(t, s, "look", "at", "https://example.atlassian.net/browse/PROJ-1"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if len(s.tasks) != 1 || s.tasks[0].BodyMD != "" {
		t.Errorf("tasks = %+v, want one literal-title task", s.tasks)
	}
}
