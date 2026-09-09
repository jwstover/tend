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
	m = drive(t, m, collect(m.(app).loadWorkflows(wfs[0].ID))[0])
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
	m = drive(t, m, collect(m.(app).loadWorkflows(wfs[0].ID))[0])
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
	m = drive(t, m, collect(m.(app).loadWorkflows(wfs[0].ID))[0])
	content = ansi.Strip(m.View().Content)
	if !strings.Contains(content, "Implement {{.Task.Title}}") {
		t.Errorf("prompt preview missing the saved template:\n%s", content)
	}

	// v validates every step prompt of the selected workflow.
	m = drive(t, m, keyPress('v'))
	if st := m.(app).status; st.isErr || !strings.Contains(st.text, "1 prompt valid") {
		t.Errorf("validate flash = %+v, want '1 prompt valid'", st)
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

	// t flips agent → gate.
	m = drive(t, m, keyPress('t'))
	waitForSteps(t, s, w.ID, "kind toggled", func(st []workflow.Step) bool {
		return st[0].Kind == workflow.StepGate
	})
	m = drive(t, m, collect(m.(app).loadWorkflows(w.ID))[0])
	content = ansi.Strip(m.View().Content)
	if !strings.Contains(content, "gate · sonnet · acceptEdits") {
		t.Errorf("step row missing its attributes:\n%s", content)
	}

	// J moves the first step below the second; the cursor follows it.
	m = drive(t, m, keyPress('J'))
	steps := waitForSteps(t, s, w.ID, "reordered", func(st []workflow.Step) bool {
		return len(st) == 2 && st[0].ID == second.ID && st[1].ID == first.ID
	})
	m = drive(t, m, collect(m.(app).loadWorkflows(w.ID))[0])
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
	m = drive(t, m, collect(m.(app).loadWorkflows(w.ID))[0])
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
	m = drive(t, m, collect(m.(app).loadWorkflows(w.ID))[0])
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
	m = drive(t, m, collect(m.(app).loadWorkflows(copyID))[0])
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
