package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jwstover/tend/internal/store"
	"github.com/jwstover/tend/internal/task"
)

// realTaskStore opens a scratch database: these commands are thin over
// the store, so the store is what they are tested against.
func realTaskStore(t *testing.T) (*store.Store, func(context.Context) (TaskStore, error)) {
	t.Helper()
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "tend.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	// Commands close what they open; hand them a view whose Close is a
	// no-op so the test keeps its store.
	return s, func(context.Context) (TaskStore, error) { return keepOpen{s}, nil }
}

type keepOpen struct{ *store.Store }

func (keepOpen) Close() error { return nil }

func decode[T any](t *testing.T, out string) T {
	t.Helper()
	var v T
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	return v
}

func TestStateAndAppendPrintTheTaskAsShowDoes(t *testing.T) {
	ctx := context.Background()
	s, open := realTaskStore(t)
	tk, err := s.AddTaskWithBody(ctx, "fix it", "first")
	if err != nil {
		t.Fatal(err)
	}
	id := strconv.FormatInt(tk.ID, 10)

	out, err := runOn(newStateCmd(open), []string{id, "doing"})
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if got := decode[showJSON](t, out); got.Task.State != "doing" {
		t.Errorf("state = %q, want doing", got.Task.State)
	}
	if _, err := runOn(newStateCmd(open), []string{id, "nope"}); err == nil {
		t.Error("state nope: want an error")
	}

	out, err = runOn(newAppendCmd(open), []string{id, "second"})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	got := decode[showJSON](t, out)
	if got.Task.Body == nil || !strings.Contains(*got.Task.Body, "first") || !strings.Contains(*got.Task.Body, "second") {
		t.Errorf("body = %v", got.Task.Body)
	}
	if got.Sessions == nil || got.Log == nil || got.Children == nil {
		t.Error("empty lists should print as [], not null")
	}
}

// prepare records a hosted session and writes its files; a resume
// rewrites them for the same session; discard takes back only a
// session that never reported.
func TestSessionPrepareResumeDiscard(t *testing.T) {
	ctx := context.Background()
	s, open := realTaskStore(t)
	tk, err := s.AddTask(ctx, "fix it")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	in := prepareArgs{taskID: tk.ID, cwd: "/work", dir: dir, host: "devhub", tendPath: "/bin/tend", dbPath: "/data/tend.db"}
	ts, _ := open(ctx)

	got, err := prepare(ctx, ts, in)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	sess, err := s.SessionByExternalID(ctx, got.Session)
	if err != nil || sess.Host != "devhub" || sess.Cwd != "/work" || sess.TaskID != tk.ID {
		t.Fatalf("session row = %+v, %v", sess, err)
	}
	brief, err := os.ReadFile(got.SystemPromptFile)
	if err != nil || !strings.Contains(string(brief), "fix it") {
		t.Fatalf("brief = %q, %v", brief, err)
	}

	in.resume = got.Session
	again, err := prepare(ctx, ts, in)
	if err != nil || again.Session != got.Session || again.MCPConfig != got.MCPConfig {
		t.Fatalf("resume = %+v, %v", again, err)
	}

	mine, err := s.CreateSession(ctx, tk.ID, "ext-tend", "/work", tk.Title, "tend-ext")
	if err != nil {
		t.Fatal(err)
	}
	in.resume = mine.ExternalID
	if _, err := prepare(ctx, ts, in); err == nil {
		t.Error("resume of a session tend launched: want an error")
	}

	out, err := runOn(newSessionDiscardCmd(open), []string{got.Session})
	if err != nil || !decode[map[string]bool](t, out)["discarded"] {
		t.Fatalf("discard = %s, %v", out, err)
	}
	if _, err := s.SessionByExternalID(ctx, got.Session); err != task.ErrSessionNotFound {
		t.Errorf("row still there after discard: %v", err)
	}
}

func TestLsJSON(t *testing.T) {
	s := newFakeStore("tend")
	if _, err := s.AddTask(context.Background(), "in unsorted"); err != nil {
		t.Fatal(err)
	}
	out, err := runLs(t, s, "--json")
	if err != nil {
		t.Fatalf("ls --json: %v", err)
	}
	got := decode[struct {
		Tasks    []taskJSON    `json:"tasks"`
		Projects []projectJSON `json:"projects"`
	}](t, out)
	if len(got.Tasks) != 1 || got.Tasks[0].Title != "in unsorted" || got.Tasks[0].Tags == nil {
		t.Errorf("tasks = %+v", got.Tasks)
	}
	if len(got.Projects) < 2 {
		t.Errorf("projects = %+v", got.Projects)
	}
	for _, p := range got.Projects {
		want := int64(0)
		if p.ID == got.Tasks[0].ProjectID {
			want = 1
		}
		if p.LiveCount == nil || *p.LiveCount != want {
			t.Errorf("project %s live_count = %v, want %d", p.Name, p.LiveCount, want)
		}
	}
}

