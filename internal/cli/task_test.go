package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

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
}
