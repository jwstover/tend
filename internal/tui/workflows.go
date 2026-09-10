package tui

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jwstover/tend/internal/task"
	"github.com/jwstover/tend/internal/workflow"
)

// The workflows view is where workflow definitions are authored: the
// workflows on the left; on the right the selected one's steps in
// sort_order, annotated with the graph their edges imply, then the
// selected step's EDGES sub-list and its prompt. Three panes, walked with
// h/l: workflows → steps → edges.
//
// Every store call is a tea.Cmd, and the panes are tracked with wfFocus
// rather than the list view's pane type because none of these columns is
// the task list or the detail viewport.

// wfPane is which workflows-view column owns the keyboard.
type wfPane int

const (
	wfPaneList  wfPane = iota // the workflows
	wfPaneSteps               // the selected workflow's steps
	wfPaneEdges               // the selected step's edges
)

// wfPickerKind is what the picker overlay is choosing.
type wfPickerKind int

const (
	wfPickModel wfPickerKind = iota
	wfPickPermission
	wfPickEdgeTarget // the step an edge being drafted leads to
)

// wfPickerOption is one row of the picker: what it shows and what it
// stores. value "" means "inherit the default", shown as such.
type wfPickerOption struct{ label, value string }

// stepModelOptions are the models a step can pin. The names are what
// claude's --model accepts as aliases; "" leaves the choice to claude.
var stepModelOptions = []wfPickerOption{
	{"opus", "opus"},
	{"sonnet", "sonnet"},
	{"haiku", "haiku"},
	{"inherit", ""},
}

// stepPermissionOptions are claude's --permission-mode values, stored
// as-is because tend only ever forwards them.
var stepPermissionOptions = []wfPickerOption{
	{"default", "default"},
	{"acceptEdits", "acceptEdits"},
	{"bypassPermissions", "bypassPermissions"},
	{"plan", "plan"},
}

// edgeDraft is an edge on its way through the three-stage add/edit flow:
// outcome prompt, then the target step picker, then the max-iterations
// prompt. editID is the edge being edited (0 when adding) and oldOutcome
// its outcome before the edit: SetEdge upserts on (from, outcome), so a
// renamed outcome means deleting the old row rather than re-pointing it.
type edgeDraft struct {
	fromStepID int64
	editID     int64
	oldOutcome string
	outcome    string
	toStepID   int64
	max        *int64
}

// startWorkflows switches into the view. Loaded data is left in place so
// re-entering does not flash empty; loadWorkflows refreshes it.
func (a *app) startWorkflows() {
	a.mode = modeWorkflows
	a.wfFocus = wfPaneList
	a.deletePending = false
	a.resize()
}

// leaveWorkflows returns to the list view.
func (a *app) leaveWorkflows() tea.Cmd {
	a.mode = modeList
	a.deletePending = false
	a.resize()
	return a.loadTasks(modeList)
}

// selectedWorkflow returns the workflow under the cursor.
func (a app) selectedWorkflow() (workflow.Workflow, bool) {
	if a.wfCursor < 0 || a.wfCursor >= len(a.workflows) {
		return workflow.Workflow{}, false
	}
	return a.workflows[a.wfCursor], true
}

// selectedWorkflowID is the id under the cursor, 0 when there is none —
// the argument loadWorkflows wants.
func (a app) selectedWorkflowID() int64 {
	w, ok := a.selectedWorkflow()
	if !ok {
		return 0
	}
	return w.ID
}

// selectedStep returns the step under the steps cursor, provided the
// loaded steps belong to the selected workflow.
func (a app) selectedStep() (workflow.Step, bool) {
	if a.wfStepsFor != a.selectedWorkflowID() || a.wfStepCursor < 0 || a.wfStepCursor >= len(a.wfSteps) {
		return workflow.Step{}, false
	}
	return a.wfSteps[a.wfStepCursor], true
}

// selectedStepEdges returns the edges leaving the selected step, in the
// order ListEdges gives them (by outcome): the EDGES sub-list.
func (a app) selectedStepEdges() []workflow.Edge {
	st, ok := a.selectedStep()
	if !ok {
		return nil
	}
	var out []workflow.Edge
	for _, e := range a.wfEdges {
		if e.FromStepID == st.ID {
			out = append(out, e)
		}
	}
	return out
}

// selectedEdge returns the edge under the edges cursor.
func (a app) selectedEdge() (workflow.Edge, bool) {
	edges := a.selectedStepEdges()
	if a.wfEdgeCursor < 0 || a.wfEdgeCursor >= len(edges) {
		return workflow.Edge{}, false
	}
	return edges[a.wfEdgeCursor], true
}

// stepName is a step's name by id, for labels; "?" for an id not among the
// loaded steps.
func (a app) stepName(id int64) string {
	for _, st := range a.wfSteps {
		if st.ID == id {
			return st.Name
		}
	}
	return "?"
}

// setSteps installs a freshly loaded step list and edges and settles the
// cursors: the step cursor on wfSelectStepID if one is pending and
// present, otherwise clamped; the edge cursor on wfSelectOutcome likewise.
// Validation problems are recomputed when they are showing for this
// workflow, so fixing one makes it disappear without another `v`.
func (a *app) setSteps(workflowID int64, steps []workflow.Step, edges []workflow.Edge) {
	a.wfSteps, a.wfEdges, a.wfStepsFor = steps, edges, workflowID
	if want := a.wfSelectStepID; want != 0 {
		a.wfSelectStepID = 0
		for i, st := range steps {
			if st.ID == want {
				a.wfStepCursor = i
			}
		}
	}
	a.wfStepCursor = max(min(a.wfStepCursor, len(steps)-1), 0)

	stepEdges := a.selectedStepEdges()
	if want := a.wfSelectOutcome; want != "" {
		a.wfSelectOutcome = ""
		for i, e := range stepEdges {
			if e.Outcome == want {
				a.wfEdgeCursor = i
			}
		}
	}
	a.wfEdgeCursor = max(min(a.wfEdgeCursor, len(stepEdges)-1), 0)

	if workflowID != 0 && a.wfProblemsFor == workflowID {
		a.wfProblems = workflow.Validate(steps, edges)
	} else {
		a.wfProblems, a.wfProblemsFor = nil, 0
	}
}

