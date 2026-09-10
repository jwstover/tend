package runner

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jwstover/tend/internal/agent"
	"github.com/jwstover/tend/internal/store"
	"github.com/jwstover/tend/internal/task"
	"github.com/jwstover/tend/internal/workflow"
)

// fakeExec stands in for claude: handle decides what each attempt
// returns, and every request is recorded so a test can assert on the
// prompt, session id and resume flag the runner asked for.
type fakeExec struct {
	mu       sync.Mutex
	calls    []StepExec
	handle   func(ctx context.Context, req StepExec) (agent.HeadlessResult, error)
	checkErr error
}

func (f *fakeExec) Check() error { return f.checkErr }

func (f *fakeExec) Run(ctx context.Context, req StepExec) (agent.HeadlessResult, error) {
	f.mu.Lock()
	f.calls = append(f.calls, req)
	f.mu.Unlock()
	if f.handle == nil {
		return success("did: " + req.Prompt), nil
	}
	return f.handle(ctx, req)
}

func (f *fakeExec) requests() []StepExec {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]StepExec(nil), f.calls...)
}

func success(text string) agent.HeadlessResult {
	return agent.HeadlessResult{Found: true, Text: text, Subtype: "success", NumTurns: 1}
}

// promptTpl exposes every template variable so a rendered prompt shows
// what the runner passed in.
const promptTpl = "step on #{{.Task.ID}} in {{.Cwd}}; input=[{{.Input}}]; feedback=[{{.Feedback}}]; iter={{.Iteration}}; outcomes={{.Outcomes}}"

