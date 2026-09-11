package mcpserver

import (
	"context"
	"strings"
	"testing"

	"github.com/jwstover/tend/internal/task"
	"github.com/jwstover/tend/internal/workflow"
)

// stepNamed finds a step of a graph by name; the tests address steps by
// name because the fake allocates ids.
func stepNamed(t *testing.T, g workflowGraphOut, name string) authoredStepOut {
	t.Helper()
	for _, st := range g.Steps {
		if st.Name == name {
			return st
		}
	}
	t.Fatalf("workflow %q has no step %q: %+v", g.Name, name, g.Steps)
	return authoredStepOut{}
}

// edgeOn finds the edge leaving st on outcome, nil when none does.
func edgeOn(st authoredStepOut, outcome string) *authoredEdgeOut {
	for i := range st.Edges {
		if st.Edges[i].Outcome == outcome {
			return &st.Edges[i]
		}
	}
	return nil
}

// The authoring tools are for every session, bound to a step run or not:
// drafting a workflow is ordinary task work.
func TestWorkflowToolsRegisteredForEverySession(t *testing.T) {
	store := newFakeStore(task.Task{ID: 1, Title: "bound"})
	names := toolNames(t, dial(t, store, 1))
	for _, want := range []string{
		"list_workflows", "get_workflow", "create_workflow", "update_workflow",
		"add_workflow_step", "update_workflow_step", "set_step_prompt",
		"reorder_workflow_steps", "delete_workflow_step",
		"add_workflow_edge", "delete_workflow_edge",
	} {
		if !names[want] {
			t.Errorf("ordinary session lacks authoring tool %q", want)
		}
	}
}

