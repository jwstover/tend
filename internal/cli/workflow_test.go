package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jwstover/tend/internal/runner"
	"github.com/jwstover/tend/internal/task"
	"github.com/jwstover/tend/internal/workflow"
)

// fakeWorkflowStore is an in-memory stand-in for the WorkflowStore slice
// of *store.Store: enough of the run lifecycle -- create, claim, state,
// step runs, gate decisions -- that the workflow commands' tests assert
// what was written, not just that a stub was called. The same rules the
// real store enforces are kept where a command leans on them: terminal is
// final (SetRunState refuses it), a finished step run cannot be finished
// again, and a workflow name resolves case-insensitively.
type fakeWorkflowStore struct {
	workflows []workflow.Workflow
	steps     []workflow.Step
	edges     []workflow.Edge
	runs      []workflow.Run
	stepRuns  []workflow.StepRun
	tasks     []task.Task
	projects  []task.Project
	sessions  []task.Session
	closed    bool
}

func newFakeWorkflowStore() *fakeWorkflowStore {
	return &fakeWorkflowStore{projects: []task.Project{{ID: task.DefaultProjectID, Name: "Unsorted"}}}
}

func (f *fakeWorkflowStore) addWorkflow(name string, stepNames ...string) workflow.Workflow {
	wf := workflow.Workflow{ID: int64(len(f.workflows) + 1), Name: name}
	f.workflows = append(f.workflows, wf)
	for i, n := range stepNames {
		f.steps = append(f.steps, workflow.Step{
			ID: int64(len(f.steps) + 1), WorkflowID: wf.ID, Name: n, Kind: workflow.StepAgent,
			PromptMD: "do " + n, SortOrder: int64(i),
		})
	}
	return wf
}

func (f *fakeWorkflowStore) addTask(title string) task.Task {
	t := task.Task{ID: int64(len(f.tasks) + 1), Title: title, ProjectID: task.DefaultProjectID}
	f.tasks = append(f.tasks, t)
	return t
}

func (f *fakeWorkflowStore) setKind(stepID int64, kind workflow.StepKind) {
	for i := range f.steps {
		if f.steps[i].ID == stepID {
			f.steps[i].Kind = kind
		}
	}
}

// addRun writes a run in state st with one step run per step id given,
// the last of them current and unfinished.
func (f *fakeWorkflowStore) addRun(wfID, taskID int64, st workflow.RunState, stepIDs ...int64) workflow.Run {
	run := workflow.Run{ID: int64(len(f.runs) + 1), WorkflowID: wfID, TaskID: taskID, Cwd: "/w", State: st, StartedAt: time.Now()}
	for i, id := range stepIDs {
		sr := workflow.StepRun{ID: int64(len(f.stepRuns) + 1), RunID: run.ID, StepID: id, Iteration: 1, StartedAt: time.Now()}
		if i < len(stepIDs)-1 {
			now := time.Now()
			sr.Outcome, sr.EndedAt = workflow.OutcomeDone, &now
		} else {
			sr.Input = "the deliverable under review"
			run.CurrentStepRunID = &sr.ID
		}
		f.stepRuns = append(f.stepRuns, sr)
	}
	f.runs = append(f.runs, run)
	return run
}

func (f *fakeWorkflowStore) run(id int64) *workflow.Run {
	for i := range f.runs {
		if f.runs[i].ID == id {
			return &f.runs[i]
		}
	}
	return nil
}

func (f *fakeWorkflowStore) GetRun(_ context.Context, id int64) (workflow.Run, error) {
	if r := f.run(id); r != nil {
		return *r, nil
	}
	return workflow.Run{}, fmt.Errorf("run %d: %w", id, workflow.ErrRunNotFound)
}

func (f *fakeWorkflowStore) ClaimRun(_ context.Context, id int64) (bool, error) {
	r := f.run(id)
	if r == nil || (r.State != workflow.RunPending && r.State != workflow.RunPaused) {
		return false, nil
	}
	r.State = workflow.RunRunning
	return true, nil
}