// setWorkflowCursor moves between workflows and fetches the new
// selection's steps.
func (a *app) setWorkflowCursor(row int) tea.Cmd {
	if row < 0 || row >= len(a.workflows) || row == a.wfCursor {
		return nil
	}
	a.wfCursor = row
	a.wfStepCursor, a.wfEdgeCursor = 0, 0
	return a.loadSteps(a.workflows[row].ID)
}

// --- keys ---

// handleWorkflowsKey owns the keyboard in the workflows view. The `dd`
// chord is handled here rather than in handleKey's shared branch because
// what it deletes depends on which of the three panes is focused.
func (a app) handleWorkflowsKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if a.deletePending {
		a.deletePending = false
		a.resize()
		if key.Matches(msg, a.keys.Delete) {
			switch a.wfFocus {
			case wfPaneEdges:
				if e, ok := a.selectedEdge(); ok {
					return a, a.deleteEdge(e)
				}
			case wfPaneSteps:
				if st, ok := a.selectedStep(); ok {
					return a, a.deleteStep(st)
				}
			default:
				if w, ok := a.selectedWorkflow(); ok {
					return a, a.deleteWorkflow(w)
				}
			}
		}
		return a, nil
	}

	switch {
	// ctrl+c always quits; `q` closes the view like esc.
	case key.Matches(msg, a.keys.Quit) && msg.String() != "q":
		return a, tea.Quit
	case key.Matches(msg, a.keys.Quit), key.Matches(msg, a.keys.Workflows):
		return a, a.leaveWorkflows()

	case key.Matches(msg, a.keys.Back):
		// esc backs out one pane at a time, like the detail pane.
		switch a.wfFocus {
		case wfPaneEdges:
			a.wfFocus = wfPaneSteps
			return a, nil
		case wfPaneSteps:
			a.wfFocus = wfPaneList
			return a, nil
		}
		return a, a.leaveWorkflows()

	case key.Matches(msg, a.keys.Help):
		a.helpOpen = true
		return a, nil
	case key.Matches(msg, a.keys.Palette):
		a.openPalette()
		return a, nil
	case key.Matches(msg, a.keys.Note):
		return a, a.modal.Open(modalLog, true, "note", 0, "")
	case key.Matches(msg, a.keys.Validate):
		return a, a.validateSelectedWorkflow()
	}

	switch a.wfFocus {
	case wfPaneEdges:
		return a.handleEdgesKey(msg)
	case wfPaneSteps:
		return a.handleStepsKey(msg)
	}
	return a.handleWorkflowListKey(msg)
}

// handleWorkflowListKey is the left pane: navigate, and create / rename /
// duplicate / delete workflows.
func (a app) handleWorkflowListKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, a.keys.ExpandOpen), key.Matches(msg, a.keys.ExpandToggle):
		if _, ok := a.selectedWorkflow(); ok {
			a.wfFocus = wfPaneSteps
		}
		return a, nil
	case key.Matches(msg, a.keys.ScrollDown):
		return a, a.setWorkflowCursor(a.wfCursor + 1)
	case key.Matches(msg, a.keys.ScrollUp):
		return a, a.setWorkflowCursor(a.wfCursor - 1)

	case key.Matches(msg, a.keys.QuickAdd):
		return a, a.openPrompt(promptNewWorkflow, "new workflow: ", 0)
	case key.Matches(msg, a.keys.Rename):
		if w, ok := a.selectedWorkflow(); ok {
			return a, a.openPromptWith(promptRenameWorkflow, fmt.Sprintf("rename %s: ", w.Name), w.Name, w.ID)
		}
		return a, nil
	case key.Matches(msg, a.keys.Duplicate):
		if w, ok := a.selectedWorkflow(); ok {
			return a, a.openPromptWith(promptDuplicateWorkflow,
				fmt.Sprintf("duplicate %s as: ", w.Name), w.Name+" copy", w.ID)
		}
		return a, nil
	case key.Matches(msg, a.keys.Delete):
		if _, ok := a.selectedWorkflow(); ok {
			a.deletePending = true
			a.resize()
		}
		return a, nil
	}

	switch msg.String() {
	case "G":
		return a, a.setWorkflowCursor(len(a.workflows) - 1)
	case "g":
		return a, a.setWorkflowCursor(0)
	}
	return a, nil
}