// The acceptance line for #188: an agent drafts implement / review / gate
// / ship entirely through the tools. Steps added in order come out linked
// by done edges; the review's approve and reject routes are then added
// and its default done edge dropped, and the preview and validator read
// the finished graph the way the TUI would.
func TestAgentDraftsReviewLoopWorkflowThroughTools(t *testing.T) {
	store := newFakeStore(task.Task{ID: 1, Title: "bound"})
	cs := dial(t, store, 1)

	wf := callTool[workflowGraphOut](t, cs, "create_workflow", map[string]any{
		"name": "implement-review-ship", "description": "code review loop",
	})
	if wf.Name != "implement-review-ship" || wf.Description != "code review loop" || len(wf.Steps) != 0 {
		t.Fatalf("create_workflow = %+v", wf)
	}

	implementPrompt := "Implement {{.Task.Title}}.\n\n{{if .Feedback}}Reviewer feedback: {{.Feedback}}{{else}}Context: {{.Input}}{{end}}"
	callTool[workflowGraphOut](t, cs, "add_workflow_step", map[string]any{
		"workflow_id": wf.ID, "name": "implement", "prompt_md": implementPrompt, "model": "opus",
	})
	callTool[workflowGraphOut](t, cs, "add_workflow_step", map[string]any{
		"workflow_id": wf.ID, "name": "review",
		"prompt_md": "Review the change described in {{.Input}}; finish with approve or reject.",
	})
	callTool[workflowGraphOut](t, cs, "add_workflow_step", map[string]any{
		"workflow_id": wf.ID, "name": "gate", "kind": "gate",
	})
	g := callTool[workflowGraphOut](t, cs, "add_workflow_step", map[string]any{
		"workflow_id": wf.ID, "name": "ship", "prompt_md": "Open a PR for {{.Input}}.",
	})

	if got := len(g.Steps); got != 4 {
		t.Fatalf("after four add_workflow_step calls the workflow has %d steps: %+v", got, g.Steps)
	}
	for i, name := range []string{"implement", "review", "gate", "ship"} {
		if g.Steps[i].Name != name || g.Steps[i].Position != i+1 {
			t.Errorf("step %d = %s at position %d, want %s at %d", i, g.Steps[i].Name, g.Steps[i].Position, name, i+1)
		}
	}
	implement, review, gate, ship := g.Steps[0], g.Steps[1], g.Steps[2], g.Steps[3]
	if implement.Model != "opus" || implement.PromptMD != implementPrompt {
		t.Errorf("implement = %+v, want model opus and the prompt as given", implement)
	}
	if gate.Kind != "gate" || gate.PromptMD != "" {
		t.Errorf("gate step = %+v, want kind gate with no prompt", gate)
	}
	// The default link: each step's done leads to the one added after it.
	for _, pair := range []struct {
		from authoredStepOut
		to   authoredStepOut
	}{{implement, review}, {review, gate}, {gate, ship}} {
		e := edgeOn(pair.from, "done")
		if e == nil || e.ToStepID != pair.to.ID || e.ToStep != pair.to.Name {
			t.Errorf("%s edges = %+v, want done -> %s", pair.from.Name, pair.from.Edges, pair.to.Name)
		}
	}
	if len(ship.Edges) != 0 {
		t.Errorf("the last step has edges: %+v", ship.Edges)
	}

	// Route the review: reject loops back (bounded), approve goes on to
	// the gate, and the default done route is dropped.
	callTool[workflowGraphOut](t, cs, "add_workflow_edge", map[string]any{
		"from_step_id": review.ID, "outcome": "reject", "to_step_id": implement.ID, "max_iterations": 3,
	})
	callTool[workflowGraphOut](t, cs, "add_workflow_edge", map[string]any{
		"from_step_id": review.ID, "outcome": "Approve", "to_step_id": gate.ID,
	})
	g = callTool[workflowGraphOut](t, cs, "delete_workflow_edge", map[string]any{
		"from_step_id": review.ID, "outcome": "done",
	})

	review = stepNamed(t, g, "review")
	if len(review.Edges) != 2 {
		t.Fatalf("review edges = %+v, want approve and reject only", review.Edges)
	}
	if e := edgeOn(review, "approve"); e == nil || e.ToStepID != gate.ID || e.MaxIterations != nil {
		t.Errorf("review approve edge = %+v, want -> gate, unbounded", e)
	}
	if e := edgeOn(review, "reject"); e == nil || e.ToStepID != implement.ID || e.MaxIterations == nil || *e.MaxIterations != 3 {
		t.Errorf("review reject edge = %+v, want -> implement (max 3)", e)
	}

	steps, _ := store.ListSteps(context.Background(), wf.ID)
	edges, _ := store.ListEdges(context.Background(), wf.ID)
	if want := workflow.PreviewText(steps, edges); g.Preview != want {
		t.Errorf("preview = %q, want %q (workflow.PreviewText of the stored graph)", g.Preview, want)
	}
	wantLines := []string{
		"1. implement",
		"2. review  [approve -> 3]  [reject -> 1 (max 3)]",
		"3. gate",
		"4. ship  [done -> end]",
	}
	if got := strings.Join(wantLines, "\n"); g.Preview != got {
		t.Errorf("preview =\n%s\nwant\n%s", g.Preview, got)
	}
	if len(g.Problems) != 0 {
		t.Errorf("the finished workflow has problems: %v", g.Problems)
	}

	// And list_workflows sees it, with its step count.
	list := callTool[workflowsOut](t, cs, "list_workflows", nil)
	if len(list.Workflows) != 1 || list.Workflows[0].ID != wf.ID || list.Workflows[0].StepCount != 4 {
		t.Errorf("list_workflows = %+v, want the one workflow with 4 steps", list.Workflows)
	}
}