type fixture struct {
	t     *testing.T
	ctx   context.Context
	s     *store.Store
	exec  *fakeExec
	log   *bytes.Buffer
	tk    task.Task
	wf    workflow.Workflow
	steps map[string]workflow.Step
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	ctx := context.Background()
	s, err := store.Open(ctx, filepath.Join(t.TempDir(), "tend.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	tk, err := s.AddTask(ctx, "flaky scheduler test")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	wf, err := s.CreateWorkflow(ctx, "ship it", "")
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	return &fixture{t: t, ctx: ctx, s: s, exec: &fakeExec{}, log: &bytes.Buffer{}, tk: tk, wf: wf, steps: map[string]workflow.Step{}}
}

// step adds an agent step (or a gate, with kind) carrying promptTpl.
func (f *fixture) step(name string, kind workflow.StepKind) workflow.Step {
	f.t.Helper()
	st, err := f.s.AddStep(f.ctx, f.wf.ID, name, kind)
	if err != nil {
		f.t.Fatalf("AddStep(%s): %v", name, err)
	}
	if err := f.s.SetStepPrompt(f.ctx, st.ID, promptTpl); err != nil {
		f.t.Fatalf("SetStepPrompt: %v", err)
	}
	if err := f.s.SetStepPermissionMode(f.ctx, st.ID, "acceptEdits"); err != nil {
		f.t.Fatalf("SetStepPermissionMode: %v", err)
	}
	st.PromptMD, st.PermissionMode = promptTpl, "acceptEdits"
	f.steps[name] = st
	return st
}

func (f *fixture) edge(from, outcome, to string, maxIter *int64) {
	f.t.Helper()
	if _, err := f.s.SetEdge(f.ctx, f.steps[from].ID, outcome, f.steps[to].ID, maxIter); err != nil {
		f.t.Fatalf("SetEdge(%s -%s-> %s): %v", from, outcome, to, err)
	}
}

func (f *fixture) run() workflow.Run {
	f.t.Helper()
	run, err := f.s.CreateRun(f.ctx, f.wf.ID, f.tk.ID, "/tmp/repo")
	if err != nil {
		f.t.Fatalf("CreateRun: %v", err)
	}
	return run
}

func (f *fixture) runner() *Runner {
	return &Runner{Store: f.s, Exec: f.exec, Poll: 10 * time.Millisecond, Log: f.log}
}

func (f *fixture) getRun(id int64) workflow.Run {
	f.t.Helper()
	run, err := f.s.GetRun(f.ctx, id)
	if err != nil {
		f.t.Fatalf("GetRun: %v", err)
	}
	return run
}

func (f *fixture) stepRuns(runID int64) []workflow.StepRun {
	f.t.Helper()
	srs, err := f.s.ListStepRunsForRun(f.ctx, runID)
	if err != nil {
		f.t.Fatalf("ListStepRunsForRun: %v", err)
	}
	return srs
}

func (f *fixture) sessions() []task.Session {
	f.t.Helper()
	sessions, err := f.s.ListSessionsForTask(f.ctx, f.tk.ID)
	if err != nil {
		f.t.Fatalf("ListSessionsForTask: %v", err)
	}
	return sessions
}

// waitFor polls check for up to a few seconds -- the runner runs on its
// own goroutine in the pause/cancel tests.
func waitFor(t *testing.T, what string, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// The acceptance case: a two-step linear workflow runs headlessly to
// done, each step's deliverable recorded and the second step's prompt
// carrying the first's.
func TestRunTwoStepLinearWorkflow(t *testing.T) {
	f := newFixture(t)
	f.step("implement", workflow.StepAgent)
	f.step("ship", workflow.StepAgent)
	f.edge("implement", "done", "ship", nil)
	f.exec.handle = func(_ context.Context, req StepExec) (agent.HeadlessResult, error) {
		if strings.Contains(req.Prompt, "input=[]") {
			return success("PR #7"), nil
		}
		return success("merged"), nil
	}
	run := f.run()

	if err := f.runner().Run(f.ctx, run.ID, false); err != nil {
		t.Fatalf("Run: %v\n%s", err, f.log)
	}

	got := f.getRun(run.ID)
	if got.State != workflow.RunDone || got.EndedAt == nil || got.TmuxSession != "" {
		t.Errorf("run = %+v, want done", got)
	}
	srs := f.stepRuns(run.ID)
	if len(srs) != 2 {
		t.Fatalf("step runs = %+v, want two", srs)
	}
	impl, ship := srs[0], srs[1]
	if impl.StepID != f.steps["implement"].ID || !impl.Finished() || impl.Outcome != "done" || impl.Deliverable != "PR #7" {
		t.Errorf("implement step run = %+v, want finished done with PR #7", impl)
	}
	if ship.StepID != f.steps["ship"].ID || !ship.Finished() || ship.Outcome != "done" || ship.Deliverable != "merged" || ship.Input != "PR #7" {
		t.Errorf("ship step run = %+v, want finished done with merged, input PR #7", ship)
	}
	if got.CurrentStepRunID == nil || *got.CurrentStepRunID != ship.ID {
		t.Errorf("run.CurrentStepRunID = %v, want the last step run %d", got.CurrentStepRunID, ship.ID)
	}

	// What actually ran was recorded on the step run and handed to exec.
	reqs := f.exec.requests()
	if len(reqs) != 2 {
		t.Fatalf("exec calls = %d, want 2", len(reqs))
	}
	wantImpl := "step on #" + itoa(f.tk.ID) + " in /tmp/repo; input=[]; feedback=[]; iter=1; outcomes=[done]"
	if reqs[0].Prompt != wantImpl || impl.PromptRendered != wantImpl {
		t.Errorf("implement prompt = %q (recorded %q), want %q", reqs[0].Prompt, impl.PromptRendered, wantImpl)
	}
	wantShip := "step on #" + itoa(f.tk.ID) + " in /tmp/repo; input=[PR #7]; feedback=[]; iter=1; outcomes=[]"
	if reqs[1].Prompt != wantShip {
		t.Errorf("ship prompt = %q, want %q", reqs[1].Prompt, wantShip)
	}
	for i, req := range reqs {
		if req.Resume || req.Run.ID != run.ID || req.TaskID != f.tk.ID || req.StepRun.PermissionMode != "acceptEdits" {
			t.Errorf("exec call %d = %+v, want a fresh attempt on this run with the step's permission mode", i, req)
		}
		if req.StepRun.LogPath == "" || req.StepRun.SessionExternalID == "" {
			t.Errorf("exec call %d has no log path or session id: %+v", i, req.StepRun)
		}
	}
	want, _ := agent.StepLogPath(run.ID, impl.ID)
	if impl.LogPath != want {
		t.Errorf("implement log path = %q, want %q (stored before the step ran)", impl.LogPath, want)
	}

	// The hand-off contract rides in the system prompt, built per step
	// and recorded on the row; the author's prompt is left alone.
	wantSys := workflow.StepSystemPrompt(workflow.HandoffContext{Workflow: "ship it", Step: "implement", Iteration: 1, Outcomes: []string{"done"}})
	if reqs[0].StepRun.SystemPrompt != wantSys || impl.SystemPrompt != wantSys {
		t.Errorf("implement system prompt = %q (recorded %q), want %q", reqs[0].StepRun.SystemPrompt, impl.SystemPrompt, wantSys)
	}
	if !strings.Contains(ship.SystemPrompt, `step "ship"`) || !strings.Contains(ship.SystemPrompt, `"done"`) {
		t.Errorf("ship system prompt = %q, want it to name the step and offer done", ship.SystemPrompt)
	}

	// One session row per agent step, bound to its step run, ended.
	sessions := f.sessions()
	if len(sessions) != 2 {
		t.Fatalf("sessions = %+v, want one per step", sessions)
	}
	for _, sess := range sessions {
		if sess.StepRunID == nil || sess.TmuxSession != "" || sess.Status != task.SessionEnded || sess.Cwd != "/tmp/repo" {
			t.Errorf("session = %+v, want bound to a step run, no tmux, ended, in the run's cwd", sess)
		}
		if !strings.Contains(sess.Label, "flaky scheduler test") {
			t.Errorf("session label = %q, want the step and task named", sess.Label)
		}
	}
	if !strings.Contains(f.log.String(), "done") {
		t.Errorf("runner log missing the ending:\n%s", f.log)
	}
}

// While claude runs, the step's session row reads 'working': it has no
// tmux pane for the TUI's poller to classify, so the runner is the only
// thing that can say so. After the step it reads 'ended' as before.
func TestRunMarksStepSessionWorkingDuringExec(t *testing.T) {
	f := newFixture(t)
	f.step("implement", workflow.StepAgent)
	var midRun task.SessionStatus
	f.exec.handle = func(_ context.Context, req StepExec) (agent.HeadlessResult, error) {
		for _, sess := range f.sessions() {
			if sess.ExternalID == req.StepRun.SessionExternalID {
				midRun = sess.Status
			}
		}
		return success("ok"), nil
	}
	run := f.run()

	if err := f.runner().Run(f.ctx, run.ID, false); err != nil {
		t.Fatalf("Run: %v\n%s", err, f.log)
	}
	if midRun != task.SessionWorking {
		t.Errorf("session status during the step = %q, want working", midRun)
	}
	if sessions := f.sessions(); len(sessions) != 1 || sessions[0].Status != task.SessionEnded {
		t.Errorf("sessions after the step = %+v, want one, ended", sessions)
	}
}

// An outcome the agent hands off through finish_step is followed, even
// when the stream's own text says something else.
func TestRunFollowsFinishStepOutcome(t *testing.T) {
	f := newFixture(t)
	f.step("implement", workflow.StepAgent)
	f.step("review", workflow.StepAgent)
	f.step("ship", workflow.StepAgent)
	f.edge("implement", "done", "review", nil)
	f.edge("review", "approve", "ship", nil)
	f.edge("review", "reject", "implement", nil)
	f.exec.handle = func(_ context.Context, req StepExec) (agent.HeadlessResult, error) {
		if req.StepRun.StepID == f.steps["review"].ID {
			if err := f.s.FinishStepRun(f.ctx, req.StepRun.ID, "Approve", "LGTM"); err != nil {
				t.Errorf("FinishStepRun from the agent: %v", err)
			}
			return success("I called finish_step."), nil
		}
		return success("ok"), nil
	}
	run := f.run()

	if err := f.runner().Run(f.ctx, run.ID, false); err != nil {
		t.Fatalf("Run: %v\n%s", err, f.log)
	}
	srs := f.stepRuns(run.ID)
	if len(srs) != 3 || srs[1].Outcome != "approve" || srs[1].Deliverable != "LGTM" || srs[2].StepID != f.steps["ship"].ID {
		t.Errorf("step runs = %+v, want implement, review(approve/LGTM), ship", srs)
	}
	if srs[2].Input != "LGTM" {
		t.Errorf("ship input = %q, want the review's deliverable", srs[2].Input)
	}
	reqs := f.exec.requests()
	if want := "outcomes=[approve reject]"; !strings.Contains(reqs[1].Prompt, want) {
		t.Errorf("review prompt = %q, want it to list %s", reqs[1].Prompt, want)
	}
}

// A reject edge loops back with the reviewer's deliverable as Feedback,
// the original Input kept, and stops at the edge's max_iterations with a
// message that says so.
func TestRunLoopsBackWithFeedbackUntilMaxIterations(t *testing.T) {
	f := newFixture(t)
	f.step("implement", workflow.StepAgent)
	f.step("review", workflow.StepAgent)
	f.edge("implement", "done", "review", nil)
	two := int64(2)
	f.edge("review", "reject", "implement", &two)
	f.exec.handle = func(_ context.Context, req StepExec) (agent.HeadlessResult, error) {
		if req.StepRun.StepID == f.steps["review"].ID {
			if err := f.s.FinishStepRun(f.ctx, req.StepRun.ID, "reject", "needs tests"); err != nil {
				t.Errorf("FinishStepRun: %v", err)
			}
			return success("rejected"), nil
		}
		return success("PR #7"), nil
	}
	run := f.run()

	err := f.runner().Run(f.ctx, run.ID, false)
	if !errors.Is(err, ErrRunFailed) {
		t.Fatalf("Run = %v, want ErrRunFailed\n%s", err, f.log)
	}
	got := f.getRun(run.ID)
	if got.State != workflow.RunFailed || !strings.Contains(got.Error, `step "implement" would run for the 3rd time`) ||
		!strings.Contains(got.Error, "the edge allows 2") {
		t.Errorf("run = %+v, want failed with the max-iterations message", got)
	}
	srs := f.stepRuns(run.ID)
	if len(srs) != 4 {
		t.Fatalf("step runs = %d, want implement, review, implement, review", len(srs))
	}
	second := srs[2]
	if second.StepID != f.steps["implement"].ID || second.Iteration != 2 || second.Input != "" {
		t.Errorf("second implement = %+v, want iteration 2 with the original (empty) input", second)
	}
	// Feedback is on the row, not just in the prompt, so get_workflow_step
	// can read it back.
	if second.Feedback != "needs tests" || srs[0].Feedback != "" {
		t.Errorf("feedback: second implement = %q (want \"needs tests\"), first = %q (want empty)", second.Feedback, srs[0].Feedback)
	}
	reqs := f.exec.requests()
	if want := "input=[]; feedback=[needs tests]; iter=2"; !strings.Contains(reqs[2].Prompt, want) {
		t.Errorf("second implement prompt = %q, want %s", reqs[2].Prompt, want)
	}
}

// A gate parks the run in waiting_review until a decision lands on its
// step run, then routes on it; an approve with no feedback passes the
// gate's input through to the next step.
func TestRunWaitsAtGate(t *testing.T) {
	f := newFixture(t)
	f.step("implement", workflow.StepAgent)
	gate := f.step("gate", workflow.StepGate)
	f.step("ship", workflow.StepAgent)
	f.edge("implement", "done", "gate", nil)
	f.edge("gate", "approve", "ship", nil)
	f.edge("gate", "reject", "implement", nil)
	f.exec.handle = func(_ context.Context, req StepExec) (agent.HeadlessResult, error) {
		return success("PR #7"), nil
	}
	run := f.run()

	done := make(chan error, 1)
	go func() { done <- f.runner().Run(f.ctx, run.ID, false) }()

	waitFor(t, "run waiting at the gate", func() bool {
		return f.getRun(run.ID).State == workflow.RunWaitingReview
	})
	srs := f.stepRuns(run.ID)
	if len(srs) != 2 || srs[1].StepID != gate.ID || srs[1].Finished() || srs[1].SessionExternalID != "" || srs[1].SystemPrompt != "" {
		t.Fatalf("step runs at the gate = %+v, want implement done and an unfinished gate with no session or system prompt", srs)
	}
	if want := "input=[PR #7]"; !strings.Contains(srs[1].PromptRendered, want) {
		t.Errorf("gate reviewer text = %q, want it rendered with %s", srs[1].PromptRendered, want)
	}
	if len(f.exec.requests()) != 1 {
		t.Errorf("exec was called %d times by the gate; a gate launches no claude", len(f.exec.requests())-1)
	}
	if len(f.sessions()) != 1 {
		t.Errorf("sessions = %d, want only the agent step's", len(f.sessions()))
	}

	// The TUI/CLI decides.
	if err := f.s.FinishStepRun(f.ctx, srs[1].ID, "approve", ""); err != nil {
		t.Fatalf("FinishStepRun(gate): %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v\n%s", err, f.log)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("runner did not finish after the gate was approved\n%s", f.log)
	}
	if got := f.getRun(run.ID); got.State != workflow.RunDone {
		t.Errorf("run = %+v, want done", got)
	}
	srs = f.stepRuns(run.ID)
	if len(srs) != 3 || srs[2].StepID != f.steps["ship"].ID || srs[2].Input != "PR #7" {
		t.Errorf("step runs = %+v, want ship after the gate with the gate's input passed through", srs)
	}
}

// blockingExec makes an attempt hang until its ctx is cancelled, the way
// a live claude does, reporting when it has started.
func blockingExec(started chan<- StepExec) func(ctx context.Context, req StepExec) (agent.HeadlessResult, error) {
	return func(ctx context.Context, req StepExec) (agent.HeadlessResult, error) {
		started <- req
		<-ctx.Done()
		return agent.HeadlessResult{}, agent.ErrKilled
	}
}

// Cancelling the run from outside stops the step and the runner exits
// cleanly, leaving the run cancelled and the step run unfinished.
func TestRunStopsWhenCancelled(t *testing.T) {
	f := newFixture(t)
	f.step("implement", workflow.StepAgent)
	started := make(chan StepExec, 1)
	f.exec.handle = blockingExec(started)
	run := f.run()

	done := make(chan error, 1)
	go func() { done <- f.runner().Run(f.ctx, run.ID, false) }()
	<-started
	if err := f.s.SetRunState(f.ctx, run.ID, workflow.RunCancelled); err != nil {
		t.Fatalf("SetRunState(cancelled): %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run after cancel = %v, want nil\n%s", err, f.log)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("runner did not stop after the run was cancelled\n%s", f.log)
	}
	if got := f.getRun(run.ID); got.State != workflow.RunCancelled {
		t.Errorf("run = %+v, want still cancelled", got)
	}
	if srs := f.stepRuns(run.ID); len(srs) != 1 || srs[0].Finished() {
		t.Errorf("step runs = %+v, want the one interrupted step, unfinished", srs)
	}
}

// Pausing stops the step with its session intact; resuming the run
// continues that session with the resume prompt and finishes the run.
func TestRunPausesAndResumesTheStepSession(t *testing.T) {
	f := newFixture(t)
	f.step("implement", workflow.StepAgent)
	f.step("ship", workflow.StepAgent)
	f.edge("implement", "done", "ship", nil)
	started := make(chan StepExec, 1)
	f.exec.handle = blockingExec(started)
	run := f.run()

	done := make(chan error, 1)
	go func() { done <- f.runner().Run(f.ctx, run.ID, false) }()
	first := <-started
	if err := f.s.SetRunState(f.ctx, run.ID, workflow.RunPaused); err != nil {
		t.Fatalf("SetRunState(paused): %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("Run after pause = %v, want nil\n%s", err, f.log)
	}
	if got := f.getRun(run.ID); got.State != workflow.RunPaused || got.CurrentStepRunID == nil {
		t.Fatalf("run = %+v, want paused at its step", got)
	}

	// Resume: the paused run is claimable, and the step is continued.
	f.exec.handle = func(_ context.Context, req StepExec) (agent.HeadlessResult, error) {
		return success("finished " + req.Prompt[:8]), nil
	}
	if err := f.runner().Run(f.ctx, run.ID, false); err != nil {
		t.Fatalf("Run (resume) = %v\n%s", err, f.log)
	}
	reqs := f.exec.requests()
	if len(reqs) != 3 {
		t.Fatalf("exec calls = %d, want the interrupted attempt, its resume, then ship", len(reqs))
	}
	resumed := reqs[1]
	if !resumed.Resume || resumed.Prompt != ResumePrompt || resumed.StepRun.ID != first.StepRun.ID ||
		resumed.StepRun.SessionExternalID != first.StepRun.SessionExternalID {
		t.Errorf("resume attempt = %+v, want the same step run and session continued with ResumePrompt", resumed)
	}
	if reqs[2].Resume || reqs[2].StepRun.StepID != f.steps["ship"].ID {
		t.Errorf("third attempt = %+v, want a fresh ship step", reqs[2])
	}
	if got := f.getRun(run.ID); got.State != workflow.RunDone {
		t.Errorf("run = %+v, want done after the resume", got)
	}
	if srs := f.stepRuns(run.ID); len(srs) != 2 || srs[0].Iteration != 1 {
		t.Errorf("step runs = %+v, want two, the resumed step still iteration 1", srs)
	}
}

// crashed sets up a run whose runner died mid-step: running, with an
// unfinished current step run and a session row, exactly as the runner
// leaves it when killed.
func (f *fixture) crashed(t *testing.T) (workflow.Run, workflow.StepRun) {
	t.Helper()
	f.step("implement", workflow.StepAgent)
	f.step("ship", workflow.StepAgent)
	f.edge("implement", "done", "ship", nil)
	run := f.run()
	if ok, err := f.s.ClaimRun(f.ctx, run.ID); err != nil || !ok {
		t.Fatalf("ClaimRun = (%v, %v)", ok, err)
	}
	sr, err := f.s.CreateStepRun(f.ctx, workflow.StepRun{
		RunID: run.ID, StepID: f.steps["implement"].ID, SessionExternalID: "sess-crashed",
		PromptRendered: "the original prompt", PermissionMode: "acceptEdits",
	})
	if err != nil {
		t.Fatalf("CreateStepRun: %v", err)
	}
	logPath, _ := agent.StepLogPath(run.ID, sr.ID)
	if err := f.s.SetStepRunLogPath(f.ctx, sr.ID, logPath); err != nil {
		t.Fatalf("SetStepRunLogPath: %v", err)
	}
	sr.LogPath = logPath
	if _, err := f.s.CreateStepRunSession(f.ctx, sr.ID, f.tk.ID, "sess-crashed", run.Cwd, "implement", ""); err != nil {
		t.Fatalf("CreateStepRunSession: %v", err)
	}
	return f.getRun(run.ID), sr
}

// A run left running by a dead runner is refused without takeover and
// re-entered with it; when the step's log already holds a result, the
// step is settled from the log without running claude again.
func TestRunTakeoverSettlesFromExistingLog(t *testing.T) {
	f := newFixture(t)
	run, sr := f.crashed(t)
	if err := os.MkdirAll(filepath.Dir(sr.LogPath), 0o755); err != nil {
		t.Fatal(err)
	}
	logLine := `{"type":"result","subtype":"success","is_error":false,"result":"PR #7 from before the crash","permission_denials":[]}` + "\n"
	if err := os.WriteFile(sr.LogPath, []byte(logLine), 0o644); err != nil {
		t.Fatal(err)
	}

	err := f.runner().Run(f.ctx, run.ID, false)
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("Run without takeover = %v, want ErrAlreadyRunning", err)
	}
	if err := f.runner().Run(f.ctx, run.ID, true); err != nil {
		t.Fatalf("Run with takeover = %v\n%s", err, f.log)
	}
	srs := f.stepRuns(run.ID)
	if len(srs) != 2 || srs[0].Deliverable != "PR #7 from before the crash" || srs[1].Input != "PR #7 from before the crash" {
		t.Errorf("step runs = %+v, want implement settled from its log and ship fed from it", srs)
	}
	reqs := f.exec.requests()
	if len(reqs) != 1 || reqs[0].StepRun.StepID != f.steps["ship"].ID {
		t.Errorf("exec calls = %+v, want only the ship step (implement came from the log)", reqs)
	}
	if got := f.getRun(run.ID); got.State != workflow.RunDone {
		t.Errorf("run = %+v, want done", got)
	}
}

// With no result in the log the crashed step's session is continued; if
// that session cannot be continued the step is started over under a new
// session id on the same step run.
func TestRunTakeoverResumesThenRestartsTheStep(t *testing.T) {
	f := newFixture(t)
	run, sr := f.crashed(t)
	f.exec.handle = func(_ context.Context, req StepExec) (agent.HeadlessResult, error) {
		if req.Resume {
			return agent.HeadlessResult{}, errors.New("No conversation found with session ID: sess-crashed")
		}
		return success("done fresh: " + req.Prompt), nil
	}

	if err := f.runner().Run(f.ctx, run.ID, true); err != nil {
		t.Fatalf("Run with takeover = %v\n%s", err, f.log)
	}
	reqs := f.exec.requests()
	if len(reqs) != 3 {
		t.Fatalf("exec calls = %d, want resume, fresh restart, ship", len(reqs))
	}
	if !reqs[0].Resume || reqs[0].StepRun.SessionExternalID != "sess-crashed" || reqs[0].Prompt != ResumePrompt {
		t.Errorf("first attempt = %+v, want the crashed session resumed", reqs[0])
	}
	restart := reqs[1]
	if restart.Resume || restart.StepRun.ID != sr.ID || restart.Prompt != "the original prompt" || restart.StepRun.SessionExternalID == "sess-crashed" {
		t.Errorf("restart = %+v, want the same step run started over with its recorded prompt under a new session", restart)
	}
	srs := f.stepRuns(run.ID)
	if len(srs) != 2 || srs[0].Iteration != 1 || srs[0].SessionExternalID != restart.StepRun.SessionExternalID ||
		srs[0].Deliverable != "done fresh: the original prompt" {
		t.Errorf("step runs = %+v, want implement (still iteration 1) on the new session, then ship", srs)
	}
	if sessions := f.sessions(); len(sessions) != 3 {
		t.Errorf("sessions = %d, want the crashed one, its replacement, and ship's", len(sessions))
	}
}

// Killing the runner itself (its ctx ending) leaves the run running and
// the step unfinished, for a resume to pick up.
func TestRunKilledLeavesRunForResume(t *testing.T) {
	f := newFixture(t)
	f.step("implement", workflow.StepAgent)
	started := make(chan StepExec, 1)
	f.exec.handle = blockingExec(started)
	run := f.run()

	ctx, cancel := context.WithCancel(f.ctx)
	done := make(chan error, 1)
	go func() { done <- f.runner().Run(ctx, run.ID, false) }()
	<-started
	cancel()
	err := <-done
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run after kill = %v, want context.Canceled\n%s", err, f.log)
	}
	if got := f.getRun(run.ID); got.State != workflow.RunRunning || got.CurrentStepRunID == nil {
		t.Errorf("run = %+v, want left running at its step", got)
	}
	if srs := f.stepRuns(run.ID); len(srs) != 1 || srs[0].Finished() {
		t.Errorf("step runs = %+v, want the interrupted step unfinished", srs)
	}
	if !strings.Contains(f.log.String(), "tend workflow resume") {
		t.Errorf("runner log should say how to resume:\n%s", f.log)
	}
}

func TestRunFailsLoudly(t *testing.T) {
	cases := []struct {
		name  string
		setup func(f *fixture)
		want  string
	}{
		{
			name: "permission denials on a success",
			setup: func(f *fixture) {
				f.step("implement", workflow.StepAgent)
				f.exec.handle = func(_ context.Context, _ StepExec) (agent.HeadlessResult, error) {
					res := success("Permission is pending.")
					res.PermissionDenials = 2
					return res, nil
				}
			},
			want: "2 tool call(s) were denied",
		},
		{
			name: "claude reports an error",
			setup: func(f *fixture) {
				f.step("implement", workflow.StepAgent)
				f.exec.handle = func(_ context.Context, _ StepExec) (agent.HeadlessResult, error) {
					return agent.HeadlessResult{Found: true, Subtype: "error_max_turns", IsError: true, Text: "ran out"}, nil
				}
			},
			want: "error_max_turns",
		},
		{
			name: "no result at all",
			setup: func(f *fixture) {
				f.step("implement", workflow.StepAgent)
				f.exec.handle = func(_ context.Context, _ StepExec) (agent.HeadlessResult, error) {
					return agent.HeadlessResult{}, errors.New("exit status 1: bad flag")
				}
			},
			want: "bad flag",
		},
		{
			name: "prompt does not render",
			setup: func(f *fixture) {
				st := f.step("implement", workflow.StepAgent)
				if err := f.s.SetStepPrompt(f.ctx, st.ID, "Fix {{.Task.Nope}}"); err != nil {
					f.t.Fatal(err)
				}
			},
			want: `step "implement" prompt`,
		},
		{
			name:  "workflow has no steps",
			setup: func(f *fixture) {},
			want:  "has no steps",
		},
		{
			name: "claude is not installed",
			setup: func(f *fixture) {
				f.step("implement", workflow.StepAgent)
				f.exec.checkErr = agent.ErrNotFound
			},
			want: "not found on $PATH",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			tc.setup(f)
			run := f.run()
			err := f.runner().Run(f.ctx, run.ID, false)
			if !errors.Is(err, ErrRunFailed) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Run = %v, want ErrRunFailed mentioning %q", err, tc.want)
			}
			got := f.getRun(run.ID)
			if got.State != workflow.RunFailed || !strings.Contains(got.Error, tc.want) {
				t.Errorf("run = %+v, want failed with %q on the row", got, tc.want)
			}
		})
	}
}