// handleStepsKey is the middle pane: navigate, and edit the selected step
// — name, prompt, model, permission mode, kind, order — or step into its
// edges.
func (a app) handleStepsKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, a.keys.ExpandClose):
		a.wfFocus = wfPaneList
		return a, nil
	case key.Matches(msg, a.keys.ExpandOpen), key.Matches(msg, a.keys.ExpandToggle):
		if _, ok := a.selectedStep(); ok {
			a.wfFocus = wfPaneEdges
			a.wfEdgeCursor = 0
		}
		return a, nil
	case key.Matches(msg, a.keys.ScrollDown):
		if a.wfStepCursor < len(a.wfSteps)-1 {
			a.wfStepCursor++
			a.wfEdgeCursor = 0
		}
		return a, nil
	case key.Matches(msg, a.keys.ScrollUp):
		if a.wfStepCursor > 0 {
			a.wfStepCursor--
			a.wfEdgeCursor = 0
		}
		return a, nil

	case key.Matches(msg, a.keys.QuickAdd):
		if w, ok := a.selectedWorkflow(); ok {
			return a, a.openPrompt(promptNewStep, fmt.Sprintf("new step in %s: ", w.Name), w.ID)
		}
		return a, nil
	}

	st, ok := a.selectedStep()
	if !ok {
		return a, nil
	}

	switch {
	case key.Matches(msg, a.keys.EditBody):
		return a, editStepPromptCmd(st)
	case key.Matches(msg, a.keys.StepModel):
		a.openWfPicker(wfPickModel, st)
		return a, nil
	case key.Matches(msg, a.keys.StepPermission):
		a.openWfPicker(wfPickPermission, st)
		return a, nil
	case key.Matches(msg, a.keys.StepKind):
		next := workflow.StepGate
		if st.Kind == workflow.StepGate {
			next = workflow.StepAgent
		}
		return a, a.setStepAttr(st.ID, flash{kind: flashEdit, text: fmt.Sprintf("%s → %s", st.Name, next)}, func() error {
			return a.store.SetStepKind(a.ctx, st.ID, next)
		})
	case key.Matches(msg, a.keys.StepDown):
		return a, a.moveStep(a.wfStepCursor, a.wfStepCursor+1)
	case key.Matches(msg, a.keys.StepUp):
		return a, a.moveStep(a.wfStepCursor, a.wfStepCursor-1)
	case key.Matches(msg, a.keys.Delete):
		a.deletePending = true
		a.resize()
		return a, nil
	}

	switch msg.String() {
	case "G":
		a.wfStepCursor, a.wfEdgeCursor = len(a.wfSteps)-1, 0
	case "g":
		a.wfStepCursor, a.wfEdgeCursor = 0, 0
	}
	return a, nil
}

// handleEdgesKey is the right-most pane: the selected step's edges.
// Navigate, add, edit, delete.
func (a app) handleEdgesKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	edges := a.selectedStepEdges()
	switch {
	case key.Matches(msg, a.keys.ExpandClose):
		a.wfFocus = wfPaneSteps
		return a, nil
	case key.Matches(msg, a.keys.ScrollDown):
		if a.wfEdgeCursor < len(edges)-1 {
			a.wfEdgeCursor++
		}
		return a, nil
	case key.Matches(msg, a.keys.ScrollUp):
		a.wfEdgeCursor = max(a.wfEdgeCursor-1, 0)
		return a, nil
	case key.Matches(msg, a.keys.QuickAdd):
		if st, ok := a.selectedStep(); ok {
			return a, a.startEdgeDraft(st, nil)
		}
		return a, nil
	case key.Matches(msg, a.keys.EditBody):
		if st, ok := a.selectedStep(); ok {
			if e, ok := a.selectedEdge(); ok {
				return a, a.startEdgeDraft(st, &e)
			}
		}
		return a, nil
	case key.Matches(msg, a.keys.Delete):
		if _, ok := a.selectedEdge(); ok {
			a.deletePending = true
			a.resize()
		}
		return a, nil
	}

	switch msg.String() {
	case "G":
		a.wfEdgeCursor = max(len(edges)-1, 0)
	case "g":
		a.wfEdgeCursor = 0
	}
	return a, nil
}

// submitWorkflowPrompt performs the workflows-view prompt whose text was
// just entered. target is the workflow (rename, duplicate, new step) the
// prompt was opened on; value is already trimmed and non-empty.
func (a app) submitWorkflowPrompt(kind promptKind, target int64, value string) tea.Cmd {
	switch kind {
	case promptNewWorkflow:
		return func() tea.Msg {
			w, err := a.store.CreateWorkflow(a.ctx, value, "")
			if err != nil {
				return errMsg{err}
			}
			return workflowCreatedMsg{w: w, status: flash{kind: flashAdd, text: "workflow: " + w.Name}}
		}
	case promptRenameWorkflow:
		return a.mutate(flash{kind: flashEdit, text: "renamed to " + value}, func() error {
			return a.store.RenameWorkflow(a.ctx, target, value)
		})
	case promptDuplicateWorkflow:
		return func() tea.Msg {
			w, err := a.store.DuplicateWorkflow(a.ctx, target, value)
			if err != nil {
				return errMsg{err}
			}
			return workflowCreatedMsg{w: w, status: flash{kind: flashAdd, text: "duplicated as " + w.Name}}
		}
	case promptNewStep:
		return a.addStep(target, value)
	}
	return nil
}

// --- commands ---

// loadWorkflows fetches every workflow and the steps and edges of the one
// the view should land on: wantID if it still exists, otherwise whichever
// sits at the current cursor row (so a delete lands on a neighbour, not
// on the first row).
func (a app) loadWorkflows(wantID int64) tea.Cmd {
	cursor := a.wfCursor
	return func() tea.Msg {
		wfs, err := a.store.ListWorkflows(a.ctx)
		if err != nil {
			return errMsg{err}
		}
		var sel int64
		for _, w := range wfs {
			if w.ID == wantID {
				sel = w.ID
			}
		}
		if sel == 0 && len(wfs) > 0 {
			sel = wfs[max(min(cursor, len(wfs)-1), 0)].ID
		}
		var steps []workflow.Step
		var edges []workflow.Edge
		if sel != 0 {
			if steps, edges, err = a.loadGraph(sel); err != nil {
				return errMsg{err}
			}
		}
		return workflowsLoadedMsg{workflows: wfs, selected: sel, steps: steps, edges: edges}
	}
}

