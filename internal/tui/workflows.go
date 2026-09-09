package tui

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jwstover/tend/internal/workflow"
)

// The workflows view is where workflow definitions are authored: the
// workflows on the left, the selected one's steps on the right, in
// sort_order. Edges (and the graph they imply) are a later task; for now a
// workflow reads as a linear list, which is what the POC runs.
//
// Every store call is a tea.Cmd, and the two panes are tracked with
// wfFocus rather than the list view's pane type because neither of these
// columns is the task list or the detail viewport.

// wfPane is which workflows-view column owns the keyboard.
type wfPane int

const (
	wfPaneList  wfPane = iota // the workflows
	wfPaneSteps               // the selected workflow's steps
)

// wfPickerKind is which step attribute the picker overlay is choosing.
type wfPickerKind int

const (
	wfPickModel wfPickerKind = iota
	wfPickPermission
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

// setSteps installs a freshly loaded step list and settles the cursor:
// on wfSelectStepID if one is pending and present, otherwise clamped to
// the list.
func (a *app) setSteps(workflowID int64, steps []workflow.Step) {
	a.wfSteps, a.wfStepsFor = steps, workflowID
	if want := a.wfSelectStepID; want != 0 {
		a.wfSelectStepID = 0
		for i, st := range steps {
			if st.ID == want {
				a.wfStepCursor = i
				return
			}
		}
	}
	a.wfStepCursor = max(min(a.wfStepCursor, len(steps)-1), 0)
}

// setWorkflowCursor moves between workflows and fetches the new
// selection's steps.
func (a *app) setWorkflowCursor(row int) tea.Cmd {
	if row < 0 || row >= len(a.workflows) || row == a.wfCursor {
		return nil
	}
	a.wfCursor = row
	a.wfStepCursor = 0
	return a.loadSteps(a.workflows[row].ID)
}

// --- keys ---

// handleWorkflowsKey owns the keyboard in the workflows view. The `dd`
// chord is handled here rather than in handleKey's shared branch because
// what it deletes depends on which of these two panes is focused.
func (a app) handleWorkflowsKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if a.deletePending {
		a.deletePending = false
		a.resize()
		if key.Matches(msg, a.keys.Delete) {
			if a.wfFocus == wfPaneSteps {
				if st, ok := a.selectedStep(); ok {
					return a, a.deleteStep(st)
				}
				return a, nil
			}
			if w, ok := a.selectedWorkflow(); ok {
				return a, a.deleteWorkflow(w)
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
		if a.wfFocus == wfPaneSteps {
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
	}

	if a.wfFocus == wfPaneSteps {
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
	case key.Matches(msg, a.keys.Validate):
		return a, a.validateSelectedWorkflow()
	}

	switch msg.String() {
	case "G":
		return a, a.setWorkflowCursor(len(a.workflows) - 1)
	case "g":
		return a, a.setWorkflowCursor(0)
	}
	return a, nil
}

// handleStepsKey is the right pane: navigate, and edit the selected step
// — name, prompt, model, permission mode, kind, order.
func (a app) handleStepsKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, a.keys.ExpandClose):
		a.wfFocus = wfPaneList
		return a, nil
	case key.Matches(msg, a.keys.ScrollDown):
		if a.wfStepCursor < len(a.wfSteps)-1 {
			a.wfStepCursor++
		}
		return a, nil
	case key.Matches(msg, a.keys.ScrollUp):
		a.wfStepCursor = max(a.wfStepCursor-1, 0)
		return a, nil

	case key.Matches(msg, a.keys.QuickAdd):
		if w, ok := a.selectedWorkflow(); ok {
			return a, a.openPrompt(promptNewStep, fmt.Sprintf("new step in %s: ", w.Name), w.ID)
		}
		return a, nil
	case key.Matches(msg, a.keys.Validate):
		return a, a.validateSelectedWorkflow()
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
		st.Kind = next
		return a, a.updateStep(st, flash{kind: flashEdit, text: fmt.Sprintf("%s → %s", st.Name, next)})
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
		a.wfStepCursor = len(a.wfSteps) - 1
	case "g":
		a.wfStepCursor = 0
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
		return func() tea.Msg {
			st, err := a.store.AddStep(a.ctx, target, value, workflow.StepAgent)
			if err != nil {
				return errMsg{err}
			}
			return stepCreatedMsg{st: st, status: flash{kind: flashAdd, text: "step: " + st.Name}}
		}
	}
	return nil
}

