package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jwstover/tend/internal/runner"
	"github.com/jwstover/tend/internal/task"
	"github.com/jwstover/tend/internal/workflow"
)

// logsFakeStore is the slice of WorkflowStore `logs` reads: one run, its
// step runs, and their steps. The embedded runner.Store is nil; any
// method the command does not call would panic, which is the point -- the
// test says exactly what the command may touch. The mutex is for the
// follow test, which finishes a step run from another goroutine while the
// command polls.
type logsFakeStore struct {
	runner.Store
	mu       sync.Mutex
	run      workflow.Run
	stepRuns []workflow.StepRun
	steps    map[int64]workflow.Step
}

func (s *logsFakeStore) GetRun(_ context.Context, id int64) (workflow.Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id != s.run.ID {
		return workflow.Run{}, workflow.ErrRunNotFound
	}
	return s.run, nil
}

func (s *logsFakeStore) GetStepRun(_ context.Context, id int64) (workflow.StepRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sr := range s.stepRuns {
		if sr.ID == id {
			return sr, nil
		}
	}
	return workflow.StepRun{}, workflow.ErrStepRunNotFound
}

func (s *logsFakeStore) ListStepRunsForRun(_ context.Context, runID int64) ([]workflow.StepRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []workflow.StepRun
	for _, sr := range s.stepRuns {
		if sr.RunID == runID {
			out = append(out, sr)
		}
	}
	return out, nil
}

func (s *logsFakeStore) GetStep(_ context.Context, id int64) (workflow.Step, error) {
	if st, ok := s.steps[id]; ok {
		return st, nil
	}
	return workflow.Step{}, workflow.ErrStepNotFound
}

func (s *logsFakeStore) Close() error { return nil }

// The rest of WorkflowStore, never reached by `logs`.
func (s *logsFakeStore) ListWorkflows(context.Context) ([]workflow.Workflow, error) {
	return nil, errors.New("logsFakeStore: not implemented")
}
func (s *logsFakeStore) WorkflowByName(context.Context, string) (workflow.Workflow, error) {
	return workflow.Workflow{}, errors.New("logsFakeStore: not implemented")
}
func (s *logsFakeStore) ListEdges(context.Context, int64) ([]workflow.Edge, error) {
	return nil, errors.New("logsFakeStore: not implemented")
}
func (s *logsFakeStore) CreateRun(context.Context, int64, int64, string) (workflow.Run, error) {
	return workflow.Run{}, errors.New("logsFakeStore: not implemented")
}
func (s *logsFakeStore) ListActiveRuns(context.Context) ([]workflow.Run, error) {
	return nil, errors.New("logsFakeStore: not implemented")
}
func (s *logsFakeStore) ListSessionsForTask(context.Context, int64) ([]task.Session, error) {
	return nil, errors.New("logsFakeStore: not implemented")
}
func (s *logsFakeStore) GetProject(context.Context, int64) (task.Project, error) {
	return task.Project{}, errors.New("logsFakeStore: not implemented")
}

// finish marks a step run finished, the way FinishStepRun would.
func (s *logsFakeStore) finish(stepRunID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for i := range s.stepRuns {
		if s.stepRuns[i].ID == stepRunID {
			s.stepRuns[i].Outcome = workflow.OutcomeDone
			s.stepRuns[i].EndedAt = &now
		}
	}
}

// setRunState moves the run, the way SetRunState would.
func (s *logsFakeStore) setRunState(st workflow.RunState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.run.State = st
}

func assistantText(text string) string {
	return `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"` + text + `"}]},"session_id":"s"}`
}

const initLine = `{"type":"system","subtype":"init","model":"claude-sonnet-4-5","permissionMode":"acceptEdits","session_id":"s"}`

// newLogsFixture builds a running run on a three-step workflow -- an
// agent step that finished, a gate, and the current agent step -- with
// log files for the two agent steps under dir.
func newLogsFixture(t *testing.T, dir string) *logsFakeStore {
	t.Helper()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
		return p
	}
	current := int64(3)
	started := time.Now().Add(-time.Minute)
	ended := time.Now().Add(-30 * time.Second)
	return &logsFakeStore{
		run: workflow.Run{ID: 7, WorkflowID: 1, TaskID: 42, State: workflow.RunRunning,
			CurrentStepRunID: &current, StartedAt: started},
		steps: map[int64]workflow.Step{
			10: {ID: 10, Name: "plan", Kind: workflow.StepAgent},
			11: {ID: 11, Name: "review", Kind: workflow.StepGate},
			12: {ID: 12, Name: "implement", Kind: workflow.StepAgent},
		},
		stepRuns: []workflow.StepRun{
			{ID: 1, RunID: 7, StepID: 10, Iteration: 1, Outcome: workflow.OutcomeDone,
				LogPath:   write("1.jsonl", initLine+"\n"+assistantText("planned")+"\n"),
				StartedAt: started, EndedAt: &ended},
			{ID: 2, RunID: 7, StepID: 11, Iteration: 1, Outcome: workflow.OutcomeApprove,
				StartedAt: started, EndedAt: &ended},
			{ID: 3, RunID: 7, StepID: 12, Iteration: 1,
				LogPath:   write("3.jsonl", initLine+"\n"+assistantText("hello")+"\n"),
				StartedAt: ended},
		},
	}
}