// An ended run cannot be driven again, with or without takeover.
func TestRunRefusesEndedRun(t *testing.T) {
	f := newFixture(t)
	f.step("implement", workflow.StepAgent)
	run := f.run()
	if err := f.s.SetRunState(f.ctx, run.ID, workflow.RunCancelled); err != nil {
		t.Fatal(err)
	}
	for _, takeover := range []bool{false, true} {
		if err := f.runner().Run(f.ctx, run.ID, takeover); !errors.Is(err, workflow.ErrRunEnded) {
			t.Errorf("Run(takeover=%v) on a cancelled run = %v, want ErrRunEnded", takeover, err)
		}
	}
	if len(f.exec.requests()) != 0 {
		t.Error("exec was called for an ended run")
	}
}

// A recorded outcome with no matching edge ends the run rather than
// failing it -- the authored contract is "no edge means done" -- and the
// log says which outcomes the step did route. (finish_step refuses such
// an outcome, so this is the TUI/CLI writing a gate-style decision
// straight to the row.)
func TestRunEndsOnUnroutedOutcome(t *testing.T) {
	f := newFixture(t)
	f.step("review", workflow.StepAgent)
	f.step("ship", workflow.StepAgent)
	f.edge("review", "approve", "ship", nil)
	f.exec.handle = func(_ context.Context, req StepExec) (agent.HeadlessResult, error) {
		if err := f.s.FinishStepRun(f.ctx, req.StepRun.ID, "skip", ""); err != nil {
			t.Errorf("FinishStepRun: %v", err)
		}
		return success("skipped"), nil
	}
	run := f.run()

	if err := f.runner().Run(f.ctx, run.ID, false); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := f.getRun(run.ID); got.State != workflow.RunDone {
		t.Errorf("run = %+v, want done", got)
	}
	if len(f.exec.requests()) != 1 {
		t.Errorf("exec calls = %d, want just the review", len(f.exec.requests()))
	}
	if !strings.Contains(f.log.String(), `outcome "skip" has no edge`) {
		t.Errorf("runner log should flag the unrouted outcome:\n%s", f.log)
	}
}