// loadSteps fetches one workflow's steps and edges, for a cursor move.
func (a app) loadSteps(workflowID int64) tea.Cmd {
	return func() tea.Msg {
		steps, edges, err := a.loadGraph(workflowID)
		if err != nil {
			return errMsg{err}
		}
		return stepsLoadedMsg{workflowID: workflowID, steps: steps, edges: edges}
	}
}

// loadGraph reads a workflow's steps and edges together; both loads use
// it so the two are never from different moments.
func (a app) loadGraph(workflowID int64) ([]workflow.Step, []workflow.Edge, error) {
	steps, err := a.store.ListSteps(a.ctx, workflowID)
	if err != nil {
		return nil, nil, err
	}
	edges, err := a.store.ListEdges(a.ctx, workflowID)
	if err != nil {
		return nil, nil, err
	}
	return steps, edges, nil
}

// addStep appends an agent step to workflowID and, when the step before
// it had no edges, links them with done -> new step. That is the edge a
// linear workflow wants every time, so it is the default; a step that
// already routes something is left alone, since its author has decided
// where its outcomes go.
func (a app) addStep(workflowID int64, name string) tea.Cmd {
	return func() tea.Msg {
		before, err := a.store.ListSteps(a.ctx, workflowID)
		if err != nil {
			return errMsg{err}
		}
		st, err := a.store.AddStep(a.ctx, workflowID, name, workflow.StepAgent)
		if err != nil {
			return errMsg{err}
		}
		status := flash{kind: flashAdd, text: "step: " + st.Name}
		if len(before) > 0 {
			prev := before[len(before)-1]
			out, err := a.store.OutgoingEdges(a.ctx, prev.ID)
			if err != nil {
				return errMsg{err}
			}
			if len(out) == 0 {
				if _, err := a.store.SetEdge(a.ctx, prev.ID, workflow.OutcomeDone, st.ID, nil); err != nil {
					return errMsg{err}
				}
				status.text += fmt.Sprintf(" (%s %s -> %s)", prev.Name, workflow.OutcomeDone, st.Name)
			}
		}
		return stepCreatedMsg{st: st, status: status}
	}
}

// deleteWorkflow removes a workflow and its steps. The store refuses
// while a run of it is live; that refusal is reworded here so the flash
// says what to do about it.
func (a app) deleteWorkflow(w workflow.Workflow) tea.Cmd {
	return func() tea.Msg {
		if err := a.store.DeleteWorkflow(a.ctx, w.ID); err != nil {
			return inUseOrErr(err, w.Name)
		}
		return refreshMsg{status: flash{kind: flashDone, text: "deleted " + w.Name}}
	}
}

// deleteStep removes a step; same live-run refusal as deleteWorkflow.
func (a app) deleteStep(st workflow.Step) tea.Cmd {
	return func() tea.Msg {
		if err := a.store.DeleteStep(a.ctx, st.ID); err != nil {
			return inUseOrErr(err, st.Name)
		}
		return refreshMsg{status: flash{kind: flashDone, text: "deleted step " + st.Name}}
	}
}

// deleteEdge removes one edge; the cursor stays on its step.
func (a *app) deleteEdge(e workflow.Edge) tea.Cmd {
	a.wfSelectStepID = e.FromStepID
	text := fmt.Sprintf("deleted edge %s on %s", a.stepName(e.FromStepID), e.Outcome)
	return a.mutate(flash{kind: flashDone, text: text}, func() error {
		return a.store.DeleteEdge(a.ctx, e.ID)
	})
}

// inUseOrErr turns the store's ErrInUse into a flash that names the
// thing and what blocks it; any other error is reported as-is.
func inUseOrErr(err error, name string) tea.Msg {
	if errors.Is(err, workflow.ErrInUse) {
		return statusMsg{isErr: true,
			text: fmt.Sprintf("cannot delete %s: an active run still uses it (%v)", name, err)}
	}
	return errMsg{err}
}

// setStepAttr runs write, a single-attribute store update for step stepID,
// and keeps the cursor on the step across the reload that follows. Each
// keypress writes only the attribute it changed rather than the whole
// cached step: a mutation's reload can still be in flight when the next
// key lands, and rewriting every column from the stale copy would undo the
// earlier write.
func (a *app) setStepAttr(stepID int64, status flash, write func() error) tea.Cmd {
	a.wfSelectStepID = stepID
	return a.mutate(status, write)
}

// moveStep swaps the step at from with the one at to and writes the new
// order. The cursor follows the moved step.
func (a *app) moveStep(from, to int) tea.Cmd {
	if from < 0 || to < 0 || from >= len(a.wfSteps) || to >= len(a.wfSteps) || from == to {
		return nil
	}
	ids := make([]int64, len(a.wfSteps))
	for i, st := range a.wfSteps {
		ids[i] = st.ID
	}
	ids[from], ids[to] = ids[to], ids[from]
	moved := a.wfSteps[from]
	workflowID := a.wfStepsFor
	a.wfStepCursor = to
	a.wfSelectStepID = moved.ID
	return a.mutate(flash{kind: flashEdit, text: fmt.Sprintf("%s → step %d", moved.Name, to+1)}, func() error {
		return a.store.ReorderSteps(a.ctx, workflowID, ids)
	})
}

// editStepPromptCmd opens a step's prompt template in $EDITOR, the same
// temp-file round-trip as a task body.
func editStepPromptCmd(st workflow.Step) tea.Cmd {
	id := st.ID
	return editInEditorCmd(fmt.Sprintf("tend-step-%d-*.md", st.ID), st.PromptMD, func(path string, err error) tea.Msg {
		return stepEditorFinishedMsg{stepID: id, path: path, err: err}
	})
}