func (f *fakeWorkflowStore) SetRunState(_ context.Context, id int64, st workflow.RunState) error {
	r := f.run(id)
	if r == nil {
		return workflow.ErrRunNotFound
	}
	if r.State.Terminal() {
		return workflow.ErrRunEnded
	}
	r.State = st
	if st.Terminal() {
		now := time.Now()
		r.EndedAt = &now
	}
	return nil
}

func (f *fakeWorkflowStore) FailRun(ctx context.Context, id int64, reason string) error {
	if err := f.SetRunState(ctx, id, workflow.RunFailed); err != nil {
		return err
	}
	f.run(id).Error = reason
	return nil
}

func (f *fakeWorkflowStore) SetRunTmuxSession(_ context.Context, id int64, name string) error {
	if r := f.run(id); r != nil {
		r.TmuxSession = name
		return nil
	}
	return workflow.ErrRunNotFound
}

func (f *fakeWorkflowStore) GetWorkflow(_ context.Context, id int64) (workflow.Workflow, error) {
	for _, wf := range f.workflows {
		if wf.ID == id {
			return wf, nil
		}
	}
	return workflow.Workflow{}, fmt.Errorf("workflow %d: %w", id, workflow.ErrWorkflowNotFound)
}

func (f *fakeWorkflowStore) WorkflowByName(_ context.Context, name string) (workflow.Workflow, error) {
	n, err := workflow.NormalizeName(name)
	if err != nil {
		return workflow.Workflow{}, err
	}
	for _, wf := range f.workflows {
		if strings.EqualFold(wf.Name, n) {
			return wf, nil
		}
	}
	return workflow.Workflow{}, fmt.Errorf("workflow %q: %w", n, workflow.ErrWorkflowNotFound)
}

func (f *fakeWorkflowStore) ListWorkflows(context.Context) ([]workflow.Workflow, error) {
	out := make([]workflow.Workflow, len(f.workflows))
	copy(out, f.workflows)
	for i := range out {
		for _, st := range f.steps {
			if st.WorkflowID == out[i].ID {
				out[i].StepCount++
			}
		}
	}
	return out, nil
}

func (f *fakeWorkflowStore) ListSteps(_ context.Context, workflowID int64) ([]workflow.Step, error) {
	var out []workflow.Step
	for _, st := range f.steps {
		if st.WorkflowID == workflowID {
			out = append(out, st)
		}
	}
	return out, nil
}

func (f *fakeWorkflowStore) GetStep(_ context.Context, id int64) (workflow.Step, error) {
	for _, st := range f.steps {
		if st.ID == id {
			return st, nil
		}
	}
	return workflow.Step{}, fmt.Errorf("step %d: %w", id, workflow.ErrStepNotFound)
}

func (f *fakeWorkflowStore) ListEdges(_ context.Context, workflowID int64) ([]workflow.Edge, error) {
	var out []workflow.Edge
	for _, e := range f.edges {
		if st, err := f.GetStep(context.Background(), e.FromStepID); err == nil && st.WorkflowID == workflowID {
			out = append(out, e)
		}
	}
	return out, nil
}

func (f *fakeWorkflowStore) OutgoingEdges(_ context.Context, stepID int64) ([]workflow.Edge, error) {
	var out []workflow.Edge
	for _, e := range f.edges {
		if e.FromStepID == stepID {
			out = append(out, e)
		}
	}
	return out, nil
}

func (f *fakeWorkflowStore) GetTask(_ context.Context, id int64) (task.Task, error) {
	for _, t := range f.tasks {
		if t.ID == id {
			return t, nil
		}
	}
	return task.Task{}, fmt.Errorf("loading task %d: not found", id)
}

func (f *fakeWorkflowStore) ListChildren(_ context.Context, parentID int64) ([]task.Task, error) {
	var out []task.Task
	for _, t := range f.tasks {
		if t.ParentID != nil && *t.ParentID == parentID {
			out = append(out, t)
		}
	}
	return out, nil
}

// Blockers reports no dependencies: the workflow commands only need the
// runner's prompt data to build, and no cli test renders {{.Subtasks}}.
func (f *fakeWorkflowStore) Blockers(context.Context, int64) ([]task.Task, error) {
	return nil, nil
}

func (f *fakeWorkflowStore) GetProject(_ context.Context, id int64) (task.Project, error) {
	for _, p := range f.projects {
		if p.ID == id {
			return p, nil
		}
	}
	return task.Project{}, task.ErrProjectNotFound
}