// --- commands ---

// loadWorkflows fetches every workflow and the steps of the one the view
// should land on: wantID if it still exists, otherwise whichever sits at
// the current cursor row (so a delete lands on a neighbour, not on the
// first row).
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
		if sel != 0 {
			if steps, err = a.store.ListSteps(a.ctx, sel); err != nil {
				return errMsg{err}
			}
		}
		return workflowsLoadedMsg{workflows: wfs, selected: sel, steps: steps}
	}
}

// loadSteps fetches one workflow's steps, for a cursor move.
func (a app) loadSteps(workflowID int64) tea.Cmd {
	return func() tea.Msg {
		steps, err := a.store.ListSteps(a.ctx, workflowID)
		if err != nil {
			return errMsg{err}
		}
		return stepsLoadedMsg{workflowID: workflowID, steps: steps}
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

// inUseOrErr turns the store's ErrInUse into a flash that names the
// thing and what blocks it; any other error is reported as-is.
func inUseOrErr(err error, name string) tea.Msg {
	if errors.Is(err, workflow.ErrInUse) {
		return statusMsg{isErr: true,
			text: fmt.Sprintf("cannot delete %s: an active run still uses it (%v)", name, err)}
	}
	return errMsg{err}
}

// updateStep writes a step's editable attributes and keeps the cursor on
// it across the reload.
func (a *app) updateStep(st workflow.Step, status flash) tea.Cmd {
	a.wfSelectStepID = st.ID
	return a.mutate(status, func() error {
		return a.store.UpdateStep(a.ctx, st)
	})
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

// validateSelectedWorkflow checks every step prompt of the selected
// workflow as a template and reports the first failure by step name.
// ValidatePrompt is pure, so this runs in Update; it is a Cmd only so the
// result arrives as a flash like every other outcome.
func (a app) validateSelectedWorkflow() tea.Cmd {
	w, ok := a.selectedWorkflow()
	if !ok {
		return nil
	}
	steps := a.wfSteps
	if a.wfStepsFor != w.ID {
		steps = nil
	}
	return func() tea.Msg {
		for _, st := range steps {
			if err := workflow.ValidatePrompt(st.PromptMD); err != nil {
				return statusMsg{isErr: true, text: fmt.Sprintf("%s: %v", st.Name, err)}
			}
		}
		noun := "prompts"
		if len(steps) == 1 {
			noun = "prompt"
		}
		return statusMsg{kind: flashDone, text: fmt.Sprintf("%s: %d %s valid", w.Name, len(steps), noun)}
	}
}

// --- picker overlay ---

func (a app) wfPickerOptions() []wfPickerOption {
	if a.wfPickerKind == wfPickPermission {
		return stepPermissionOptions
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
			return a, nil
		}
		o := opts[idx]
		var status flash
		if kind == wfPickPermission {
			st.PermissionMode = o.value
			status = flash{kind: flashEdit, text: fmt.Sprintf("%s permission mode → %s", st.Name, o.label)}
		} else {
			st.Model = o.value
			status = flash{kind: flashEdit, text: fmt.Sprintf("%s model → %s", st.Name, o.label)}
		}
		return a, a.updateStep(st, status)
	}

	switch msg.String() {
	case "esc":
		a.closeWfPicker()
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

	what := "model"
	if a.wfPickerKind == wfPickPermission {
		what = "permission mode"
	}
	name := ""
	if st, ok := a.selectedStep(); ok {
		name = truncTail(st.Name, max(w-30, 10), g.Ellipsis)
	}
	lines := []string{"  " + cb.Render(g.BoxTL+hbar+g.BoxTR)}
	lines = append(lines, row(s.Accent.Bold(true).Render(g.CaretClosed+" ")+
		s.Title.Render(what+" for ")+s.Dimmed.Render(name)))
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

// workflowsView renders the two panes fitted to bodyHeight rows.
func (a app) workflowsView() string {
	h := max(a.bodyHeight, 1)
	leftW, rightW := a.workflowsWidths()

	left := fitPane(a.workflowListLines(leftW), leftW, h, 0)
	right := fitPane(a.stepsPaneLines(rightW), rightW, h, a.stepsScroll(h, rightW))
	divider := strings.TrimSuffix(strings.Repeat(
		a.styles.Rule.Render(a.styles.Glyphs.RuleV)+"\n", h), "\n")

	return lipgloss.JoinHorizontal(lipgloss.Top,
		strings.Join(left, "\n"), divider, strings.Join(right, "\n"))
}

// stepsScroll keeps the selected step row on screen when the steps pane
// outgrows the body; the prompt preview beneath scrolls with it.
func (a app) stepsScroll(height, width int) int {
	total := len(a.stepsPaneLines(width))
	if total <= height {
		return 0
	}
	// Row i of the steps sits at line offset 2+i (blank + heading).
	line := 2 + a.wfStepCursor
	if line < height {
		return 0
	}
	return min(line-height+1, total-height)
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

// stepsPaneLines lays out the right pane: heading naming the workflow,
// one row per step (number, name, then kind · model · permission mode),
// and beneath them the selected step's prompt template.
func (a app) stepsPaneLines(width int) []string {
	s, g := a.styles, a.styles.Glyphs
	focused := a.wfFocus == wfPaneSteps

	w, ok := a.selectedWorkflow()
	if !ok {
		return []string{"", "  " + s.SubHeader.Render("STEPS")}
	}
	lines := []string{"", "  " + s.SubHeader.Render("STEPS") + s.Dimmed.Render(" · "+w.Name)}

	steps := a.wfSteps
	if a.wfStepsFor != w.ID {
		steps = nil
	}
	if len(steps) == 0 {
		lines = append(lines, "",
			"  "+s.Muted.Render("no steps yet — press ")+s.FooterKey.Render("l")+
				s.Muted.Render(" then ")+s.FooterKey.Render("n")+s.Muted.Render(" to add one"))
		return lines
	}

	for i, st := range steps {
		selected := i == a.wfStepCursor
		gutter, gutterStyle := "  ", s.Normal
		if selected {
			gutter, gutterStyle = g.SelBar+" ", s.SelBar
		}
		num := fmt.Sprintf("%d  ", i+1)
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
		nameStyle := s.Dimmed
		switch {
		case selected && focused:
			nameStyle = s.Title.Bold(true)
		case selected:
			nameStyle = s.Title
		}
		nameW := max(width-runeWidth(gutter)-runeWidth(num)-runeWidth(meta)-3, 8)
		label := truncTail(st.Name, nameW, g.Ellipsis)
		gap := max(width-runeWidth(gutter)-runeWidth(num)-runeWidth(label)-runeWidth(meta)-1, 1)
		lines = append(lines, gutterStyle.Render(gutter)+s.Muted.Render(num)+nameStyle.Render(label)+
			strings.Repeat(" ", gap)+s.Faint.Render(meta))
	}

	st, ok := a.selectedStep()
	if !ok {
		return lines
	}
	lines = append(lines, "", "  "+s.SubHeader.Render("PROMPT")+s.Dimmed.Render(" · "+st.Name))
	if strings.TrimSpace(st.PromptMD) == "" {
		lines = append(lines, "  "+s.Muted.Render("empty — press ")+s.FooterKey.Render("e")+
			s.Muted.Render(" to write it in $EDITOR"))
		return lines
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
	return lines
}

// workflowsHints is the footer for the view, per focused pane.
func (a app) workflowsHints() [][2]string {
	if a.wfFocus == wfPaneSteps {
		return [][2]string{
			{"j/k", "move"}, {"h", "to workflows"}, {"n", "add step"}, {"e", "edit prompt"},
			{"m", "model"}, {"p", "permission"}, {"t", "agent/gate"}, {"J/K", "reorder"},
			{"dd", "delete"}, {"v", "validate"}, {"esc", "back"},
		}
	}
	return [][2]string{
		{"j/k", "move"}, {"l/⏎", "to steps"}, {"n", "new"}, {"R", "rename"}, {"D", "duplicate"},
		{"dd", "delete"}, {"v", "validate"}, {"esc/q", "back"}, {"?", "help"},
	}
}
