package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jwstover/tend/internal/store"
	"github.com/jwstover/tend/internal/workflow"
)

// typeText feeds each rune as a key press.
func typeText(t *testing.T, m tea.Model, text string) tea.Model {
	t.Helper()
	for _, r := range text {
		m = drive(t, m, keyPress(r))
	}
	return m
}

func enter() tea.KeyPressMsg { return tea.KeyPressMsg{Code: tea.KeyEnter} }
func esc() tea.KeyPressMsg   { return tea.KeyPressMsg{Code: tea.KeyEscape} }

// waitForWorkflows polls until the store's workflow list satisfies check.
func waitForWorkflows(t *testing.T, s *store.Store, what string, check func([]workflow.Workflow) bool) []workflow.Workflow {
	t.Helper()
	var got []workflow.Workflow
	waitFor(t, what, func() bool {
		var err error
		got, err = s.ListWorkflows(context.Background())
		return err == nil && check(got)
	})
	return got
}

// waitForSteps polls until a workflow's steps satisfy check.
func waitForSteps(t *testing.T, s *store.Store, workflowID int64, what string, check func([]workflow.Step) bool) []workflow.Step {
	t.Helper()
	var got []workflow.Step
	waitFor(t, what, func() bool {
		var err error
		got, err = s.ListSteps(context.Background(), workflowID)
		return err == nil && check(got)
	})
	return got
}

// reloadWorkflows runs the view's load synchronously and applies its
// result, so the model reflects the store before the next key press or
// assertion. A mutation's own follow-up reload runs through collect and
// can be abandoned on a slow runner; calling the Cmd directly cannot be.
func reloadWorkflows(t *testing.T, m tea.Model, workflowID int64) tea.Model {
	t.Helper()
	return drive(t, m, m.(app).loadWorkflows(workflowID)())
}

// openWorkflows enters the view and waits for the initial load.
func openWorkflows(t *testing.T, m tea.Model) tea.Model {
	t.Helper()
	m = drive(t, m, keyPress('W'))
	if m.(app).mode != modeWorkflows {
		t.Fatalf("mode after W = %v, want modeWorkflows", m.(app).mode)
	}
	return m
}