func (f *fakeWorkflowStore) ListSessionsForTask(_ context.Context, taskID int64) ([]task.Session, error) {
	var out []task.Session
	for _, s := range f.sessions {
		if s.TaskID == taskID {
			out = append(out, s)
		}
	}
	return out, nil
}

func (f *fakeWorkflowStore) CreateRun(_ context.Context, workflowID, taskID int64, cwd string) (workflow.Run, error) {
	run := workflow.Run{ID: int64(len(f.runs) + 1), WorkflowID: workflowID, TaskID: taskID, Cwd: cwd,
		State: workflow.RunPending, StartedAt: time.Now()}
	f.runs = append(f.runs, run)
	return run, nil
}

func (f *fakeWorkflowStore) ListActiveRuns(context.Context) ([]workflow.Run, error) {
	var out []workflow.Run
	for i := len(f.runs) - 1; i >= 0; i-- {
		if !f.runs[i].State.Terminal() {
			out = append(out, f.runs[i])
		}
	}
	return out, nil
}

func (f *fakeWorkflowStore) CreateStepRun(_ context.Context, sr workflow.StepRun) (workflow.StepRun, error) {
	sr.ID = int64(len(f.stepRuns) + 1)
	sr.StartedAt = time.Now()
	f.stepRuns = append(f.stepRuns, sr)
	return sr, nil
}

func (f *fakeWorkflowStore) GetStepRun(_ context.Context, id int64) (workflow.StepRun, error) {
	for _, sr := range f.stepRuns {
		if sr.ID == id {
			return sr, nil
		}
	}
	return workflow.StepRun{}, fmt.Errorf("step run %d: %w", id, workflow.ErrStepRunNotFound)
}

func (f *fakeWorkflowStore) ListStepRunsForRun(_ context.Context, runID int64) ([]workflow.StepRun, error) {
	var out []workflow.StepRun
	for _, sr := range f.stepRuns {
		if sr.RunID == runID {
			out = append(out, sr)
		}
	}
	return out, nil
}

func (f *fakeWorkflowStore) FinishStepRun(_ context.Context, id int64, outcome, deliverable string) error {
	o, err := workflow.NormalizeOutcome(outcome)
	if err != nil {
		return err
	}
	for i := range f.stepRuns {
		if f.stepRuns[i].ID == id {
			if f.stepRuns[i].Finished() {
				return workflow.ErrStepRunFinished
			}
			now := time.Now()
			f.stepRuns[i].Outcome, f.stepRuns[i].Deliverable, f.stepRuns[i].EndedAt = o, deliverable, &now
			return nil
		}
	}
	return workflow.ErrStepRunNotFound
}

func (f *fakeWorkflowStore) SetStepRunLogPath(_ context.Context, id int64, path string) error {
	for i := range f.stepRuns {
		if f.stepRuns[i].ID == id {
			f.stepRuns[i].LogPath = path
			return nil
		}
	}
	return workflow.ErrStepRunNotFound
}

func (f *fakeWorkflowStore) SetStepRunSession(_ context.Context, id int64, externalID string) error {
	for i := range f.stepRuns {
		if f.stepRuns[i].ID == id {
			f.stepRuns[i].SessionExternalID = externalID
			return nil
		}
	}
	return workflow.ErrStepRunNotFound
}

func (f *fakeWorkflowStore) CreateStepRunSession(_ context.Context, stepRunID, taskID int64, externalID, cwd, label, tmuxSession string) (task.Session, error) {
	s := task.Session{ID: int64(len(f.sessions) + 1), TaskID: taskID, ExternalID: externalID, Cwd: cwd,
		Label: label, TmuxSession: tmuxSession, StepRunID: &stepRunID}
	f.sessions = append(f.sessions, s)
	return s, nil
}

func (f *fakeWorkflowStore) SetSessionStatus(context.Context, string, task.SessionStatus) error {
	return nil
}

func (f *fakeWorkflowStore) Close() error {
	f.closed = true
	return nil
}

var _ WorkflowStore = (*fakeWorkflowStore)(nil)