// saveStepPrompt reads the edited temp file back into the step. The
// template is checked on the way in; an invalid one is still saved (the
// edit must not be lost) but the flash says so.
func (a app) saveStepPrompt(stepID int64, path string) tea.Cmd {
	return func() tea.Msg {
		defer os.Remove(path)
		b, err := os.ReadFile(path)
		if err != nil {
			return errMsg{fmt.Errorf("reading edited prompt: %w", err)}
		}
		if err := a.store.SetStepPrompt(a.ctx, stepID, string(b)); err != nil {
			return errMsg{err}
		}
		if err := workflow.ValidatePrompt(string(b)); err != nil {
			return refreshMsg{status: flash{isErr: true, text: "prompt saved, but: " + err.Error()}}
		}
		return refreshMsg{status: flash{kind: flashEdit, text: "prompt saved"}}
	}
}

// validateSelectedWorkflow is `v`: it checks the selected workflow's graph
// and prompts (workflow.Validate) and shows the problems under the steps,
// where they stay -- recomputed on every reload -- until they are fixed.
// The flash carries the first problem and a count. Validate is pure, so
// it runs here in Update; the flash is a Cmd only so it arrives like every
// other outcome.
func (a *app) validateSelectedWorkflow() tea.Cmd {
	w, ok := a.selectedWorkflow()
	if !ok {
		return nil
	}
	steps, edges := a.wfSteps, a.wfEdges
	if a.wfStepsFor != w.ID {
		steps, edges = nil, nil
	}
	a.wfProblems, a.wfProblemsFor = workflow.Validate(steps, edges), w.ID
	if n := len(a.wfProblems); n > 0 {
		text := a.wfProblems[0].String()
		if n > 1 {
			text += fmt.Sprintf(" (+%d more)", n-1)
		}
		return statusCmd(flash{isErr: true, text: text})
	}
	noun := "steps"
	if len(steps) == 1 {
		noun = "step"
	}
	return statusCmd(flash{kind: flashDone, text: fmt.Sprintf("%s: %d %s, graph and prompts valid", w.Name, len(steps), noun)})
}

// --- edge add / edit flow ---

// startEdgeDraft opens the outcome prompt for a new edge leaving st, or
// for existing (seeded with its values) when editing. The draft carries
// the answers through the step picker to the max-iterations prompt;
// escaping any stage drops it.
func (a *app) startEdgeDraft(st workflow.Step, existing *workflow.Edge) tea.Cmd {
	d := &edgeDraft{fromStepID: st.ID}
	value := ""
	if existing != nil {
		d.editID, d.oldOutcome = existing.ID, existing.Outcome
		d.outcome, d.toStepID, d.max = existing.Outcome, existing.ToStepID, existing.MaxIterations
		value = existing.Outcome
	}
	cmd := a.openPromptWith(promptEdgeOutcome, fmt.Sprintf("%s on outcome: ", st.Name), value, st.ID)
	a.wfEdgeDraft = d
	return cmd
}

// submitEdgeOutcome is stage one done: normalize the outcome the way
// edges are stored, then ask which step it leads to.
func (a app) submitEdgeOutcome(draft *edgeDraft, value string) (tea.Model, tea.Cmd) {
	if draft == nil {
		return a, nil
	}
	o, err := workflow.NormalizeOutcome(value)
	if err != nil {
		a.status = flash{isErr: true, text: "an edge needs an outcome name"}
		return a, nil
	}
	draft.outcome = o
	a.wfEdgeDraft = draft
	a.openEdgeTargetPicker(draft)
	return a, nil
}

// openEdgeTargetPicker arms the picker over the workflow's steps, starting
// on the draft's current target, or -- for a new edge -- the step after
// its source, the linear default.
func (a *app) openEdgeTargetPicker(draft *edgeDraft) {
	a.wfPickerOpen, a.wfPickerKind, a.wfPickerStepID, a.wfPickerSel = true, wfPickEdgeTarget, draft.fromStepID, 0
	want := draft.toStepID
	if want == 0 {
		for i, st := range a.wfSteps {
			if st.ID == draft.fromStepID && i+1 < len(a.wfSteps) {
				want = a.wfSteps[i+1].ID
			}
		}
	}
	for i, o := range a.wfPickerOptions() {
		if o.value == strconv.FormatInt(want, 10) {
			a.wfPickerSel = i
		}
	}
}

// submitEdgeMax is the last stage: parse the bound (blank = unbounded)
// and write the edge. A renamed outcome deletes the old row first, since
// SetEdge upserts on (from, outcome). The cursors land on the edge.
func (a *app) submitEdgeMax(draft *edgeDraft, value string) tea.Cmd {
	if draft == nil || draft.toStepID == 0 {
		return nil
	}
	if value != "" {
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil || n < 1 {
			return statusCmd(flash{isErr: true, text: fmt.Sprintf("max iterations %q: need a whole number of at least 1, or blank for unbounded", value)})
		}
		draft.max = &n
	} else {
		draft.max = nil
	}
	a.wfSelectStepID, a.wfSelectOutcome = draft.fromStepID, draft.outcome
	d := *draft
	text := fmt.Sprintf("edge: %s on %s -> %s", a.stepName(d.fromStepID), d.outcome, a.stepName(d.toStepID))
	if d.max != nil {
		text += fmt.Sprintf(" (max %d)", *d.max)
	}
	return func() tea.Msg {
		if d.editID != 0 && d.oldOutcome != d.outcome {
			if err := a.store.DeleteEdge(a.ctx, d.editID); err != nil {
				return errMsg{err}
			}
		}
		if _, err := a.store.SetEdge(a.ctx, d.fromStepID, d.outcome, d.toStepID, d.max); err != nil {
			return errMsg{err}
		}
		return refreshMsg{status: flash{kind: flashEdit, text: text}}
	}
}