func TestGetWorkflowByNameOrID(t *testing.T) {
	store := newFakeStore(task.Task{ID: 1, Title: "bound"})
	seedStepRun(store)
	cs := dial(t, store, 1)

	byName := callTool[workflowGraphOut](t, cs, "get_workflow", map[string]any{"name": "FIX A Bug"})
	if byName.ID != 1 || byName.Name != "fix a bug" {
		t.Errorf("get_workflow(name) = %+v, want workflow 1 matched case-insensitively", byName)
	}
	if len(byName.Steps) != 3 || byName.Steps[1].Name != "review" || len(byName.Steps[1].Edges) != 2 {
		t.Errorf("get_workflow steps = %+v, want implement / review (2 edges) / ship", byName.Steps)
	}

	byID := callTool[workflowGraphOut](t, cs, "get_workflow", map[string]any{"workflow_id": 1})
	if byID.Name != "fix a bug" {
		t.Errorf("get_workflow(workflow_id) = %+v", byID)
	}

	msg := callToolErr(t, cs, "get_workflow", map[string]any{})
	if !strings.Contains(msg, "workflow_id") || !strings.Contains(msg, "name") {
		t.Errorf("get_workflow with neither argument: %q, want it to ask for workflow_id or name", msg)
	}
	if msg := callToolErr(t, cs, "get_workflow", map[string]any{"name": "nope"}); !strings.Contains(msg, "not found") {
		t.Errorf("get_workflow(unknown name) = %q, want not found", msg)
	}
	if msg := callToolErr(t, cs, "get_workflow", map[string]any{"workflow_id": 99}); !strings.Contains(msg, "not found") {
		t.Errorf("get_workflow(unknown id) = %q, want not found", msg)
	}
}

// A template that does not render is refused before the write, since it
// would fail every run at launch.
func TestSetStepPromptRefusesABrokenTemplate(t *testing.T) {
	store := newFakeStore(task.Task{ID: 1, Title: "bound"})
	seedStepRun(store)
	store.steps[10] = func(st workflow.Step) workflow.Step { st.PromptMD = "original"; return st }(store.steps[10])
	cs := dial(t, store, 1)

	msg := callToolErr(t, cs, "set_step_prompt", map[string]any{"step_id": 10, "prompt_md": "Do {{.Nope}}"})
	if !strings.Contains(msg, "invalid prompt template") {
		t.Errorf("set_step_prompt error = %q, want invalid prompt template", msg)
	}
	if got := store.steps[10].PromptMD; got != "original" {
		t.Errorf("a refused prompt was written: %q", got)
	}

	g := callTool[workflowGraphOut](t, cs, "set_step_prompt", map[string]any{
		"step_id": 10, "prompt_md": "Implement {{.Task.Title}} in {{.Cwd}}",
	})
	if got := stepNamed(t, g, "implement").PromptMD; got != "Implement {{.Task.Title}} in {{.Cwd}}" {
		t.Errorf("prompt after set_step_prompt = %q", got)
	}
	if store.steps[10].PromptMD != "Implement {{.Task.Title}} in {{.Cwd}}" {
		t.Errorf("stored prompt = %q", store.steps[10].PromptMD)
	}

	if msg := callToolErr(t, cs, "set_step_prompt", map[string]any{"step_id": 999, "prompt_md": "x"}); !strings.Contains(msg, "not found") {
		t.Errorf("set_step_prompt on an unknown step = %q, want not found", msg)
	}
}