// stubProcessControl points the claude/tmux seams at fakes for one test:
// both binaries present, a runner that "launches" by recording the run id
// and returning the session name tmux would, and a runner-alive answer the
// test picks. Restored when the test ends.
func stubProcessControl(t *testing.T, alive, known bool) *[]int64 {
	t.Helper()
	oldClaude, oldTmux, oldLaunch, oldAlive := checkClaude, tmuxPresent, launchRunner, runnerAlive
	t.Cleanup(func() { checkClaude, tmuxPresent, launchRunner, runnerAlive = oldClaude, oldTmux, oldLaunch, oldAlive })
	var launched []int64
	checkClaude = func() error { return nil }
	tmuxPresent = func() bool { return true }
	launchRunner = func(ctx context.Context, s runner.LaunchStore, runID int64, _ string) (string, error) {
		launched = append(launched, runID)
		name := fmt.Sprintf("tend-wf-%d", runID)
		return name, s.SetRunTmuxSession(ctx, runID, name)
	}
	runnerAlive = func(int64) (bool, bool) { return alive, known }
	return &launched
}

func runWorkflow(t *testing.T, s *fakeWorkflowStore, args ...string) (string, error) {
	t.Helper()
	cmd := newWorkflowCmd(func(context.Context) (WorkflowStore, error) { return s, nil }, func() string { return "/tmp/tend.db" })
	return runOn(cmd, args)
}

// `tend workflow` and `tend workflow ls` list the workflows with their step
// counts, the way `tend projects` lists projects.
func TestWorkflowLsShowsStepCounts(t *testing.T) {
	s := newFakeWorkflowStore()
	s.addWorkflow("ship", "plan", "implement", "review")
	s.addWorkflow("triage", "look")

	for _, args := range [][]string{nil, {"ls"}} {
		out, err := runWorkflow(t, s, args...)
		if err != nil {
			t.Fatalf("workflow %v: %v", args, err)
		}
		for _, want := range []string{"ship", "3 steps", "triage", "1 step"} {
			if !strings.Contains(out, want) {
				t.Errorf("workflow %v listing missing %q:\n%s", args, want, out)
			}
		}
	}
}

func TestWorkflowLsEmptySaysWhereToCreateOne(t *testing.T) {
	out, err := runWorkflow(t, newFakeWorkflowStore(), "ls")
	if err != nil {
		t.Fatalf("ls: %v", err)
	}
	if !strings.Contains(out, "no workflows yet") {
		t.Errorf("empty listing should say so: %q", out)
	}
}

// `start` writes a pending run and launches its runner, the CLI counterpart
// of the TUI's `w` chord, and says how to follow it.
func TestWorkflowStartCreatesRunAndLaunchesRunner(t *testing.T) {
	s := newFakeWorkflowStore()
	launched := stubProcessControl(t, true, true)
	wf := s.addWorkflow("ship", "plan", "implement")
	tk := s.addTask("add the thing")

	out, err := runWorkflow(t, s, "start", "ship", "--task", "1", "--cwd", "/repo")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if len(s.runs) != 1 {
		t.Fatalf("runs = %d, want 1", len(s.runs))
	}
	run := s.runs[0]
	if run.WorkflowID != wf.ID || run.TaskID != tk.ID || run.Cwd != "/repo" {
		t.Errorf("run = %+v, want workflow %d on task %d in /repo", run, wf.ID, tk.ID)
	}
	if run.State != workflow.RunPending {
		t.Errorf("state = %s, want pending: the runner claims it", run.State)
	}
	if *launched == nil || (*launched)[0] != run.ID {
		t.Errorf("launched = %v, want run %d", *launched, run.ID)
	}
	if run.TmuxSession != "tend-wf-1" {
		t.Errorf("tmux session %q not recorded on the run", run.TmuxSession)
	}
	for _, want := range []string{"started run 1", "ship", "#1", "tend workflow status 1"} {
		if !strings.Contains(out, want) {
			t.Errorf("start output missing %q:\n%s", want, out)
		}
	}
}