// --- picker overlay ---

// wfPickerOptions is the picker's rows for its current kind. For an edge
// target the rows are the workflow's steps, numbered as the preview
// numbers them, with the step id as the value.
func (a app) wfPickerOptions() []wfPickerOption {
	switch a.wfPickerKind {
	case wfPickPermission:
		return stepPermissionOptions
	case wfPickEdgeTarget:
		opts := make([]wfPickerOption, 0, len(a.wfSteps))
		for _, st := range a.wfSteps {
			opts = append(opts, wfPickerOption{label: st.Name, value: strconv.FormatInt(st.ID, 10)})
		}
		return opts
	}
	return stepModelOptions
}

// openWfPicker arms the picker for one step attribute, starting on the
// step's current value so Enter is a no-op rather than a surprise.
func (a *app) openWfPicker(kind wfPickerKind, st workflow.Step) {
	a.wfPickerOpen, a.wfPickerKind, a.wfPickerStepID, a.wfPickerSel = true, kind, st.ID, 0
	current := st.Model
	if kind == wfPickPermission {
		current = st.PermissionMode
	}
	for i, o := range a.wfPickerOptions() {
		if o.value == current {
			a.wfPickerSel = i
		}
	}
}

func (a *app) closeWfPicker() {
	a.wfPickerOpen, a.wfPickerStepID, a.wfPickerSel = false, 0, 0
}

// handleWfPickerKey owns the keyboard while the picker is open: arrows or
// ctrl-n/ctrl-p move, a digit picks directly, Enter applies, esc dismisses.
func (a app) handleWfPickerKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	opts := a.wfPickerOptions()
	apply := func(idx int) (tea.Model, tea.Cmd) {
		kind, stepID := a.wfPickerKind, a.wfPickerStepID
		a.closeWfPicker()
		st, ok := a.selectedStep()
		if !ok || st.ID != stepID || idx < 0 || idx >= len(opts) {
			a.wfEdgeDraft = nil
			return a, nil
		}
		o := opts[idx]
		switch kind {
		case wfPickEdgeTarget:
			draft := a.wfEdgeDraft
			if draft == nil || draft.fromStepID != stepID {
				a.wfEdgeDraft = nil
				return a, nil
			}
			draft.toStepID, _ = strconv.ParseInt(o.value, 10, 64)
			seed := ""
			if draft.max != nil {
				seed = strconv.FormatInt(*draft.max, 10)
			}
			cmd := a.openPromptWith(promptEdgeMax,
				fmt.Sprintf("%s on %s -> %s · max iterations (blank = unbounded): ", st.Name, draft.outcome, o.label),
				seed, stepID)
			a.wfEdgeDraft = draft
			return a, cmd
		case wfPickPermission:
			status := flash{kind: flashEdit, text: fmt.Sprintf("%s permission mode → %s", st.Name, o.label)}
			return a, a.setStepAttr(st.ID, status, func() error {
				return a.store.SetStepPermissionMode(a.ctx, st.ID, o.value)
			})
		}
		status := flash{kind: flashEdit, text: fmt.Sprintf("%s model → %s", st.Name, o.label)}
		return a, a.setStepAttr(st.ID, status, func() error {
			return a.store.SetStepModel(a.ctx, st.ID, o.value)
		})
	}

	switch msg.String() {
	case "esc":
		a.closeWfPicker()
		a.wfEdgeDraft = nil
		return a, nil
	case "enter":
		return apply(a.wfPickerSel)
	case "up", "ctrl+p", "k":
		if a.wfPickerSel > 0 {
			a.wfPickerSel--
		}
		return a, nil
	case "down", "ctrl+n", "j":
		if a.wfPickerSel < len(opts)-1 {
			a.wfPickerSel++
		}
		return a, nil
	}
	if len(msg.Text) == 1 && msg.Text[0] >= '1' && msg.Text[0] <= '9' {
		if idx := int(msg.Text[0] - '1'); idx < len(opts) {
			return apply(idx)
		}
	}
	return a, nil
}

// wfPickerView renders the chooser box in the project picker's mould.
func (a app) wfPickerView() string {
	s, g := a.styles, a.styles.Glyphs
	w := max(a.width, 20)
	cb := s.CardBorder
	hbar := strings.Repeat(g.RuleH, w-4)

	row := func(content string) string {
		gap := max(w-5-lipgloss.Width(content), 0)
		return "  " + cb.Render(g.RuleV) + " " + content +
			strings.Repeat(" ", gap) + cb.Render(g.RuleV)
	}

	name := ""
	if st, ok := a.selectedStep(); ok {
		name = truncTail(st.Name, max(w-30, 10), g.Ellipsis)
	}
	var title string
	switch {
	case a.wfPickerKind == wfPickEdgeTarget && a.wfEdgeDraft != nil:
		title = s.Title.Render(fmt.Sprintf("%s on %s -> ", name, a.wfEdgeDraft.outcome)) + s.Dimmed.Render("which step?")
	case a.wfPickerKind == wfPickPermission:
		title = s.Title.Render("permission mode for ") + s.Dimmed.Render(name)
	default:
		title = s.Title.Render("model for ") + s.Dimmed.Render(name)
	}
	lines := []string{"  " + cb.Render(g.BoxTL+hbar+g.BoxTR)}
	lines = append(lines, row(s.Accent.Bold(true).Render(g.CaretClosed+" ")+title))
	lines = append(lines, "  "+cb.Render(g.TeeRight+hbar+g.TeeLeft))

	opts := a.wfPickerOptions()
	sel := min(a.wfPickerSel, len(opts)-1)
	for i, o := range opts {
		num := fmt.Sprintf("%d ", i+1)
		var content string
		if i == sel {
			content = s.SelBar.Render(g.SelBar+" ") + s.Accent.Render(num) + s.Title.Render(o.label)
		} else {
			content = "  " + s.Muted.Render(num) + s.Dimmed.Render(o.label)
		}
		lines = append(lines, row(content))
	}
	lines = append(lines, "  "+cb.Render(g.BoxBL+hbar+g.BoxBR))
	return strings.Join(lines, "\n")
}