// A step that routes more than done and exits without finish_step is
// not guessed at: its session gets one nudge turn (same step run, same
// session, no new iteration), and the outcome it then hands off is
// followed.
func TestRunNudgesOnceForMissingHandoff(t *testing.T) {
	f := newFixture(t)
	f.step("implement", workflow.StepAgent)
	f.step("review", workflow.StepAgent)
	f.step("ship", workflow.StepAgent)
	f.edge("implement", "done", "review", nil)
	f.edge("review", "approve", "ship", nil)
	f.edge("review", "reject", "implement", nil)
	f.exec.handle = func(_ context.Context, req StepExec) (agent.HeadlessResult, error) {
		if req.StepRun.StepID != f.steps["review"].ID {
			return success("ok"), nil
		}
		if !req.Resume {
			// First attempt: the verdict is in the text, not in finish_step.
			return success("LGTM, approving."), nil
		}
		if err := f.s.FinishStepRun(f.ctx, req.StepRun.ID, "approve", "LGTM"); err != nil {
			t.Errorf("FinishStepRun from the nudged agent: %v", err)
		}
		return success("Called finish_step."), nil
	}
	run := f.run()

	if err := f.runner().Run(f.ctx, run.ID, false); err != nil {
		t.Fatalf("Run: %v\n%s", err, f.log)
	}
	if got := f.getRun(run.ID); got.State != workflow.RunDone {
		t.Errorf("run = %+v, want done", got)
	}
	reqs := f.exec.requests()
	if len(reqs) != 4 {
		t.Fatalf("exec calls = %d, want implement, review, the nudge, ship", len(reqs))
	}
	first, nudge := reqs[1], reqs[2]
	if !nudge.Resume || nudge.StepRun.ID != first.StepRun.ID || nudge.StepRun.SessionExternalID != first.StepRun.SessionExternalID {
		t.Errorf("nudge = %+v, want the review's own session resumed on the same step run", nudge)
	}
	if want := workflow.NudgePrompt("review", []string{"approve", "reject"}); nudge.Prompt != want {
		t.Errorf("nudge prompt = %q, want %q", nudge.Prompt, want)
	}
	if nudge.StepRun.SystemPrompt == "" || nudge.StepRun.SystemPrompt != first.StepRun.SystemPrompt {
		t.Errorf("nudge system prompt = %q, want the step's own block carried on the resume", nudge.StepRun.SystemPrompt)
	}
	srs := f.stepRuns(run.ID)
	if len(srs) != 3 {
		t.Fatalf("step runs = %+v, want implement, review, ship: the nudge is not a step run", srs)
	}
	review := srs[1]
	if review.Iteration != 1 || review.Outcome != "approve" || review.Deliverable != "LGTM" {
		t.Errorf("review step run = %+v, want iteration 1 finished approve/LGTM", review)
	}
	if srs[2].StepID != f.steps["ship"].ID || srs[2].Input != "LGTM" {
		t.Errorf("ship step run = %+v, want fed from the nudged hand-off", srs[2])
	}
	if sessions := f.sessions(); len(sessions) != 3 {
		t.Errorf("sessions = %d, want one per step; the nudge reuses the review's", len(sessions))
	}
	if !strings.Contains(f.log.String(), "asked once to hand off") {
		t.Errorf("runner log should record the nudge:\n%s", f.log)
	}
}