// A workflow name resolves case-insensitively, or by the id `ls` prints;
// an unknown one is an error with a hint and never a run.
func TestWorkflowStartResolvesNamesNeverCreates(t *testing.T) {
	s := newFakeWorkflowStore()
	stubProcessControl(t, true, true)
	s.addWorkflow("Ship It", "plan")
	s.addTask("t")

	if _, err := runWorkflow(t, s, "start", "ship", "it", "--task", "1", "--cwd", "/r"); err != nil {
		t.Fatalf("start by unquoted, differently-cased name: %v", err)
	}
	if _, err := runWorkflow(t, s, "start", "1", "--task", "1", "--cwd", "/r"); err != nil {
		t.Fatalf("start by id: %v", err)
	}
	if len(s.runs) != 2 {
		t.Fatalf("runs = %d, want 2", len(s.runs))
	}

	_, err := runWorkflow(t, s, "start", "shp", "--task", "1", "--cwd", "/r")
	if err == nil {
		t.Fatal("an unknown workflow should fail")
	}
	if !strings.Contains(err.Error(), "tend workflow ls") {
		t.Errorf("error should hint at the listing: %v", err)
	}
	if len(s.runs) != 2 || len(s.workflows) != 1 {
		t.Errorf("a failed lookup wrote something: runs=%d workflows=%d", len(s.runs), len(s.workflows))
	}
}

// The cwd defaults the way the TUI's prompt does: the task's most recent
// session's directory first, then the project's default.
func TestWorkflowStartDefaultsCwdFromSessionsThenProject(t *testing.T) {
	s := newFakeWorkflowStore()
	stubProcessControl(t, true, true)
	s.addWorkflow("ship", "plan")
	s.addTask("t")
	s.projects[0].Cwd = "/proj"

	if _, err := runWorkflow(t, s, "start", "ship", "--task", "1"); err != nil {
		t.Fatalf("start: %v", err)
	}
	if got := s.runs[0].Cwd; got != "/proj" {
		t.Errorf("cwd = %q, want the project default /proj", got)
	}

	s.sessions = append(s.sessions, task.Session{ID: 1, TaskID: 1, Cwd: "/last"})
	if _, err := runWorkflow(t, s, "start", "ship", "--task", "1"); err != nil {
		t.Fatalf("start: %v", err)
	}
	if got := s.runs[1].Cwd; got != "/last" {
		t.Errorf("cwd = %q, want the last session's /last", got)
	}
}

// A workflow that cannot run is refused before anything is written, naming
// what is missing: no steps, a prompt that does not render, or a missing
// binary.
func TestWorkflowStartRefusesUnrunnable(t *testing.T) {
	cases := []struct {
		name  string
		setup func(s *fakeWorkflowStore)
		want  string
	}{
		{"no steps", func(s *fakeWorkflowStore) { s.addWorkflow("empty") }, "no steps"},
		{"bad prompt", func(s *fakeWorkflowStore) {
			s.addWorkflow("empty", "plan")
			s.steps[0].PromptMD = "{{.Nope"
		}, "plan"},
		{"no claude", func(s *fakeWorkflowStore) {
			s.addWorkflow("empty", "plan")
			checkClaude = func() error { return errors.New("claude: not found on $PATH") }
		}, "claude"},
		{"no tmux", func(s *fakeWorkflowStore) {
			s.addWorkflow("empty", "plan")
			tmuxPresent = func() bool { return false }
		}, "tmux"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newFakeWorkflowStore()
			stubProcessControl(t, true, true)
			s.addTask("t")
			tc.setup(s)
			_, err := runWorkflow(t, s, "start", "empty", "--task", "1", "--cwd", "/r")
			if err == nil {
				t.Fatal("start should be refused")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q should name %q", err, tc.want)
			}
			if len(s.runs) != 0 {
				t.Errorf("a refused start wrote a run: %+v", s.runs)
			}
		})
	}
}