func runLogs(s *logsFakeStore, args ...string) (string, error) {
	return runOn(newWorkflowLogsCmd(func(context.Context) (WorkflowStore, error) { return s, nil }), args)
}

func TestWorkflowLogsPicksStep(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    []string // substrings the output must contain
		wantErr string   // substring the error must contain, "" for success
	}{
		{
			name: "default is the current step",
			args: []string{"7"},
			want: []string{"hello"},
		},
		{
			name: "--step picks by status numbering",
			args: []string{"7", "--step", "1"},
			want: []string{"planned"},
		},
		{
			name:    "--step out of range names the count",
			args:    []string{"7", "--step", "4"},
			wantErr: "run 7 has 3 step runs; --step 4 is out of range",
		},
		{
			name:    "--step 0 is rejected",
			args:    []string{"7", "--step", "0"},
			wantErr: "step numbers start at 1",
		},
		{
			name:    "a gate has no log",
			args:    []string{"7", "--step", "2"},
			wantErr: "step 2 (review) is a gate; it has no log",
		},
		{
			name:    "unknown run",
			args:    []string{"9"},
			wantErr: workflow.ErrRunNotFound.Error(),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newLogsFixture(t, t.TempDir())
			out, err := runLogs(s, tc.args...)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			for _, w := range tc.want {
				if !strings.Contains(out, w) {
					t.Errorf("output missing %q:\n%s", w, out)
				}
			}
		})
	}
}

func TestWorkflowLogsDefaultsToLastStepWithoutCurrent(t *testing.T) {
	s := newLogsFixture(t, t.TempDir())
	s.run.CurrentStepRunID = nil
	out, err := runLogs(s, "7")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "hello") || strings.Contains(out, "planned") {
		t.Fatalf("want the last step's log only, got:\n%s", out)
	}
}

func TestWorkflowLogsNoStepRunsYet(t *testing.T) {
	s := newLogsFixture(t, t.TempDir())
	s.stepRuns = nil
	s.run.CurrentStepRunID = nil
	_, err := runLogs(s, "7")
	if err == nil || !strings.Contains(err.Error(), "run 7 has no step runs yet") {
		t.Fatalf("err = %v, want no step runs yet", err)
	}
}

func TestWorkflowLogsAgentStepWithoutLogPath(t *testing.T) {
	s := newLogsFixture(t, t.TempDir())
	s.stepRuns[2].LogPath = ""
	_, err := runLogs(s, "7")
	if err == nil || !strings.Contains(err.Error(), "no log yet for step 3 (implement)") {
		t.Fatalf("err = %v, want no log yet", err)
	}
}

func TestWorkflowLogsMissingFilePrintsNothing(t *testing.T) {
	dir := t.TempDir()
	s := newLogsFixture(t, dir)
	s.stepRuns[2].LogPath = filepath.Join(dir, "not-written-yet.jsonl")
	out, err := runLogs(s, "7")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != "" {
		t.Fatalf("want no output for a log not yet written, got:\n%s", out)
	}
}

func TestWorkflowLogsRenderedVersusRaw(t *testing.T) {
	s := newLogsFixture(t, t.TempDir())

	rendered, err := runLogs(s, "7")
	if err != nil {
		t.Fatalf("rendered: %v", err)
	}
	want := "session started · claude-sonnet-4-5 · acceptEdits\nhello\n"
	if rendered != want {
		t.Errorf("rendered = %q, want %q", rendered, want)
	}

	raw, err := runLogs(s, "7", "--raw")
	if err != nil {
		t.Fatalf("raw: %v", err)
	}
	if raw != initLine+"\n"+assistantText("hello")+"\n" {
		t.Errorf("raw = %q, want the file's lines verbatim", raw)
	}
}

func TestWorkflowLogsRunnerLog(t *testing.T) {
	dataHome := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dataHome)
	s := newLogsFixture(t, t.TempDir())

	_, err := runLogs(s, "7", "--runner")
	if err == nil || !strings.Contains(err.Error(), "runner.log") {
		t.Fatalf("err = %v, want an error naming the missing runner.log", err)
	}

	dir := filepath.Join(dataHome, "tend", "runs", "7")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "runner.log"), []byte("claimed run 7\nstep plan: started\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// --runner is plain text: --step and --raw are ignored rather than
	// refused, and the lines come out as written.
	out, err := runLogs(s, "7", "--runner", "--step", "9")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != "claimed run 7\nstep plan: started\n" {
		t.Fatalf("out = %q", out)
	}
}