// A step that stays silent through the nudge fails the run with a
// message naming the step and what it was supposed to return; nothing is
// recorded as "done" on its behalf.
func TestRunFailsWhenHandoffStaysMissing(t *testing.T) {
	f := newFixture(t)
	f.step("review", workflow.StepAgent)
	f.step("ship", workflow.StepAgent)
	f.edge("review", "approve", "ship", nil)
	f.edge("review", "reject", "review", nil)
	f.exec.handle = func(_ context.Context, _ StepExec) (agent.HeadlessResult, error) {
		return success("I tried `claude mcp call` but it did not work."), nil
	}
	run := f.run()

	err := f.runner().Run(f.ctx, run.ID, false)
	if !errors.Is(err, ErrRunFailed) {
		t.Fatalf("Run = %v, want ErrRunFailed\n%s", err, f.log)
	}
	got := f.getRun(run.ID)
	if got.State != workflow.RunFailed || !strings.Contains(got.Error, `step "review"`) ||
		!strings.Contains(got.Error, "finish_step") || !strings.Contains(got.Error, "approve, reject") {
		t.Errorf("run = %+v, want failed naming the step, finish_step and the outcomes it routes", got)
	}
	if reqs := f.exec.requests(); len(reqs) != 2 || !reqs[1].Resume {
		t.Errorf("exec calls = %+v, want the attempt and exactly one nudge", reqs)
	}
	if srs := f.stepRuns(run.ID); len(srs) != 1 || srs[0].Finished() {
		t.Errorf("step runs = %+v, want the one review, left unfinished rather than guessed done", srs)
	}
}