func TestAddWorkflowStepOptions(t *testing.T) {
	store := newFakeStore(task.Task{ID: 1, Title: "bound"})
	cs := dial(t, store, 1)
	wf := callTool[workflowGraphOut](t, cs, "create_workflow", map[string]any{"name": "opts"})

	callTool[workflowGraphOut](t, cs, "add_workflow_step", map[string]any{
		"workflow_id": wf.ID, "name": "first", "model": "inherit", "permission_mode": "acceptEdits",
	})
	g := callTool[workflowGraphOut](t, cs, "add_workflow_step", map[string]any{
		"workflow_id": wf.ID, "name": "unlinked", "link_from_previous": false,
	})
	first := stepNamed(t, g, "first")
	if len(first.Edges) != 0 {
		t.Errorf("link_from_previous false still linked: %+v", first.Edges)
	}
	if first.Model != "" || first.PermissionMode != "acceptEdits" {
		t.Errorf("first = %+v, want model inherit stored as empty and permission_mode acceptEdits", first)
	}
	if store.steps[first.ID].Model != "" {
		t.Errorf("stored model = %q, want empty for inherit", store.steps[first.ID].Model)
	}

	g = callTool[workflowGraphOut](t, cs, "add_workflow_step", map[string]any{
		"workflow_id": wf.ID, "name": "check", "kind": "gate", "prompt_md": "should be dropped",
	})
	gate := stepNamed(t, g, "check")
	if gate.Kind != "gate" || gate.PromptMD != "" {
		t.Errorf("gate step = %+v, want kind gate with prompt_md ignored", gate)
	}
	// The default link fires for the gate: unlinked had no edges.
	if e := edgeOn(stepNamed(t, g, "unlinked"), "done"); e == nil || e.ToStepID != gate.ID {
		t.Errorf("unlinked edges = %+v, want done -> check", stepNamed(t, g, "unlinked").Edges)
	}

	before := len(store.steps)
	if msg := callToolErr(t, cs, "add_workflow_step", map[string]any{
		"workflow_id": wf.ID, "name": "bad", "kind": "robot",
	}); !strings.Contains(msg, `"robot"`) {
		t.Errorf("unknown kind error = %q, want it to name the kind", msg)
	}
	if msg := callToolErr(t, cs, "add_workflow_step", map[string]any{
		"workflow_id": wf.ID, "name": "bad", "permission_mode": "yolo",
	}); !strings.Contains(msg, `"yolo"`) || !strings.Contains(msg, "acceptEdits") {
		t.Errorf("unknown permission mode error = %q, want it to name the mode and list the known ones", msg)
	}
	if msg := callToolErr(t, cs, "add_workflow_step", map[string]any{
		"workflow_id": wf.ID, "name": "bad", "prompt_md": "{{.Nope}}",
	}); !strings.Contains(msg, "invalid prompt template") {
		t.Errorf("broken prompt error = %q", msg)
	}
	if msg := callToolErr(t, cs, "add_workflow_step", map[string]any{
		"workflow_id": wf.ID, "name": "   ",
	}); !strings.Contains(msg, "empty") {
		t.Errorf("blank name error = %q, want it to say the name is empty", msg)
	}
	if len(store.steps) != before {
		t.Errorf("a refused add_workflow_step still added a step: %d -> %d", before, len(store.steps))
	}
	if msg := callToolErr(t, cs, "add_workflow_step", map[string]any{
		"workflow_id": 404, "name": "orphan",
	}); !strings.Contains(msg, "not found") {
		t.Errorf("add_workflow_step to an unknown workflow = %q, want not found", msg)
	}
}

func TestUpdateWorkflowStepChangesOnlyGivenFields(t *testing.T) {
	store := newFakeStore(task.Task{ID: 1, Title: "bound"})
	seedStepRun(store)
	st := store.steps[11]
	st.PromptMD, st.Model, st.PermissionMode = "Review {{.Input}}", "sonnet", "plan"
	store.steps[11] = st
	cs := dial(t, store, 1)

	g := callTool[workflowGraphOut](t, cs, "update_workflow_step", map[string]any{
		"step_id": 11, "name": " code review ",
	})
	got := stepNamed(t, g, "code review")
	if got.ID != 11 || got.Kind != "agent" || got.PromptMD != "Review {{.Input}}" || got.Model != "sonnet" || got.PermissionMode != "plan" {
		t.Errorf("after renaming only: %+v, want the other fields untouched", got)
	}

	g = callTool[workflowGraphOut](t, cs, "update_workflow_step", map[string]any{
		"step_id": 11, "permission_mode": "inherit", "model": "haiku",
	})
	got = stepNamed(t, g, "code review")
	if got.PermissionMode != "" || got.Model != "haiku" || got.PromptMD != "Review {{.Input}}" {
		t.Errorf("after permission_mode inherit + model haiku: %+v", got)
	}
	if store.steps[11].PermissionMode != "" {
		t.Errorf("stored permission mode = %q, want empty for inherit", store.steps[11].PermissionMode)
	}

	g = callTool[workflowGraphOut](t, cs, "update_workflow_step", map[string]any{"step_id": 11, "kind": "gate"})
	if got = stepNamed(t, g, "code review"); got.Kind != "gate" {
		t.Errorf("kind after update = %q, want gate", got.Kind)
	}

	if msg := callToolErr(t, cs, "update_workflow_step", map[string]any{"step_id": 11, "kind": "robot"}); !strings.Contains(msg, `"robot"`) {
		t.Errorf("unknown kind error = %q", msg)
	}
	if msg := callToolErr(t, cs, "update_workflow_step", map[string]any{"step_id": 11, "permission_mode": "yolo"}); !strings.Contains(msg, `"yolo"`) {
		t.Errorf("unknown permission mode error = %q", msg)
	}
	if msg := callToolErr(t, cs, "update_workflow_step", map[string]any{"step_id": 999, "name": "x"}); !strings.Contains(msg, "not found") {
		t.Errorf("unknown step error = %q", msg)
	}
	if store.steps[11].Kind != workflow.StepGate || store.steps[11].PermissionMode != "" {
		t.Errorf("a refused update changed the step: %+v", store.steps[11])
	}
}