// Without --cwd a new session starts where tend would start it: here the
// project's directory, since the task has no session yet. With neither,
// prepare says to pass --cwd.
func TestSessionPrepareDefaultsToTendsDirectory(t *testing.T) {
	ctx := context.Background()
	s, open := realTaskStore(t)
	tk, err := s.AddTask(ctx, "fix it")
	if err != nil {
		t.Fatal(err)
	}
	ts, _ := open(ctx)
	in := prepareArgs{taskID: tk.ID, dir: t.TempDir(), host: "devhub", tendPath: "/bin/tend", dbPath: "/data/tend.db"}
	if _, err := prepare(ctx, ts, in); err == nil || !strings.Contains(err.Error(), "--cwd") {
		t.Fatalf("no directory anywhere: err = %v, want one naming --cwd", err)
	}
	if err := s.SetProjectCwd(ctx, tk.ProjectID, "/proj"); err != nil {
		t.Fatal(err)
	}
	got, err := prepare(ctx, ts, in)
	if err != nil || got.Cwd != "/proj" {
		t.Fatalf("prepare = %+v, %v; want cwd /proj", got, err)
	}
	shown, err := show(ctx, ts, tk.ID)
	if err != nil || shown.DefaultCwd != "/proj" {
		t.Fatalf("show default_cwd = %q, %v", shown.DefaultCwd, err)
	}
}

// Progress counts every sub-task, closed ones included, so it reads 1/2
// whether or not --closed brings the done one into the list.
func TestLsJSONClosedAndProgress(t *testing.T) {
	ctx := context.Background()
	s, _ := realTaskStore(t)
	parent, err := s.AddTask(ctx, "parent")
	if err != nil {
		t.Fatal(err)
	}
	done, err := s.AddChild(ctx, parent.ID, "done one")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddChild(ctx, parent.ID, "open one"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetState(ctx, done.ID, task.StateDone); err != nil {
		t.Fatal(err)
	}
	ls := func(args ...string) []taskJSON {
		t.Helper()
		out, err := runOn(newLsCmd(func(context.Context) (Store, error) { return keepOpen{s}, nil }), args)
		if err != nil {
			t.Fatalf("ls %v: %v", args, err)
		}
		return decode[struct {
			Tasks []taskJSON `json:"tasks"`
		}](t, out).Tasks
	}
	find := func(ts []taskJSON, id int64) *taskJSON {
		for i := range ts {
			if ts[i].ID == id {
				return &ts[i]
			}
		}
		return nil
	}
	open := ls("--json")
	if p := find(open, parent.ID); p == nil || p.ChildrenDone != 1 || p.ChildrenTotal != 2 {
		t.Fatalf("parent progress = %+v, want 1/2", p)
	}
	if find(open, done.ID) != nil {
		t.Error("the done sub-task is listed without --closed")
	}
	if find(ls("--json", "--closed"), done.ID) == nil {
		t.Error("the done sub-task is missing with --closed")
	}
}

// A task lists its sessions that have not ended, newest first; an ended
// one is left out.
func TestLsJSONListsOpenSessions(t *testing.T) {
	ctx := context.Background()
	s, _ := realTaskStore(t)
	tk, err := s.AddTask(ctx, "fix it")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"ext-old", "ext-ended", "ext-new"} {
		if _, err := s.CreateHostedSession(ctx, tk.ID, id, "/work", tk.Title, "devhub"); err != nil {
			t.Fatal(err)
		}
		if err := s.SetSessionStatus(ctx, id, task.SessionIdle); err != nil { // stamps last_active_at
			t.Fatal(err)
		}
		if id == "ext-old" {
			time.Sleep(1100 * time.Millisecond) // last_active_at has whole seconds
		}
	}
	if err := s.SetSessionStatus(ctx, "ext-ended", task.SessionEnded); err != nil {
		t.Fatal(err)
	}
	out, err := runOn(newLsCmd(func(context.Context) (Store, error) { return keepOpen{s}, nil }), []string{"--json"})
	if err != nil {
		t.Fatal(err)
	}
	got := decode[struct {
		Tasks []taskJSON `json:"tasks"`
	}](t, out).Tasks
	if len(got) != 1 || strings.Join(got[0].Sessions, ",") != "ext-new,ext-old" {
		t.Fatalf("sessions = %v, want ext-new,ext-old", got)
	}
}

// `tend body` saves against the version `show` printed and refuses once the
// body has changed since.
func TestBodyExpectsTheVersionItEdited(t *testing.T) {
	ctx := context.Background()
	s, open := realTaskStore(t)
	tk, err := s.AddTaskWithBody(ctx, "fix it", "first")
	if err != nil {
		t.Fatal(err)
	}
	ts, _ := open(ctx)
	read, err := show(ctx, ts, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	id := strconv.FormatInt(tk.ID, 10)
	body := func(text, version string) (string, error) {
		cmd := newBodyCmd(open)
		cmd.SetIn(strings.NewReader(text))
		return runOn(cmd, []string{id, "--expect", version})
	}
	out, err := body("edited", read.Task.BodyVersion)
	if err != nil {
		t.Fatalf("body: %v", err)
	}
	saved := decode[showJSON](t, out)
	if *saved.Task.Body != "edited" || saved.Task.BodyVersion == read.Task.BodyVersion {
		t.Fatalf("saved = %q (%s)", *saved.Task.Body, saved.Task.BodyVersion)
	}
	if _, err := body("again", read.Task.BodyVersion); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("stale save: err = %v, want a refusal", err)
	}
}