// TestWorkflowsAuthoringRoundTrip is the task's acceptance test: create a
// workflow with one step and a prompt entirely from the TUI, and read it
// back from the store.
func TestWorkflowsAuthoringRoundTrip(t *testing.T) {
	m, s := newTestApp(t)
	m = openWorkflows(t, m)

	content := ansi.Strip(m.View().Content)
	for _, want := range []string{"workflows", "WORKFLOWS", "none yet", "0 workflows"} {
		if !strings.Contains(content, want) {
			t.Errorf("empty workflows view missing %q:\n%s", want, content)
		}
	}

	// n → name prompt → ⏎ creates the workflow and lands on it.
	m = drive(t, m, keyPress('n'))
	if m.(app).promptKind != promptNewWorkflow {
		t.Fatalf("promptKind after n = %v, want promptNewWorkflow", m.(app).promptKind)
	}
	m = typeText(t, m, "ship it")
	m = drive(t, m, enter())
	wfs := waitForWorkflows(t, s, "workflow created", func(w []workflow.Workflow) bool { return len(w) == 1 })
	if wfs[0].Name != "ship it" {
		t.Fatalf("created workflow = %+v, want name 'ship it'", wfs[0])
	}
	// Drain the follow-up load so the view reflects the store.
	m = reloadWorkflows(t, m, wfs[0].ID)
	a := m.(app)
	if got := a.selectedWorkflowID(); got != wfs[0].ID {
		t.Fatalf("selected workflow = %d, want %d", got, wfs[0].ID)
	}
	content = ansi.Strip(m.View().Content)
	if !strings.Contains(content, "ship it") || !strings.Contains(content, "no steps yet") {
		t.Fatalf("view after create missing the workflow row or empty-steps hint:\n%s", content)
	}

	// l → steps pane; n → step prompt → ⏎ adds an agent step.
	m = drive(t, m, keyPress('l'))
	if m.(app).wfFocus != wfPaneSteps {
		t.Fatal("l did not move focus to the steps pane")
	}
	m = drive(t, m, keyPress('n'))
	if m.(app).promptKind != promptNewStep {
		t.Fatalf("promptKind after n = %v, want promptNewStep", m.(app).promptKind)
	}
	m = typeText(t, m, "implement")
	m = drive(t, m, enter())
	steps := waitForSteps(t, s, wfs[0].ID, "step created", func(st []workflow.Step) bool { return len(st) == 1 })
	if steps[0].Name != "implement" || steps[0].Kind != workflow.StepAgent {
		t.Fatalf("created step = %+v, want agent step 'implement'", steps[0])
	}
	m = reloadWorkflows(t, m, wfs[0].ID)
	content = ansi.Strip(m.View().Content)
	for _, want := range []string{"1  implement", "agent", "no prompt", "PROMPT"} {
		if !strings.Contains(content, want) {
			t.Errorf("steps pane missing %q:\n%s", want, content)
		}
	}

	// The $EDITOR round-trip: simulate the editor having written the temp
	// file, then feed the finished message the ExecProcess callback sends.
	path := filepath.Join(t.TempDir(), "prompt.md")
	prompt := "Implement {{.Task.Title}} in {{.Cwd}}.\n"
	if err := os.WriteFile(path, []byte(prompt), 0o600); err != nil {
		t.Fatal(err)
	}
	m = drive(t, m, stepEditorFinishedMsg{stepID: steps[0].ID, path: path})
	waitForSteps(t, s, wfs[0].ID, "prompt saved", func(st []workflow.Step) bool {
		return len(st) == 1 && st[0].PromptMD == prompt
	})
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("temp file %s not removed after save (err=%v)", path, err)
	}
	m = reloadWorkflows(t, m, wfs[0].ID)
	content = ansi.Strip(m.View().Content)
	if !strings.Contains(content, "Implement {{.Task.Title}}") {
		t.Errorf("prompt preview missing the saved template:\n%s", content)
	}

	// v validates the graph and every step prompt of the selected workflow.
	m = drive(t, m, keyPress('v'))
	if st := m.(app).status; st.isErr || !strings.Contains(st.text, "1 step, graph and prompts valid") {
		t.Errorf("validate flash = %+v, want '1 step, graph and prompts valid'", st)
	}
}

// waitForEdges polls until a workflow's edges satisfy check.
func waitForEdges(t *testing.T, s *store.Store, workflowID int64, what string, check func([]workflow.Edge) bool) []workflow.Edge {
	t.Helper()
	var got []workflow.Edge
	waitFor(t, what, func() bool {
		var err error
		got, err = s.ListEdges(context.Background(), workflowID)
		return err == nil && check(got)
	})
	return got
}

// addStepFromTUI runs the `n` → name → ⏎ flow in the steps pane and
// waits for the step (and any default edge) to land, then reloads.
func addStepFromTUI(t *testing.T, m tea.Model, s *store.Store, workflowID int64, name string, wantSteps int) tea.Model {
	t.Helper()
	m = drive(t, m, keyPress('n'))
	m = typeText(t, m, name)
	m = drive(t, m, enter())
	waitForSteps(t, s, workflowID, "step "+name, func(st []workflow.Step) bool { return len(st) == wantSteps })
	if wantSteps > 1 {
		waitForEdges(t, s, workflowID, "default edge into "+name, func(es []workflow.Edge) bool { return len(es) == wantSteps-1 })
	}
	return reloadWorkflows(t, m, workflowID)
}

// backspaces clears n characters from the open prompt.
func backspaces(t *testing.T, m tea.Model, n int) tea.Model {
	t.Helper()
	for range n {
		m = drive(t, m, tea.KeyPressMsg{Code: tea.KeyBackspace})
	}
	return m
}