func TestUpdateWorkflowRenamesAndDescribes(t *testing.T) {
	store := newFakeStore(task.Task{ID: 1, Title: "bound"})
	seedStepRun(store)
	store.workflows[2] = workflow.Workflow{ID: 2, Name: "other"}
	cs := dial(t, store, 1)

	g := callTool[workflowGraphOut](t, cs, "update_workflow", map[string]any{
		"workflow_id": 1, "description": "loop until the review passes",
	})
	if g.Name != "fix a bug" || g.Description != "loop until the review passes" {
		t.Errorf("update_workflow(description) = name %q description %q", g.Name, g.Description)
	}
	g = callTool[workflowGraphOut](t, cs, "update_workflow", map[string]any{"workflow_id": 1, "name": "Fix A Bug"})
	if g.Name != "Fix A Bug" || g.Description != "loop until the review passes" {
		t.Errorf("update_workflow(name) = name %q description %q, want the description kept", g.Name, g.Description)
	}
	// Names are unique case-insensitively, as the schema's index insists.
	if msg := callToolErr(t, cs, "update_workflow", map[string]any{"workflow_id": 1, "name": "OTHER"}); !strings.Contains(msg, "UNIQUE") {
		t.Errorf("renaming onto another workflow's name = %q, want the constraint error", msg)
	}
	if msg := callToolErr(t, cs, "create_workflow", map[string]any{"name": "other"}); !strings.Contains(msg, "UNIQUE") {
		t.Errorf("creating a duplicate name = %q, want the constraint error", msg)
	}
}

func TestDeleteWorkflowEdgeNamesTheRoutesOnAMiss(t *testing.T) {
	store := newFakeStore(task.Task{ID: 1, Title: "bound"})
	seedStepRun(store)
	cs := dial(t, store, 1)

	msg := callToolErr(t, cs, "delete_workflow_edge", map[string]any{"from_step_id": 11, "outcome": "ship it"})
	if !strings.Contains(msg, `"review"`) || !strings.Contains(msg, `"ship it"`) || !strings.Contains(msg, "approve, reject") {
		t.Errorf("error = %q, want it to name the step, the outcome and the routes approve, reject", msg)
	}
	if len(store.edges) != 3 {
		t.Errorf("a miss removed an edge: %+v", store.edges)
	}
	// ship has no edges at all; the message says so rather than listing nothing.
	if msg := callToolErr(t, cs, "delete_workflow_edge", map[string]any{"from_step_id": 12, "outcome": "done"}); !strings.Contains(msg, "no edges") {
		t.Errorf("error for a step without edges = %q", msg)
	}

	g := callTool[workflowGraphOut](t, cs, "delete_workflow_edge", map[string]any{"from_step_id": 11, "outcome": " APPROVE "})
	review := stepNamed(t, g, "review")
	if len(review.Edges) != 1 || review.Edges[0].Outcome != "reject" {
		t.Errorf("review edges after deleting approve = %+v, want reject only", review.Edges)
	}
	if len(store.edges) != 2 {
		t.Errorf("store edges = %+v, want 2 left", store.edges)
	}
}