// A runner that cannot be started fails the run on the spot, so no pending
// run is left looking live.
func TestWorkflowStartFailsRunWhenRunnerWontLaunch(t *testing.T) {
	s := newFakeWorkflowStore()
	stubProcessControl(t, true, true)
	launchRunner = func(context.Context, runner.LaunchStore, int64, string) (string, error) {
		return "", errors.New("tmux exploded")
	}
	s.addWorkflow("ship", "plan")
	s.addTask("t")

	_, err := runWorkflow(t, s, "start", "ship", "--task", "1", "--cwd", "/r")
	if err == nil || !strings.Contains(err.Error(), "tmux exploded") {
		t.Fatalf("start should surface the launch error, got %v", err)
	}
	if len(s.runs) != 1 || s.runs[0].State != workflow.RunFailed || s.runs[0].Error != "tmux exploded" {
		t.Errorf("run should be failed with the reason: %+v", s.runs)
	}
}

// `status` lists live runs with their current step and flags a run whose
// runner is gone; `status <run-id>` shows one run's steps.
func TestWorkflowStatusListsLiveRuns(t *testing.T) {
	s := newFakeWorkflowStore()
	stubProcessControl(t, false, true) // tmux says: no runner session
	wf := s.addWorkflow("ship", "plan", "review")
	tk := s.addTask("t")
	s.addRun(wf.ID, tk.ID, workflow.RunRunning, 1, 2)
	s.addRun(wf.ID, tk.ID, workflow.RunDone, 1)

	out, err := runWorkflow(t, s, "status")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	for _, want := range []string{"ship", "#1", "running", "review", "runner gone"} {
		if !strings.Contains(out, want) {
			t.Errorf("status missing %q:\n%s", want, out)
		}
	}
	if strings.Count(out, "\n") != 1 {
		t.Errorf("only the live run should be listed:\n%s", out)
	}

	// With tmux unable to answer, nothing is claimed about the runner.
	runnerAlive = func(int64) (bool, bool) { return false, false }
	if out, _ = runWorkflow(t, s, "status"); strings.Contains(out, "runner gone") {
		t.Errorf("no tmux, no verdict on the runner:\n%s", out)
	}
}

func TestWorkflowStatusEmpty(t *testing.T) {
	out, err := runWorkflow(t, newFakeWorkflowStore(), "status")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !strings.Contains(out, "no live runs") {
		t.Errorf("status with nothing live should say so: %q", out)
	}
}

func TestWorkflowStatusShowsOneRun(t *testing.T) {
	s := newFakeWorkflowStore()
	stubProcessControl(t, true, true)
	wf := s.addWorkflow("ship", "plan", "review")
	s.setKind(2, workflow.StepGate)
	tk := s.addTask("add the thing")
	run := s.addRun(wf.ID, tk.ID, workflow.RunWaitingReview, 1, 2)

	out, err := runWorkflow(t, s, "status", "1")
	if err != nil {
		t.Fatalf("status 1: %v", err)
	}
	for _, want := range []string{
		"run 1: ship on #1 add the thing", "state: waiting_review", "cwd: /w",
		"plan", "done", "> 2", "review", "waiting review",
		"tend workflow approve 1", "the deliverable under review",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("status %d missing %q:\n%s", run.ID, want, out)
		}
	}

	// A failed run carries its reason.
	s.addRun(wf.ID, tk.ID, workflow.RunRunning, 1)
	if err := s.FailRun(context.Background(), 2, "step could not launch"); err != nil {
		t.Fatal(err)
	}
	if out, _ = runWorkflow(t, s, "status", "2"); !strings.Contains(out, "failed: step could not launch") {
		t.Errorf("a failed run should show its error:\n%s", out)
	}

	if _, err := runWorkflow(t, s, "status", "99"); err == nil {
		t.Error("an unknown run should be an error")
	}
}