// --- view ---

// workflowsWidths splits the body: a narrow workflows column on the left,
// the steps on the right, one divider between.
func (a app) workflowsWidths() (leftW, rightW int) {
	w := max(a.width, 20)
	leftW = max(min(w/3, 40), 12)
	return leftW, w - leftW - 1
}

// workflowsView renders the two columns fitted to bodyHeight rows.
func (a app) workflowsView() string {
	h := max(a.bodyHeight, 1)
	leftW, rightW := a.workflowsWidths()

	right, focusLine := a.stepsPaneLines(rightW)
	left := fitPane(a.workflowListLines(leftW), leftW, h, 0)
	right = fitPane(right, rightW, h, paneScroll(len(right), focusLine, h))
	divider := strings.TrimSuffix(strings.Repeat(
		a.styles.Rule.Render(a.styles.Glyphs.RuleV)+"\n", h), "\n")

	return lipgloss.JoinHorizontal(lipgloss.Top,
		strings.Join(left, "\n"), divider, strings.Join(right, "\n"))
}

// paneScroll is the offset that keeps line focus of a total-line pane on
// a height-row screen: none until the focused line would fall off the
// bottom, then just enough.
func paneScroll(total, focus, height int) int {
	if total <= height || focus < height {
		return 0
	}
	return min(focus-height+1, total-height)
}

// workflowListLines lays out the left pane: heading, then one row per
// workflow with its step count right-aligned.
func (a app) workflowListLines(width int) []string {
	s, g := a.styles, a.styles.Glyphs
	focused := a.wfFocus == wfPaneList
	lines := []string{"", "  " + s.SubHeader.Render("WORKFLOWS")}

	if len(a.workflows) == 0 {
		lines = append(lines, "",
			"  "+s.Muted.Render("none yet — press ")+s.FooterKey.Render("n")+s.Muted.Render(" to create one"))
		return lines
	}
	for i, w := range a.workflows {
		selected := i == a.wfCursor
		gutter, gutterStyle := "  ", s.Normal
		if selected {
			gutter, gutterStyle = g.SelBar+" ", s.SelBar
		}
		count := ""
		if w.StepCount > 0 {
			count = fmt.Sprintf("%d", w.StepCount)
		}
		nameStyle := s.Dimmed
		switch {
		case selected && focused:
			nameStyle = s.Title.Bold(true)
		case selected:
			nameStyle = s.Title
		}
		nameW := max(width-runeWidth(gutter)-runeWidth(count)-2, 1)
		label := truncTail(w.Name, nameW, g.Ellipsis)
		gap := max(width-runeWidth(gutter)-runeWidth(label)-runeWidth(count)-1, 0)
		lines = append(lines, gutterStyle.Render(gutter)+nameStyle.Render(label)+
			strings.Repeat(" ", gap)+s.CountLabel.Render(count))
	}
	return lines
}