// TestWorkflowsEdgesEditorAuthorsReviewLoop is the task's acceptance
// test: author implement / review / gate / ship with a bounded reject loop
// from the TUI alone, and read the preview back.
func TestWorkflowsEdgesEditorAuthorsReviewLoop(t *testing.T) {
	m, s := newTestApp(t)
	ctx := context.Background()
	m = openWorkflows(t, m)
	m = drive(t, m, keyPress('n'))
	m = typeText(t, m, "ship it")
	m = drive(t, m, enter())
	wfs := waitForWorkflows(t, s, "workflow created", func(w []workflow.Workflow) bool { return len(w) == 1 })
	wfID := wfs[0].ID
	m = reloadWorkflows(t, m, wfID)
	m = drive(t, m, keyPress('l'))

	// Four steps; each one after the first links the previous step to it
	// with done -> next, so the workflow is linear with no edge work.
	m = addStepFromTUI(t, m, s, wfID, "implement", 1)
	m = addStepFromTUI(t, m, s, wfID, "review", 2)
	if st := m.(app).status; !strings.Contains(st.text, "implement done -> review") {
		t.Errorf("flash after adding review = %+v, want it to name the default edge", st)
	}
	m = addStepFromTUI(t, m, s, wfID, "gate", 3)
	m = drive(t, m, keyPress('t')) // the cursor is on the new step: make it a gate
	waitForSteps(t, s, wfID, "gate kind", func(st []workflow.Step) bool { return st[2].Kind == workflow.StepGate })
	m = reloadWorkflows(t, m, wfID)
	m = addStepFromTUI(t, m, s, wfID, "ship", 4)
	steps, _ := s.ListSteps(ctx, wfID)
	implement, review, gate, ship := steps[0], steps[1], steps[2], steps[3]
	for _, e := range mustEdges(t, s, wfID) {
		if e.Outcome != "done" {
			t.Errorf("default edge %+v, want outcome done", e)
		}
	}
	// A linear workflow previews with no annotations but the end.
	content := ansi.Strip(m.(app).View().Content)
	for _, want := range []string{"1  implement", "2  review", "3  gate", "4  ship  [done -> end]", "EDGES · ship", "none — its one outcome, done, ends the run"} {
		if !strings.Contains(content, want) {
			t.Errorf("linear preview missing %q:\n%s", want, content)
		}
	}
	if strings.Contains(content, "[done -> 2]") {
		t.Errorf("the linear default edge should be implicit, not annotated:\n%s", content)
	}

	// Onto review, into its EDGES: its default edge reads done -> gate.
	m = drive(t, m, keyPress('g'))
	m = drive(t, m, keyPress('j'))
	m = drive(t, m, keyPress('l'))
	a := m.(app)
	if a.wfFocus != wfPaneEdges {
		t.Fatalf("l from the steps pane: focus = %v, want the edges pane", a.wfFocus)
	}
	if e, ok := a.selectedEdge(); !ok || e.FromStepID != review.ID || e.ToStepID != gate.ID {
		t.Fatalf("selected edge = (%+v, %v), want review's done -> gate", e, ok)
	}
	if !strings.Contains(ansi.Strip(a.View().Content), "on done -> gate") {
		t.Errorf("edges pane missing the default edge row:\n%s", ansi.Strip(a.View().Content))
	}

	// e edits it: rename the outcome to approve (the old row goes), keep
	// the target the picker starts on (gate), leave max blank.
	m = drive(t, m, keyPress('e'))
	a = m.(app)
	if a.promptKind != promptEdgeOutcome || a.prompt.Value() != "done" {
		t.Fatalf("edit prompt = (%v, %q), want (promptEdgeOutcome, done)", a.promptKind, a.prompt.Value())
	}
	m = backspaces(t, m, 4)
	m = typeText(t, m, "Approve")
	m = drive(t, m, enter())
	a = m.(app)
	if !a.wfPickerOpen || a.wfPickerKind != wfPickEdgeTarget {
		t.Fatal("outcome ⏎ did not open the target step picker")
	}
	content = ansi.Strip(a.View().Content)
	for _, want := range []string{"review on approve ->", "which step?", "1 implement", "3 gate"} {
		if !strings.Contains(content, want) {
			t.Errorf("target picker missing %q:\n%s", want, content)
		}
	}
	if got := a.wfPickerOptions()[a.wfPickerSel]; got.label != "gate" {
		t.Errorf("picker starts on %q, want the edge's current target gate", got.label)
	}
	m = drive(t, m, enter())
	a = m.(app)
	if a.promptKind != promptEdgeMax || a.prompt.Value() != "" {
		t.Fatalf("after the picker: prompt = (%v, %q), want an empty max-iterations prompt", a.promptKind, a.prompt.Value())
	}
	m = drive(t, m, enter())
	waitForEdges(t, s, wfID, "done renamed to approve", func(es []workflow.Edge) bool {
		for _, e := range es {
			if e.FromStepID == review.ID {
				return e.Outcome == "approve" && e.ToStepID == gate.ID && e.MaxIterations == nil && len(es) == 3
			}
		}
		return false
	})
	m = reloadWorkflows(t, m, wfID)

	// n adds reject -> implement (max 3), picking the step by digit.
	m = drive(t, m, keyPress('n'))
	m = typeText(t, m, "reject")
	m = drive(t, m, enter())
	if !m.(app).wfPickerOpen {
		t.Fatal("n → outcome ⏎ did not open the target picker")
	}
	m = drive(t, m, keyPress('1'))
	if m.(app).promptKind != promptEdgeMax {
		t.Fatal("digit pick did not move on to the max-iterations prompt")
	}
	m = typeText(t, m, "3")
	m = drive(t, m, enter())
	edges := waitForEdges(t, s, wfID, "reject edge", func(es []workflow.Edge) bool { return len(es) == 4 })
	var reject workflow.Edge
	for _, e := range edges {
		if e.Outcome == "reject" {
			reject = e
		}
	}
	if reject.FromStepID != review.ID || reject.ToStepID != implement.ID || reject.MaxIterations == nil || *reject.MaxIterations != 3 {
		t.Errorf("reject edge = %+v, want review -> implement max 3", reject)
	}
	m = reloadWorkflows(t, m, wfID)
	a = m.(app)
	if e, ok := a.selectedEdge(); !ok || e.ID != reject.ID {
		t.Errorf("edge cursor after add on %+v, want the new reject edge", e)
	}

	// The preview reads correctly, in the pane and as text.
	want := strings.Join([]string{
		"1. implement",
		"2. review  [approve -> 3]  [reject -> 1 (max 3)]",
		"3. gate",
		"4. ship  [done -> end]",
	}, "\n")
	if got := workflow.PreviewText(steps, edges); got != want {
		t.Errorf("PreviewText =\n%s\nwant\n%s", got, want)
	}
	content = ansi.Strip(a.View().Content)
	for _, w := range []string{
		"2  review  [approve -> 3]  [reject -> 1 (max 3)]",
		"4  ship  [done -> end]",
		"on approve -> gate", "on reject -> implement (max 3)",
	} {
		if !strings.Contains(content, w) {
			t.Errorf("view missing %q:\n%s", w, content)
		}
	}
	_ = ship

	// v: a sound graph.
	m = drive(t, m, keyPress('v'))
	if st := m.(app).status; st.isErr || !strings.Contains(st.text, "4 steps, graph and prompts valid") {
		t.Errorf("validate flash = %+v, want the all-clear", st)
	}

	// esc mid-flow abandons the draft without writing.
	m = drive(t, m, keyPress('n'))
	m = typeText(t, m, "retry")
	m = drive(t, m, enter())
	m = drive(t, m, esc())
	a = m.(app)
	if a.wfPickerOpen || a.wfEdgeDraft != nil || a.promptKind != promptNone {
		t.Errorf("esc on the picker left state behind: picker=%v draft=%+v prompt=%v", a.wfPickerOpen, a.wfEdgeDraft, a.promptKind)
	}
	if es, _ := s.ListEdges(ctx, wfID); len(es) != 4 {
		t.Errorf("edges after an abandoned draft = %d, want 4", len(es))
	}

	// dd deletes the selected edge.
	m = drive(t, m, keyPress('d'))
	if !strings.Contains(ansi.Strip(m.(app).View().Content), "delete edge") {
		t.Error("delete panel does not say it deletes an edge")
	}
	drive(t, m, keyPress('d'))
	waitForEdges(t, s, wfID, "edge deleted", func(es []workflow.Edge) bool {
		for _, e := range es {
			if e.ID == reject.ID {
				return false
			}
		}
		return len(es) == 3
	})
}