func TestDeleteWorkflowStepRemovesItsEdges(t *testing.T) {
	store := newFakeStore(task.Task{ID: 1, Title: "bound"})
	seedStepRun(store)
	cs := dial(t, store, 1)

	g := callTool[workflowGraphOut](t, cs, "delete_workflow_step", map[string]any{"step_id": 11})
	if len(g.Steps) != 2 || g.Steps[0].Name != "implement" || g.Steps[1].Name != "ship" {
		t.Errorf("steps after deleting review = %+v, want implement, ship", g.Steps)
	}
	if _, ok := store.steps[11]; ok {
		t.Error("review still stored")
	}
	// Every seeded edge touched review, so none is left.
	if len(store.edges) != 0 {
		t.Errorf("edges after deleting review = %+v, want none", store.edges)
	}
	for _, st := range g.Steps {
		if len(st.Edges) != 0 {
			t.Errorf("%s still has edges: %+v", st.Name, st.Edges)
		}
	}
	if msg := callToolErr(t, cs, "delete_workflow_step", map[string]any{"step_id": 11}); !strings.Contains(msg, "not found") {
		t.Errorf("deleting it again = %q, want not found", msg)
	}
}

func TestReorderWorkflowSteps(t *testing.T) {
	store := newFakeStore(task.Task{ID: 1, Title: "bound"})
	seedStepRun(store)
	cs := dial(t, store, 1)

	if msg := callToolErr(t, cs, "reorder_workflow_steps", map[string]any{
		"workflow_id": 1, "step_ids": []int64{10, 11},
	}); !strings.Contains(msg, "2 ids for 3 steps") {
		t.Errorf("partial list error = %q", msg)
	}
	if msg := callToolErr(t, cs, "reorder_workflow_steps", map[string]any{
		"workflow_id": 1, "step_ids": []int64{10, 11, 99},
	}); !strings.Contains(msg, "99") {
		t.Errorf("foreign id error = %q, want it to name step 99", msg)
	}
	if msg := callToolErr(t, cs, "reorder_workflow_steps", map[string]any{
		"workflow_id": 1, "step_ids": []int64{10, 11, 11},
	}); !strings.Contains(msg, "11") {
		t.Errorf("duplicate id error = %q", msg)
	}
	for _, id := range []int64{10, 11, 12} {
		if store.steps[id].SortOrder != 0 {
			t.Errorf("a refused reorder moved step %d to %d", id, store.steps[id].SortOrder)
		}
	}

	g := callTool[workflowGraphOut](t, cs, "reorder_workflow_steps", map[string]any{
		"workflow_id": 1, "step_ids": []int64{12, 10, 11},
	})
	var order []string
	for _, st := range g.Steps {
		order = append(order, st.Name)
		if want := len(order); st.Position != want {
			t.Errorf("%s position = %d, want %d", st.Name, st.Position, want)
		}
	}
	if strings.Join(order, ",") != "ship,implement,review" {
		t.Errorf("order after reorder = %v, want ship, implement, review", order)
	}
	// Edges follow the steps; the preview renumbers accordingly.
	if !strings.Contains(g.Preview, "3. review  [approve -> 1]  [reject -> 2]") {
		t.Errorf("preview after reorder =\n%s", g.Preview)
	}
}

// An empty workflow is a draft in progress, not a broken one.
func TestEmptyWorkflowReportsNoProblems(t *testing.T) {
	store := newFakeStore(task.Task{ID: 1, Title: "bound"})
	cs := dial(t, store, 1)

	g := callTool[workflowGraphOut](t, cs, "create_workflow", map[string]any{"name": "draft"})
	if g.Problems == nil || len(g.Problems) != 0 {
		t.Errorf("problems = %#v, want an empty list (not nil, not \"no steps\")", g.Problems)
	}
	if g.Steps == nil || len(g.Steps) != 0 || g.Preview != "" {
		t.Errorf("empty workflow = %+v, want an empty steps list and empty preview", g)
	}
}