// shortPoll shortens the follow poll for the duration of the test.
func shortPoll(t *testing.T) {
	t.Helper()
	old := logsPollInterval
	logsPollInterval = 2 * time.Millisecond
	t.Cleanup(func() { logsPollInterval = old })
}

func appendFile(t *testing.T, path, content string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Errorf("opening %s: %v", path, err)
		return
	}
	defer f.Close()
	if _, err := f.WriteString(content); err != nil {
		t.Errorf("appending to %s: %v", path, err)
	}
}

func TestWorkflowLogsFollowStopsWhenStepFinishes(t *testing.T) {
	shortPoll(t)
	s := newLogsFixture(t, t.TempDir())
	path := s.stepRuns[2].LogPath

	// A second line arrives in two writes -- the partial line must wait
	// for its newline -- then a last one lands right before the step's
	// row is finished, which the final drain has to catch.
	go func() {
		time.Sleep(20 * time.Millisecond)
		half := assistantText("appended")
		appendFile(t, path, half[:len(half)/2])
		time.Sleep(20 * time.Millisecond)
		appendFile(t, path, half[len(half)/2:]+"\n")
		time.Sleep(20 * time.Millisecond)
		appendFile(t, path, assistantText("last word")+"\n")
		s.finish(3)
	}()

	done := make(chan struct{})
	var out string
	var err error
	go func() {
		defer close(done)
		out, err = runLogs(s, "7", "-f")
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("logs -f did not stop after the step finished")
	}
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "session started · claude-sonnet-4-5 · acceptEdits\nhello\nappended\nlast word\n"
	if out != want {
		t.Fatalf("out = %q, want %q", out, want)
	}
}

func TestWorkflowLogsFollowStopsWhenRunEnds(t *testing.T) {
	shortPoll(t)
	s := newLogsFixture(t, t.TempDir())

	// The step run never finishes (a cancel kills the step outright);
	// the run reaching a terminal state must end the follow instead.
	go func() {
		time.Sleep(20 * time.Millisecond)
		s.setRunState(workflow.RunCancelled)
	}()

	done := make(chan struct{})
	var out string
	var err error
	go func() {
		defer close(done)
		out, err = runLogs(s, "7", "-f")
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("logs -f did not stop after the run ended")
	}
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "hello") {
		t.Fatalf("out = %q, want the existing log", out)
	}
}

func TestWorkflowLogsFollowRunnerLogUntilTerminal(t *testing.T) {
	shortPoll(t)
	dataHome := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dataHome)
	s := newLogsFixture(t, t.TempDir())
	dir := filepath.Join(dataHome, "tend", "runs", "7")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "runner.log")
	if err := os.WriteFile(path, []byte("claimed run 7\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	go func() {
		time.Sleep(20 * time.Millisecond)
		appendFile(t, path, "run done\n")
		s.setRunState(workflow.RunDone)
	}()

	done := make(chan struct{})
	var out string
	var err error
	go func() {
		defer close(done)
		out, err = runLogs(s, "7", "--runner", "-f")
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("logs --runner -f did not stop after the run ended")
	}
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != "claimed run 7\nrun done\n" {
		t.Fatalf("out = %q", out)
	}
}

func TestWorkflowLogsFollowEndsQuietlyOnCancel(t *testing.T) {
	shortPoll(t)
	s := newLogsFixture(t, t.TempDir())
	cmd := newWorkflowLogsCmd(func(context.Context) (WorkflowStore, error) { return s, nil })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cmd.SetContext(ctx)

	// Ctrl-C is a cancelled context: the follow returns nil, not
	// context.Canceled, after printing what was there.
	out, err := runOn(cmd, []string{"7", "-f"})
	if err != nil {
		t.Fatalf("err = %v, want nil on a cancelled context", err)
	}
	if !strings.Contains(out, "hello") {
		t.Fatalf("out = %q, want the existing log", out)
	}
}

func TestPickStepRun(t *testing.T) {
	two := int64(2)
	stepRuns := []workflow.StepRun{{ID: 1}, {ID: 2}, {ID: 3}}
	tests := []struct {
		name    string
		run     workflow.Run
		runs    []workflow.StepRun
		n       int
		want    int
		wantErr bool
	}{
		{name: "explicit", run: workflow.Run{ID: 1}, runs: stepRuns, n: 3, want: 2},
		{name: "current", run: workflow.Run{ID: 1, CurrentStepRunID: &two}, runs: stepRuns, want: 1},
		{name: "last when no current", run: workflow.Run{ID: 1}, runs: stepRuns, want: 2},
		{name: "too high", run: workflow.Run{ID: 1}, runs: stepRuns, n: 4, wantErr: true},
		{name: "negative", run: workflow.Run{ID: 1}, runs: stepRuns, n: -1, wantErr: true},
		{name: "none", run: workflow.Run{ID: 1}, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := pickStepRun(tc.run, tc.runs, tc.n)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got index %d", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %d, want %d", got, tc.want)
			}
		})
	}
}