// stepsPaneLines lays out the right pane and reports which line the
// focused row is on, for scrolling: a heading naming the workflow; one
// row per step (number, name, the graph annotations workflow.Preview
// gives it, then kind · model · permission mode); the selected step's
// EDGES; the validation PROBLEMS once `v` has found some; and the
// selected step's prompt template.
func (a app) stepsPaneLines(width int) (lines []string, focusLine int) {
	s, g := a.styles, a.styles.Glyphs
	focused := a.wfFocus == wfPaneSteps

	w, ok := a.selectedWorkflow()
	if !ok {
		return []string{"", "  " + s.SubHeader.Render("STEPS")}, 0
	}
	lines = []string{"", "  " + s.SubHeader.Render("STEPS") + s.Dimmed.Render(" · "+w.Name)}

	steps, edges := a.wfSteps, a.wfEdges
	if a.wfStepsFor != w.ID {
		steps, edges = nil, nil
	}
	if len(steps) == 0 {
		lines = append(lines, "",
			"  "+s.Muted.Render("no steps yet — press ")+s.FooterKey.Render("l")+
				s.Muted.Render(" then ")+s.FooterKey.Render("n")+s.Muted.Render(" to add one"))
		return lines, 0
	}

	focusLine = 2 + a.wfStepCursor
	for i, row := range workflow.Preview(steps, edges) {
		st := row.Step
		selected := i == a.wfStepCursor
		gutter, gutterStyle := "  ", s.Normal
		if selected {
			gutter, gutterStyle = g.SelBar+" ", s.SelBar
		}
		num := fmt.Sprintf("%d  ", row.Number)
		meta := string(st.Kind)
		if st.Model != "" {
			meta += " · " + st.Model
		}
		if st.PermissionMode != "" {
			meta += " · " + st.PermissionMode
		}
		if strings.TrimSpace(st.PromptMD) == "" && st.Kind == workflow.StepAgent {
			meta += " · no prompt"
		}
		var notes []string
		for _, n := range row.Annotations() {
			notes = append(notes, "["+n+"]")
		}
		ann := strings.Join(notes, "  ")
		nameStyle := s.Dimmed
		switch {
		case selected && focused:
			nameStyle = s.Title.Bold(true)
		case selected:
			nameStyle = s.Title
		}
		// Budget: gutter, number, name, two spaces, annotations, a gap, meta.
		// Meta yields first when the row is tight, then the annotations.
		fixed := runeWidth(gutter) + runeWidth(num)
		if fixed+8+2+runeWidth(ann)+1+runeWidth(meta) > width {
			meta = ""
		}
		annW := max(min(runeWidth(ann), width-fixed-8-2-1-runeWidth(meta)), 0)
		if ann != "" {
			ann = truncTail(ann, annW, g.Ellipsis)
		}
		nameW := max(width-fixed-runeWidth(meta)-runeWidth(ann)-3, 8)
		label := truncTail(st.Name, nameW, g.Ellipsis)
		annStyle := s.Muted
		if row.End {
			annStyle = s.Faint
		}
		line := gutterStyle.Render(gutter) + s.Muted.Render(num) + nameStyle.Render(label)
		if ann != "" {
			line += "  " + annStyle.Render(ann)
		}
		gap := max(width-fixed-runeWidth(label)-runeWidth(ann)-runeWidth(meta)-3, 1)
		if ann == "" {
			gap += 2
		}
		lines = append(lines, line+strings.Repeat(" ", gap)+s.Faint.Render(meta))
	}

	st, ok := a.selectedStep()
	if !ok {
		return lines, focusLine
	}

	// EDGES: what leaves the selected step, one row per outcome.
	lines = append(lines, "", "  "+s.SubHeader.Render("EDGES")+s.Dimmed.Render(" · "+st.Name))
	stepEdges := a.selectedStepEdges()
	edgesFocused := a.wfFocus == wfPaneEdges
	if edgesFocused {
		focusLine = len(lines)
	}
	if len(stepEdges) == 0 {
		hint := s.Muted.Render("none — its one outcome, done, ends the run · ")
		if edgesFocused {
			hint += s.FooterKey.Render("n") + s.Muted.Render(" adds one")
		} else {
			hint += s.FooterKey.Render("l") + s.Muted.Render(" then ") + s.FooterKey.Render("n") + s.Muted.Render(" adds one")
		}
		lines = append(lines, "  "+hint)
	}
	for i, e := range stepEdges {
		selected := i == a.wfEdgeCursor
		gutter, gutterStyle := "  ", s.Normal
		if selected {
			gutter, gutterStyle = g.SelBar+" ", s.SelBar
			if edgesFocused {
				focusLine = len(lines)
			}
		}
		text := fmt.Sprintf("on %s -> %s", e.Outcome, a.stepName(e.ToStepID))
		if e.MaxIterations != nil {
			text += fmt.Sprintf(" (max %d)", *e.MaxIterations)
		}
		style := s.Dimmed
		switch {
		case selected && edgesFocused:
			style = s.Title.Bold(true)
		case selected:
			style = s.Title
		}
		lines = append(lines, gutterStyle.Render(gutter)+style.Render(truncTail(text, max(width-4, 8), g.Ellipsis)))
	}

	// PROBLEMS: only once `v` has been pressed on this workflow, and only
	// while any remain.
	if len(a.wfProblems) > 0 {
		lines = append(lines, "", "  "+s.SubHeader.Render("PROBLEMS")+
			s.Dimmed.Render(fmt.Sprintf(" · %d", len(a.wfProblems))))
		mark := s.State[task.StateBlocked]
		for _, p := range a.wfProblems {
			for j, l := range strings.Split(ansi.Wrap(p.String(), max(width-6, 10), ""), "\n") {
				lead := "  " + mark.Render(g.State[task.StateBlocked]) + " "
				if j > 0 {
					lead = "    "
				}
				lines = append(lines, lead+s.Dimmed.Render(l))
			}
		}
	}

	lines = append(lines, "", "  "+s.SubHeader.Render("PROMPT")+s.Dimmed.Render(" · "+st.Name))
	if strings.TrimSpace(st.PromptMD) == "" {
		lines = append(lines, "  "+s.Muted.Render("empty — press ")+s.FooterKey.Render("e")+
			s.Muted.Render(" to write it in $EDITOR"))
		return lines, focusLine
	}
	for _, para := range strings.Split(strings.TrimRight(st.PromptMD, "\n"), "\n") {
		if para == "" {
			lines = append(lines, "")
			continue
		}
		for _, l := range strings.Split(ansi.Wrap(para, max(width-4, 10), ""), "\n") {
			lines = append(lines, "  "+s.Dimmed.Render(l))
		}
	}
	return lines, focusLine
}

// workflowsHints is the footer for the view, per focused pane.
func (a app) workflowsHints() [][2]string {
	switch a.wfFocus {
	case wfPaneEdges:
		return [][2]string{
			{"j/k", "move"}, {"h/esc", "to steps"}, {"n", "add edge"}, {"e", "edit"},
			{"dd", "delete"}, {"v", "validate"}, {"?", "help"},
		}
	case wfPaneSteps:
		return [][2]string{
			{"j/k", "move"}, {"h", "to workflows"}, {"l/⏎", "edges"}, {"n", "add step"}, {"e", "edit prompt"},
			{"m", "model"}, {"p", "permission"}, {"t", "agent/gate"}, {"J/K", "reorder"},
			{"dd", "delete"}, {"v", "validate"},
		}
	}
	return [][2]string{
		{"j/k", "move"}, {"l/⏎", "to steps"}, {"n", "new"}, {"R", "rename"}, {"D", "duplicate"},
		{"dd", "delete"}, {"v", "validate"}, {"esc/q", "back"}, {"?", "help"},
	}
}