// The validator does show up once there are steps: a review prompt naming
// an outcome no edge routes is flagged, and routing it clears the flag.
func TestGraphProblemsFollowTheEdges(t *testing.T) {
	store := newFakeStore(task.Task{ID: 1, Title: "bound"})
	cs := dial(t, store, 1)
	wf := callTool[workflowGraphOut](t, cs, "create_workflow", map[string]any{"name": "p"})
	callTool[workflowGraphOut](t, cs, "add_workflow_step", map[string]any{
		"workflow_id": wf.ID, "name": "implement", "prompt_md": "Build {{.Task.Title}}",
	})
	g := callTool[workflowGraphOut](t, cs, "add_workflow_step", map[string]any{
		"workflow_id": wf.ID, "name": "review", "prompt_md": "finish with approve or reject",
	})
	if len(g.Problems) != 2 || !strings.Contains(g.Problems[0], `"approve"`) || !strings.Contains(g.Problems[1], `"reject"`) {
		t.Fatalf("problems = %v, want review's unrouted approve and reject", g.Problems)
	}
	implement, review := stepNamed(t, g, "implement"), stepNamed(t, g, "review")
	g = callTool[workflowGraphOut](t, cs, "add_workflow_edge", map[string]any{
		"from_step_id": review.ID, "outcome": "reject", "to_step_id": implement.ID,
	})
	if len(g.Problems) != 2 || !strings.Contains(g.Problems[0], "no max iterations") {
		t.Errorf("problems after an unbounded loop-back = %v, want the loop flagged", g.Problems)
	}
	g = callTool[workflowGraphOut](t, cs, "add_workflow_edge", map[string]any{
		"from_step_id": review.ID, "outcome": "reject", "to_step_id": implement.ID, "max_iterations": 2,
	})
	// The upsert re-pointed the same edge rather than adding a second;
	// review is the last step, so reject is its only edge.
	review = stepNamed(t, g, "review")
	if n := len(review.Edges); n != 1 {
		t.Errorf("review has %d edges after re-setting reject, want just reject: %+v", n, review.Edges)
	}
	if len(g.Problems) != 1 || !strings.Contains(g.Problems[0], `"approve"`) {
		t.Errorf("problems after bounding the loop = %v, want only the unrouted approve", g.Problems)
	}
	if e := edgeOn(review, "reject"); e == nil || e.MaxIterations == nil || *e.MaxIterations != 2 {
		t.Errorf("reject edge = %+v, want max 2", e)
	}
	if msg := callToolErr(t, cs, "add_workflow_edge", map[string]any{
		"from_step_id": review.ID, "outcome": "reject", "to_step_id": implement.ID, "max_iterations": 0,
	}); !strings.Contains(msg, "at least 1") {
		t.Errorf("max_iterations 0 error = %q", msg)
	}
}

func TestAddWorkflowEdgeAcrossWorkflowsIsAnError(t *testing.T) {
	store := newFakeStore(task.Task{ID: 1, Title: "bound"})
	seedStepRun(store)
	cs := dial(t, store, 1)

	other := callTool[workflowGraphOut](t, cs, "create_workflow", map[string]any{"name": "other"})
	g := callTool[workflowGraphOut](t, cs, "add_workflow_step", map[string]any{"workflow_id": other.ID, "name": "lone"})
	lone := stepNamed(t, g, "lone")

	msg := callToolErr(t, cs, "add_workflow_edge", map[string]any{
		"from_step_id": 12, "outcome": "done", "to_step_id": lone.ID,
	})
	if !strings.Contains(msg, "different workflows") {
		t.Errorf("cross-workflow edge error = %q", msg)
	}
	if len(store.edges) != 3 {
		t.Errorf("a refused edge was stored: %+v", store.edges)
	}
	if msg := callToolErr(t, cs, "add_workflow_edge", map[string]any{
		"from_step_id": 12, "outcome": "done", "to_step_id": 999,
	}); !strings.Contains(msg, "not found") {
		t.Errorf("edge to an unknown step = %q, want not found", msg)
	}
}