func mustEdges(t *testing.T, s *store.Store, workflowID int64) []workflow.Edge {
	t.Helper()
	es, err := s.ListEdges(context.Background(), workflowID)
	if err != nil {
		t.Fatal(err)
	}
	return es
}

// TestWorkflowsValidateShowsProblemsUntilFixed: `v` lists the graph's
// problems under the steps; they are recomputed on reload, so fixing one
// removes it without another `v`.
func TestWorkflowsValidateShowsProblemsUntilFixed(t *testing.T) {
	m, s := newTestApp(t)
	ctx := context.Background()
	w, err := s.CreateWorkflow(ctx, "loose", "")
	if err != nil {
		t.Fatal(err)
	}
	// Straight to the store, so no default edge is written.
	implement, _ := s.AddStep(ctx, w.ID, "implement", workflow.StepAgent)
	review, _ := s.AddStep(ctx, w.ID, "review", workflow.StepAgent)
	if err := s.SetStepPrompt(ctx, review.ID, "Review it and finish with approve or reject."); err != nil {
		t.Fatal(err)
	}
	m = openWorkflows(t, m)

	m = drive(t, m, keyPress('v'))
	a := m.(app)
	if !a.status.isErr || !strings.Contains(a.status.text, "implement: no edge leaves it") || !strings.Contains(a.status.text, "(+3 more)") {
		t.Errorf("validate flash = %+v, want the first problem and a count of 3 more", a.status)
	}
	content := ansi.Strip(a.View().Content)
	for _, want := range []string{"PROBLEMS · 4", "the run would end here, before review", "review: unreachable",
		`prompt mentions "approve" but no edge routes it`, `prompt mentions "reject"`} {
		if !strings.Contains(content, want) {
			t.Errorf("problems section missing %q:\n%s", want, content)
		}
	}

	// Fix the graph behind the view's back and reload: the problems shrink.
	if _, err := s.SetEdge(ctx, implement.ID, "done", review.ID, nil); err != nil {
		t.Fatal(err)
	}
	two := int64(2)
	if _, err := s.SetEdge(ctx, review.ID, "reject", implement.ID, &two); err != nil {
		t.Fatal(err)
	}
	m = reloadWorkflows(t, m, w.ID)
	content = ansi.Strip(m.(app).View().Content)
	if !strings.Contains(content, "PROBLEMS · 1") || !strings.Contains(content, `prompt mentions "approve"`) {
		t.Errorf("after fixing two problems, want only the approve one left:\n%s", content)
	}
	if strings.Contains(content, "unreachable") {
		t.Errorf("fixed problem still shown:\n%s", content)
	}
	if _, err := s.SetEdge(ctx, review.ID, "approve", review.ID, &two); err != nil {
		t.Fatal(err)
	}
	m = reloadWorkflows(t, m, w.ID)
	if content = ansi.Strip(m.(app).View().Content); strings.Contains(content, "PROBLEMS") {
		t.Errorf("problems section still shown once everything is fixed:\n%s", content)
	}

	// Another workflow does not inherit the problems display.
	other, _ := s.CreateWorkflow(ctx, "other", "")
	m = reloadWorkflows(t, m, other.ID)
	if a := m.(app); a.wfProblemsFor != 0 || len(a.wfProblems) != 0 {
		t.Errorf("problems carried to another workflow: for=%d %v", a.wfProblemsFor, a.wfProblems)
	}
}