// approve and reject settle the gate the run is waiting at with the same
// one-shot write the TUI and finish_step make. A plain approve records an
// empty deliverable so the runner passes the gate's input through (what
// the TUI's `a` writes); --feedback becomes the deliverable, which a
// forward edge hands on as the next step's input. A reject must carry
// non-blank feedback.
func TestWorkflowApproveAndReject(t *testing.T) {
	s := newFakeWorkflowStore()
	wf := s.addWorkflow("ship", "implement", "review")
	s.setKind(2, workflow.StepGate)
	s.edges = append(s.edges,
		workflow.Edge{ID: 1, FromStepID: 2, Outcome: workflow.OutcomeApprove, ToStepID: 1},
		workflow.Edge{ID: 2, FromStepID: 2, Outcome: workflow.OutcomeReject, ToStepID: 1},
	)
	tk := s.addTask("t")
	s.addRun(wf.ID, tk.ID, workflow.RunWaitingReview, 1, 2)
	s.addRun(wf.ID, tk.ID, workflow.RunWaitingReview, 1, 2)
	s.addRun(wf.ID, tk.ID, workflow.RunWaitingReview, 1, 2)

	out, err := runWorkflow(t, s, "approve", "1")
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	if !strings.Contains(out, "review approve") {
		t.Errorf("approve should name the gate and outcome: %q", out)
	}
	if strings.Contains(out, "with feedback") {
		t.Errorf("a plain approve should not claim feedback: %q", out)
	}
	if sr := s.stepRuns[1]; sr.Outcome != workflow.OutcomeApprove || !sr.Finished() || sr.Deliverable != "" {
		t.Errorf("gate step run = %+v, want approve finished with an empty deliverable so the input passes through", sr)
	}

	out, err = runWorkflow(t, s, "approve", "3", "--feedback", "ship it as is")
	if err != nil {
		t.Fatalf("approve --feedback: %v", err)
	}
	if !strings.Contains(out, "with feedback") {
		t.Errorf("approve --feedback should say it carried feedback: %q", out)
	}
	if sr := s.stepRuns[5]; sr.Outcome != workflow.OutcomeApprove || sr.Deliverable != "ship it as is" {
		t.Errorf("gate step run = %+v, want approve with the feedback as deliverable", sr)
	}

	if _, err := runWorkflow(t, s, "reject", "2"); err == nil {
		t.Fatal("reject without --feedback should fail")
	}
	if _, err := runWorkflow(t, s, "reject", "2", "--feedback", "  "); err == nil || !strings.Contains(err.Error(), "something in it") {
		t.Fatalf("reject with blank --feedback should be refused saying so, got %v", err)
	}
	if sr := s.stepRuns[3]; sr.Finished() {
		t.Fatalf("a refused reject must not touch the gate: %+v", sr)
	}
	out, err = runWorkflow(t, s, "reject", "2", "--feedback", "tests are missing")
	if err != nil {
		t.Fatalf("reject: %v", err)
	}
	if !strings.Contains(out, "with feedback") {
		t.Errorf("reject should say it carried feedback: %q", out)
	}
	if sr := s.stepRuns[3]; sr.Outcome != workflow.OutcomeReject || sr.Deliverable != "tests are missing" {
		t.Errorf("gate step run = %+v, want reject with the feedback as deliverable", sr)
	}

	// Deciding twice is refused: the first outcome stands.
	if _, err := runWorkflow(t, s, "approve", "1"); err == nil || !strings.Contains(err.Error(), "already decided") {
		t.Errorf("second decision should be refused naming the first, got %v", err)
	}
}

// A decision the gate's edges do not route is refused naming what they do,
// so it never silently ends the run.
func TestWorkflowApproveRefusesUnroutedOutcome(t *testing.T) {
	s := newFakeWorkflowStore()
	wf := s.addWorkflow("ship", "implement", "review")
	s.setKind(2, workflow.StepGate)
	s.edges = append(s.edges,
		workflow.Edge{ID: 1, FromStepID: 2, Outcome: "ship", ToStepID: 1},
		workflow.Edge{ID: 2, FromStepID: 2, Outcome: "revise", ToStepID: 1},
	)
	tk := s.addTask("t")
	s.addRun(wf.ID, tk.ID, workflow.RunWaitingReview, 1, 2)

	_, err := runWorkflow(t, s, "approve", "1")
	if err == nil {
		t.Fatal("approve should be refused")
	}
	for _, want := range []string{"routes", "ship", "revise"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should name %q", err, want)
		}
	}
	if s.stepRuns[1].Finished() {
		t.Error("a refused decision must not finish the gate")
	}
}