// The stdout fallback survives where it is safe: a step whose only
// outcome is done (a single done edge, or no edges at all) is settled
// from its final text without a nudge.
func TestRunFallsBackToTextForDoneOnlySteps(t *testing.T) {
	f := newFixture(t)
	f.step("implement", workflow.StepAgent)
	f.step("ship", workflow.StepAgent)
	f.edge("implement", "done", "ship", nil)
	run := f.run()

	if err := f.runner().Run(f.ctx, run.ID, false); err != nil {
		t.Fatalf("Run: %v\n%s", err, f.log)
	}
	reqs := f.exec.requests()
	if len(reqs) != 2 || reqs[0].Resume || reqs[1].Resume {
		t.Errorf("exec calls = %+v, want two fresh attempts and no nudge", reqs)
	}
	srs := f.stepRuns(run.ID)
	if len(srs) != 2 || srs[0].Outcome != "done" || !strings.HasPrefix(srs[0].Deliverable, "did: ") ||
		srs[1].Outcome != "done" || !strings.HasPrefix(srs[1].Deliverable, "did: ") {
		t.Errorf("step runs = %+v, want both settled done from their final text", srs)
	}
}

// A crash resume that finds the step's result in its log still enforces
// the hand-off: the settled-from-log path nudges too.
func TestRunTakeoverNudgesWhenLoggedResultHasNoHandoff(t *testing.T) {
	f := newFixture(t)
	f.step("review", workflow.StepAgent)
	f.step("ship", workflow.StepAgent)
	f.edge("review", "approve", "ship", nil)
	f.edge("review", "reject", "review", nil)
	run := f.run()
	if ok, err := f.s.ClaimRun(f.ctx, run.ID); err != nil || !ok {
		t.Fatalf("ClaimRun = (%v, %v)", ok, err)
	}
	sr, err := f.s.CreateStepRun(f.ctx, workflow.StepRun{RunID: run.ID, StepID: f.steps["review"].ID, SessionExternalID: "sess-crashed"})
	if err != nil {
		t.Fatalf("CreateStepRun: %v", err)
	}
	logPath, _ := agent.StepLogPath(run.ID, sr.ID)
	if err := f.s.SetStepRunLogPath(f.ctx, sr.ID, logPath); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, []byte(`{"type":"result","subtype":"success","result":"Approved in prose only"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.exec.handle = func(_ context.Context, req StepExec) (agent.HeadlessResult, error) {
		if req.Resume {
			if err := f.s.FinishStepRun(f.ctx, req.StepRun.ID, "approve", "LGTM"); err != nil {
				t.Errorf("FinishStepRun: %v", err)
			}
		}
		return success("ok"), nil
	}

	if err := f.runner().Run(f.ctx, run.ID, true); err != nil {
		t.Fatalf("Run with takeover = %v\n%s", err, f.log)
	}
	reqs := f.exec.requests()
	if len(reqs) != 2 || !reqs[0].Resume || reqs[0].StepRun.SessionExternalID != "sess-crashed" || reqs[1].Resume {
		t.Errorf("exec calls = %+v, want the nudge on the crashed session, then a fresh ship", reqs)
	}
	if srs := f.stepRuns(run.ID); len(srs) != 2 || srs[0].Outcome != "approve" || srs[1].Input != "LGTM" {
		t.Errorf("step runs = %+v, want review approved via the nudge and ship fed from it", srs)
	}
}

func TestOrdinal(t *testing.T) {
	for n, want := range map[int]string{1: "1st", 2: "2nd", 3: "3rd", 4: "4th", 11: "11th", 12: "12th", 13: "13th", 21: "21st", 22: "22nd", 103: "103rd", 111: "111th"} {
		if got := ordinal(n); got != want {
			t.Errorf("ordinal(%d) = %q, want %q", n, got, want)
		}
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