func TestWorkflowsStepAttributesAndOrder(t *testing.T) {
	m, s := newTestApp(t)
	ctx := context.Background()
	w, err := s.CreateWorkflow(ctx, "review loop", "")
	if err != nil {
		t.Fatal(err)
	}
	first, _ := s.AddStep(ctx, w.ID, "draft", workflow.StepAgent)
	second, _ := s.AddStep(ctx, w.ID, "review", workflow.StepAgent)

	m = openWorkflows(t, m)
	m = drive(t, m, keyPress('l'))

	// m → model picker, pick sonnet.
	m = drive(t, m, keyPress('m'))
	if !m.(app).wfPickerOpen || m.(app).wfPickerKind != wfPickModel {
		t.Fatal("m did not open the model picker")
	}
	content := ansi.Strip(m.View().Content)
	for _, want := range []string{"model for draft", "opus", "sonnet", "haiku", "inherit"} {
		if !strings.Contains(content, want) {
			t.Errorf("model picker missing %q:\n%s", want, content)
		}
	}
	m = drive(t, m, keyPress('2'))
	if m.(app).wfPickerOpen {
		t.Error("picker still open after a digit pick")
	}
	waitForSteps(t, s, w.ID, "model set", func(st []workflow.Step) bool {
		return st[0].ID == first.ID && st[0].Model == "sonnet"
	})
	// The store having the write is not the same as the model having it:
	// bring the model up to date before the next key acts on its copy.
	m = reloadWorkflows(t, m, w.ID)

	// p → permission picker, arrow down to acceptEdits, ⏎.
	m = drive(t, m, keyPress('p'))
	if !m.(app).wfPickerOpen || m.(app).wfPickerKind != wfPickPermission {
		t.Fatal("p did not open the permission-mode picker")
	}
	m = drive(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	m = drive(t, m, enter())
	waitForSteps(t, s, w.ID, "permission mode set", func(st []workflow.Step) bool {
		return st[0].PermissionMode == "acceptEdits"
	})
	m = reloadWorkflows(t, m, w.ID)

	// t flips agent → gate.
	m = drive(t, m, keyPress('t'))
	waitForSteps(t, s, w.ID, "kind toggled", func(st []workflow.Step) bool {
		return st[0].Kind == workflow.StepGate
	})
	m = reloadWorkflows(t, m, w.ID)
	content = ansi.Strip(m.View().Content)
	if !strings.Contains(content, "gate · sonnet · acceptEdits") {
		t.Errorf("step row missing its attributes:\n%s", content)
	}

	// J moves the first step below the second; the cursor follows it.
	m = drive(t, m, keyPress('J'))
	steps := waitForSteps(t, s, w.ID, "reordered", func(st []workflow.Step) bool {
		return len(st) == 2 && st[0].ID == second.ID && st[1].ID == first.ID
	})
	m = reloadWorkflows(t, m, w.ID)
	if got, ok := m.(app).selectedStep(); !ok || got.ID != first.ID {
		t.Errorf("cursor after J on step %d, want it to follow %d", got.ID, first.ID)
	}
	if steps[0].Name != "review" {
		t.Errorf("first step after J = %q, want review", steps[0].Name)
	}

	// K moves it back up.
	m = drive(t, m, keyPress('K'))
	waitForSteps(t, s, w.ID, "reordered back", func(st []workflow.Step) bool {
		return len(st) == 2 && st[0].ID == first.ID
	})

	// dd deletes the selected step after the panel confirms.
	m = reloadWorkflows(t, m, w.ID)
	m = drive(t, m, keyPress('d'))
	if !m.(app).deletePending {
		t.Fatal("first d did not arm the delete chord")
	}
	if !strings.Contains(ansi.Strip(m.View().Content), "delete step") {
		t.Error("delete panel does not say it deletes a step")
	}
	drive(t, m, keyPress('d'))
	waitForSteps(t, s, w.ID, "step deleted", func(st []workflow.Step) bool {
		return len(st) == 1 && st[0].ID == second.ID
	})
}

// TestWorkflowsStepEditsDoNotClobberEachOther pins the lost update behind
// the flaky CI run of TestWorkflowsStepAttributesAndOrder: an attribute
// written to the store after the model last loaded its steps must survive
// the next edit made from that (now stale) model. Each key writes only the
// attribute it changed, so the stale copy is never written back.
func TestWorkflowsStepEditsDoNotClobberEachOther(t *testing.T) {
	m, s := newTestApp(t)
	ctx := context.Background()
	w, err := s.CreateWorkflow(ctx, "review loop", "")
	if err != nil {
		t.Fatal(err)
	}
	st, err := s.AddStep(ctx, w.ID, "draft", workflow.StepAgent)
	if err != nil {
		t.Fatal(err)
	}
	m = openWorkflows(t, m)
	m = drive(t, m, keyPress('l'))
	if got, ok := m.(app).selectedStep(); !ok || got.PermissionMode != "" {
		t.Fatalf("selected step = (%+v, %v), want the fresh draft step", got, ok)
	}

	// Land a write behind the model's back, as a picker's mutate does when
	// its reload is still in flight while the next key arrives.
	if err := s.SetStepPermissionMode(ctx, st.ID, "acceptEdits"); err != nil {
		t.Fatal(err)
	}
	// t toggles the kind from the stale model.
	m = drive(t, m, keyPress('t'))
	steps := waitForSteps(t, s, w.ID, "kind toggled", func(st []workflow.Step) bool {
		return st[0].Kind == workflow.StepGate
	})
	if steps[0].PermissionMode != "acceptEdits" {
		t.Errorf("kind toggle clobbered permission mode: got %q, want acceptEdits", steps[0].PermissionMode)
	}

	// Same through the picker: the kind flips behind the model's back, then
	// a model pick from the stale copy must leave it alone.
	if err := s.SetStepKind(ctx, st.ID, workflow.StepAgent); err != nil {
		t.Fatal(err)
	}
	m = drive(t, m, keyPress('m'))
	drive(t, m, keyPress('2'))
	steps = waitForSteps(t, s, w.ID, "model set", func(st []workflow.Step) bool {
		return st[0].Model == "sonnet"
	})
	if steps[0].Kind != workflow.StepAgent || steps[0].PermissionMode != "acceptEdits" {
		t.Errorf("model pick clobbered other attributes: %+v", steps[0])
	}
}

func TestWorkflowsRenameDuplicateDelete(t *testing.T) {
	m, s := newTestApp(t)
	ctx := context.Background()
	w, err := s.CreateWorkflow(ctx, "alpha", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddStep(ctx, w.ID, "one", workflow.StepAgent); err != nil {
		t.Fatal(err)
	}
	m = openWorkflows(t, m)

	// R renames, seeded with the current name.
	m = drive(t, m, keyPress('R'))
	a := m.(app)
	if a.promptKind != promptRenameWorkflow || a.prompt.Value() != "alpha" {
		t.Fatalf("rename prompt = (%v, %q), want (promptRenameWorkflow, alpha)", a.promptKind, a.prompt.Value())
	}
	m = typeText(t, m, " two")
	m = drive(t, m, enter())
	waitForWorkflows(t, s, "renamed", func(ws []workflow.Workflow) bool {
		return len(ws) == 1 && ws[0].Name == "alpha two"
	})

	// D duplicates, steps included, and lands on the copy.
	m = reloadWorkflows(t, m, w.ID)
	m = drive(t, m, keyPress('D'))
	if m.(app).promptKind != promptDuplicateWorkflow {
		t.Fatal("D did not open the duplicate prompt")
	}
	m = drive(t, m, enter()) // accept the seeded "<name> copy"
	wfs := waitForWorkflows(t, s, "duplicated", func(ws []workflow.Workflow) bool { return len(ws) == 2 })
	var copyID int64
	for _, x := range wfs {
		if x.Name == "alpha two copy" {
			copyID = x.ID
			if x.StepCount != 1 {
				t.Errorf("duplicate step count = %d, want 1", x.StepCount)
			}
		}
	}
	if copyID == 0 {
		t.Fatalf("duplicate not found in %+v", wfs)
	}
	m = reloadWorkflows(t, m, copyID)
	if got := m.(app).selectedWorkflowID(); got != copyID {
		t.Errorf("selection after duplicate = %d, want the copy %d", got, copyID)
	}

	// dd deletes the selected (copied) workflow.
	m = drive(t, m, keyPress('d'))
	if !strings.Contains(ansi.Strip(m.View().Content), "delete workflow") {
		t.Error("delete panel does not say it deletes a workflow")
	}
	drive(t, m, keyPress('d'))
	waitForWorkflows(t, s, "deleted", func(ws []workflow.Workflow) bool {
		return len(ws) == 1 && ws[0].ID == w.ID
	})
}

func TestWorkflowsDeleteRefusedWhileRunIsLive(t *testing.T) {
	m, s := newTestApp(t)
	ctx := context.Background()
	w, err := s.CreateWorkflow(ctx, "busy", "")
	if err != nil {
		t.Fatal(err)
	}
	tk, err := s.AddTaskIn(ctx, 1, "task")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateRun(ctx, w.ID, tk.ID, "/tmp"); err != nil {
		t.Fatal(err)
	}
	m = openWorkflows(t, m)
	m = drive(t, m, keyPress('d'))
	m = drive(t, m, keyPress('d'))

	// The refusal arrives as an error flash; the workflow is still there.
	if st := m.(app).status; !st.isErr || !strings.Contains(st.text, "cannot delete busy") {
		t.Errorf("status after refused delete = %+v, want the in-use flash", st)
	}
	if wfs, _ := s.ListWorkflows(ctx); len(wfs) != 1 {
		t.Errorf("workflows after refused delete = %d, want 1", len(wfs))
	}
}

func TestWorkflowsNavigationAndExit(t *testing.T) {
	m, s := newTestApp(t)
	ctx := context.Background()
	for _, name := range []string{"a", "b"} {
		if _, err := s.CreateWorkflow(ctx, name, ""); err != nil {
			t.Fatal(err)
		}
	}
	m = openWorkflows(t, m)
	if got, _ := m.(app).selectedWorkflow(); got.Name != "a" {
		t.Fatalf("initial selection = %q, want a", got.Name)
	}
	m = drive(t, m, keyPress('j'))
	if got, _ := m.(app).selectedWorkflow(); got.Name != "b" {
		t.Errorf("selection after j = %q, want b", got.Name)
	}

	// The header names the view and counts; the footer carries the hints.
	content := ansi.Strip(m.View().Content)
	for _, want := range []string{"tend  ·  workflows", "2 workflows", "D duplicate"} {
		if !strings.Contains(content, want) {
			t.Errorf("view missing %q:\n%s", want, content)
		}
	}

	// esc from the steps pane backs out to the workflows pane; a second
	// esc leaves the view.
	m = drive(t, m, enter())
	if m.(app).wfFocus != wfPaneSteps {
		t.Fatal("⏎ did not focus the steps pane")
	}
	m = drive(t, m, esc())
	if a := m.(app); a.mode != modeWorkflows || a.wfFocus != wfPaneList {
		t.Fatalf("after esc: mode=%v focus=%v, want workflows view, list pane", a.mode, a.wfFocus)
	}
	m = drive(t, m, esc())
	if m.(app).mode != modeList {
		t.Errorf("mode after second esc = %v, want modeList", m.(app).mode)
	}

	// The palette reaches the view too.
	m = drive(t, m, keyPress(':'))
	m = typeText(t, m, "workflows")
	m = drive(t, m, enter())
	if m.(app).mode != modeWorkflows {
		t.Errorf("palette `workflows` did not open the view")
	}
	// q leaves it.
	m = drive(t, m, keyPress('q'))
	if m.(app).mode != modeList {
		t.Errorf("mode after q = %v, want modeList", m.(app).mode)
	}
}