// A gate decision needs a run waiting at a gate.
func TestWorkflowApproveRefusesWhenNotAtAGate(t *testing.T) {
	s := newFakeWorkflowStore()
	wf := s.addWorkflow("ship", "implement", "review")
	s.setKind(2, workflow.StepGate)
	tk := s.addTask("t")
	s.addRun(wf.ID, tk.ID, workflow.RunRunning, 1)       // 1: an agent step, running
	s.addRun(wf.ID, tk.ID, workflow.RunPaused, 1, 2)     // 2: at the gate but paused
	s.addRun(wf.ID, tk.ID, workflow.RunCancelled, 1, 2)  // 3: ended
	s.addRun(wf.ID, tk.ID, workflow.RunWaitingReview, 1) // 4: waiting but current step is not a gate

	for id, want := range map[string]string{
		"1": "not waiting for review", "2": "paused", "3": "already cancelled", "4": "is not a gate",
	} {
		_, err := runWorkflow(t, s, "approve", id)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("approve %s: got %v, want an error mentioning %q", id, err, want)
		}
	}
	for _, sr := range s.stepRuns {
		if sr.Finished() && sr.Outcome != workflow.OutcomeDone {
			t.Errorf("a refused decision wrote %+v", sr)
		}
	}
}

// pause writes paused on a live run and is refused, saying why, otherwise.
func TestWorkflowPause(t *testing.T) {
	s := newFakeWorkflowStore()
	wf := s.addWorkflow("ship", "plan")
	tk := s.addTask("t")
	s.addRun(wf.ID, tk.ID, workflow.RunRunning, 1)
	s.addRun(wf.ID, tk.ID, workflow.RunWaitingReview, 1)
	s.addRun(wf.ID, tk.ID, workflow.RunPending)
	s.addRun(wf.ID, tk.ID, workflow.RunDone, 1)

	for _, id := range []string{"1", "2"} {
		out, err := runWorkflow(t, s, "pause", id)
		if err != nil {
			t.Fatalf("pause %s: %v", id, err)
		}
		if !strings.Contains(out, "tend workflow resume "+id) {
			t.Errorf("pause should say how to continue: %q", out)
		}
	}
	if s.runs[0].State != workflow.RunPaused || s.runs[1].State != workflow.RunPaused {
		t.Errorf("runs should be paused: %+v", s.runs[:2])
	}
	// Pausing again is a no-op, not an error.
	if out, err := runWorkflow(t, s, "pause", "1"); err != nil || !strings.Contains(out, "already paused") {
		t.Errorf("pause on a paused run: out=%q err=%v", out, err)
	}
	if _, err := runWorkflow(t, s, "pause", "3"); err == nil || !strings.Contains(err.Error(), "not been claimed") {
		t.Errorf("pausing a pending run should be refused, got %v", err)
	}
	if _, err := runWorkflow(t, s, "pause", "4"); err == nil || !strings.Contains(err.Error(), "already done") {
		t.Errorf("pausing an ended run should be refused, got %v", err)
	}
}

// cancel ends any live run; terminal is final.
func TestWorkflowCancel(t *testing.T) {
	s := newFakeWorkflowStore()
	wf := s.addWorkflow("ship", "plan")
	tk := s.addTask("t")
	s.addRun(wf.ID, tk.ID, workflow.RunRunning, 1)
	s.addRun(wf.ID, tk.ID, workflow.RunPaused, 1)
	s.addRun(wf.ID, tk.ID, workflow.RunPending)

	for _, id := range []string{"1", "2", "3"} {
		if _, err := runWorkflow(t, s, "cancel", id); err != nil {
			t.Fatalf("cancel %s: %v", id, err)
		}
	}
	for _, r := range s.runs {
		if r.State != workflow.RunCancelled || r.EndedAt == nil {
			t.Errorf("run %d = %s, want cancelled with ended_at", r.ID, r.State)
		}
	}
	if _, err := runWorkflow(t, s, "cancel", "1"); err == nil || !strings.Contains(err.Error(), "already cancelled") {
		t.Errorf("cancelling twice should be refused, got %v", err)
	}
}

// Every subcommand validates its run id before opening anything.
func TestWorkflowRunIDMustBePositive(t *testing.T) {
	s := newFakeWorkflowStore()
	for _, args := range [][]string{{"status", "x"}, {"approve", "0"}, {"pause", "-1"}, {"cancel", "abc"}} {
		if _, err := runWorkflow(t, s, args...); err == nil {
			t.Errorf("%v should fail on its run id", args)
		}
	}
}
