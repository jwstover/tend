package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jwstover/tend/internal/task"
	"github.com/jwstover/tend/internal/workflow"
)

// fakeStore is an in-memory stand-in for *store.Store, just enough of
// mcpserver.Store's method set to exercise the tool surface without a
// real SQLite file. The workflow maps back the step tools (steps_test.go).
type fakeStore struct {
	tasks    map[int64]task.Task
	tags     map[int64][]string
	projects []task.Project
	nextID   int64
	// deps are the task_dependencies rows in insertion order: the task
	// that waits, and the task it waits on (dependencies_test.go).
	deps []depEdge

	workflows map[int64]workflow.Workflow
	steps     map[int64]workflow.Step
	edges     []workflow.Edge
	stepRuns  map[int64]workflow.StepRun
}

func newFakeStore(seed ...task.Task) *fakeStore {
	s := &fakeStore{
		tasks:     make(map[int64]task.Task),
		tags:      make(map[int64][]string),
		projects:  []task.Project{{ID: task.DefaultProjectID, Name: "Unsorted"}},
		workflows: make(map[int64]workflow.Workflow),
		steps:     make(map[int64]workflow.Step),
		stepRuns:  make(map[int64]workflow.StepRun),
	}
	for _, t := range seed {
		s.tasks[t.ID] = t
		if t.ID >= s.nextID {
			s.nextID = t.ID + 1
		}
	}
	return s
}

func (s *fakeStore) GetTask(_ context.Context, id int64) (task.Task, error) {
	t, ok := s.tasks[id]
	if !ok {
		return task.Task{}, errors.New("no such task")
	}
	return t, nil
}

func (s *fakeStore) ListChildren(_ context.Context, parentID int64) ([]task.Task, error) {
	var out []task.Task
	for _, t := range s.tasks {
		if t.ParentID != nil && *t.ParentID == parentID {
			out = append(out, t)
		}
	}
	return out, nil
}

func (s *fakeStore) AddTaskWithBody(_ context.Context, title, body string) (task.Task, error) {
	t := task.Task{ID: s.nextID, Title: title, BodyMD: body, State: task.StateInbox}
	s.tasks[t.ID] = t
	s.nextID++
	return t, nil
}

func (s *fakeStore) AddChild(_ context.Context, parentID int64, title string) (task.Task, error) {
	pid := parentID
	t := task.Task{ID: s.nextID, Title: title, State: task.StateInbox, ParentID: &pid}
	s.tasks[t.ID] = t
	s.nextID++
	return t, nil
}

func (s *fakeStore) SetBody(_ context.Context, id int64, body string) error {
	t, ok := s.tasks[id]
	if !ok {
		return errors.New("no such task")
	}
	t.BodyMD = body
	s.tasks[id] = t
	return nil
}

// AppendBody mirrors the store's paragraph-append semantics.
func (s *fakeStore) AppendBody(_ context.Context, id int64, text string) error {
	t, ok := s.tasks[id]
	if !ok {
		return errors.New("no such task")
	}
	if strings.TrimSpace(text) == "" {
		return nil
	}
	if strings.TrimSpace(t.BodyMD) == "" {
		t.BodyMD = text
	} else {
		t.BodyMD = strings.TrimRight(t.BodyMD, " \t\r\n") + "\n\n" + text
	}
	s.tasks[id] = t
	return nil
}

func (s *fakeStore) SetState(_ context.Context, id int64, st task.State) error {
	if !st.Valid() {
		return errors.New("unknown state")
	}
	t, ok := s.tasks[id]
	if !ok {
		return errors.New("no such task")
	}
	t.State = st
	s.tasks[id] = t
	return nil
}

func (s *fakeStore) SetTags(_ context.Context, id int64, tags []string) error {
	if _, ok := s.tasks[id]; !ok {
		return errors.New("no such task")
	}
	if len(tags) == 0 {
		delete(s.tags, id)
		return nil
	}
	s.tags[id] = tags
	return nil
}

func (s *fakeStore) TagsForTask(_ context.Context, id int64) ([]string, error) {
	return s.tags[id], nil
}

// SetProject mirrors the store: the whole sub-tree moves with the task.
func (s *fakeStore) SetProject(_ context.Context, taskID, projectID int64) error {
	t, ok := s.tasks[taskID]
	if !ok {
		return errors.New("no such task")
	}
	t.ProjectID = projectID
	s.tasks[taskID] = t
	for id, child := range s.tasks {
		if child.ParentID != nil && *child.ParentID == taskID {
			if err := s.SetProject(context.Background(), id, projectID); err != nil {
				return err
			}
		}
	}
	return nil
}

// SetParent mirrors the store: no self-parenting, no moving under a
// descendant, the parent must exist, and a parent in another project
// drags the sub-tree into that project.
func (s *fakeStore) SetParent(_ context.Context, taskID int64, parentID *int64) error {
	t, ok := s.tasks[taskID]
	if !ok {
		return errors.New("no such task")
	}
	if parentID != nil {
		if *parentID == taskID {
			return errors.New("a task cannot be its own parent")
		}
		if s.isDescendant(*parentID, taskID) {
			return errors.New("a task cannot move under its own sub-task")
		}
		parent, ok := s.tasks[*parentID]
		if !ok {
			return errors.New("no such parent task")
		}
		if parent.ProjectID != t.ProjectID {
			if err := s.SetProject(context.Background(), taskID, parent.ProjectID); err != nil {
				return err
			}
			t = s.tasks[taskID]
		}
	}
	t.ParentID = parentID
	s.tasks[taskID] = t
	return nil
}

// isDescendant reports whether id sits somewhere below ancestorID,
// walking parent links up from id.
func (s *fakeStore) isDescendant(id, ancestorID int64) bool {
	for {
		t, ok := s.tasks[id]
		if !ok || t.ParentID == nil {
			return false
		}
		if *t.ParentID == ancestorID {
			return true
		}
		id = *t.ParentID
	}
}

func (s *fakeStore) GetProject(_ context.Context, id int64) (task.Project, error) {
	for _, p := range s.projects {
		if p.ID == id {
			return p, nil
		}
	}
	return task.Project{}, task.ErrProjectNotFound
}

func (s *fakeStore) ProjectByName(_ context.Context, name string) (task.Project, error) {
	for _, p := range s.projects {
		if strings.EqualFold(p.Name, name) {
			return p, nil
		}
	}
	return task.Project{}, task.ErrProjectNotFound
}

func (s *fakeStore) ListProjects(context.Context) ([]task.Project, error) {
	return s.projects, nil
}

func (s *fakeStore) SetPriority(_ context.Context, id int64, p *int64) error {
	t, ok := s.tasks[id]
	if !ok {
		return errors.New("no such task")
	}
	t.Priority = p
	s.tasks[id] = t
	return nil
}

func (s *fakeStore) SetDue(_ context.Context, id int64, due *string) error {
	t, ok := s.tasks[id]
	if !ok {
		return errors.New("no such task")
	}
	t.Due = due
	s.tasks[id] = t
	return nil
}

func (s *fakeStore) GetStepRun(_ context.Context, id int64) (workflow.StepRun, error) {
	sr, ok := s.stepRuns[id]
	if !ok {
		return workflow.StepRun{}, workflow.ErrStepRunNotFound
	}
	return sr, nil
}

func (s *fakeStore) GetStep(_ context.Context, id int64) (workflow.Step, error) {
	st, ok := s.steps[id]
	if !ok {
		return workflow.Step{}, workflow.ErrStepNotFound
	}
	return st, nil
}

func (s *fakeStore) GetWorkflow(_ context.Context, id int64) (workflow.Workflow, error) {
	w, ok := s.workflows[id]
	if !ok {
		return workflow.Workflow{}, workflow.ErrWorkflowNotFound
	}
	return w, nil
}

func (s *fakeStore) OutgoingEdges(_ context.Context, stepID int64) ([]workflow.Edge, error) {
	var out []workflow.Edge
	for _, e := range s.edges {
		if e.FromStepID == stepID {
			out = append(out, e)
		}
	}
	return out, nil
}

// FinishStepRun mirrors the store: the outcome is normalized and the
// hand-off is one-shot.
func (s *fakeStore) FinishStepRun(_ context.Context, id int64, outcome, deliverable string) error {
	o, err := workflow.NormalizeOutcome(outcome)
	if err != nil {
		return err
	}
	sr, ok := s.stepRuns[id]
	if !ok {
		return workflow.ErrStepRunNotFound
	}
	if sr.Finished() {
		return workflow.ErrStepRunFinished
	}
	now := time.Now()
	sr.Outcome, sr.Deliverable, sr.EndedAt = o, deliverable, &now
	s.stepRuns[id] = sr
	return nil
}

// ---- workflow authoring ----------------------------------------------------
//
// The fake mirrors internal/store's workflow semantics closely enough for
// the authoring tools: normalized names, case-insensitive uniqueness,
// append-at-end steps, the (from, outcome) upsert on edges, and the same
// ordering the SQL gives ListSteps and ListEdges.

// allocID hands out an id above every workflow, step and edge the fake
// holds, so ids the tools allocate never collide with the ones a seed
// helper (seedStepRun) sets directly.
func (s *fakeStore) allocID() int64 {
	var top int64
	for id := range s.workflows {
		top = max(top, id)
	}
	for id := range s.steps {
		top = max(top, id)
	}
	for _, e := range s.edges {
		top = max(top, e.ID)
	}
	return top + 1
}

// workflowNameTaken mirrors the schema's NOCASE unique index on name,
// ignoring the workflow with id except (0 for none).
func (s *fakeStore) workflowNameTaken(name string, except int64) bool {
	for _, w := range s.workflows {
		if w.ID != except && strings.EqualFold(w.Name, name) {
			return true
		}
	}
	return false
}

func (s *fakeStore) ListWorkflows(context.Context) ([]workflow.Workflow, error) {
	out := make([]workflow.Workflow, 0, len(s.workflows))
	for _, w := range s.workflows {
		w.StepCount = 0
		for _, st := range s.steps {
			if st.WorkflowID == w.ID {
				w.StepCount++
			}
		}
		out = append(out, w)
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out, nil
}

func (s *fakeStore) WorkflowByName(_ context.Context, name string) (workflow.Workflow, error) {
	n, err := workflow.NormalizeName(name)
	if err != nil {
		return workflow.Workflow{}, err
	}
	for _, w := range s.workflows {
		if strings.EqualFold(w.Name, n) {
			return w, nil
		}
	}
	return workflow.Workflow{}, fmt.Errorf("workflow %q: %w", n, workflow.ErrWorkflowNotFound)
}

func (s *fakeStore) CreateWorkflow(_ context.Context, name, description string) (workflow.Workflow, error) {
	n, err := workflow.NormalizeName(name)
	if err != nil {
		return workflow.Workflow{}, err
	}
	if s.workflowNameTaken(n, 0) {
		return workflow.Workflow{}, fmt.Errorf("creating workflow %q: UNIQUE constraint failed: workflows.name", n)
	}
	w := workflow.Workflow{ID: s.allocID(), Name: n, Description: description}
	s.workflows[w.ID] = w
	return w, nil
}

func (s *fakeStore) RenameWorkflow(_ context.Context, id int64, name string) error {
	n, err := workflow.NormalizeName(name)
	if err != nil {
		return err
	}
	w, ok := s.workflows[id]
	if !ok {
		return fmt.Errorf("workflow %d: %w", id, workflow.ErrWorkflowNotFound)
	}
	if s.workflowNameTaken(n, id) {
		return fmt.Errorf("renaming workflow %d: UNIQUE constraint failed: workflows.name", id)
	}
	w.Name = n
	s.workflows[id] = w
	return nil
}

func (s *fakeStore) SetWorkflowDescription(_ context.Context, id int64, description string) error {
	w, ok := s.workflows[id]
	if !ok {
		return fmt.Errorf("workflow %d: %w", id, workflow.ErrWorkflowNotFound)
	}
	w.Description = description
	s.workflows[id] = w
	return nil
}

// ListSteps returns the workflow's steps by sort order then id, as the
// SQL does.
func (s *fakeStore) ListSteps(_ context.Context, workflowID int64) ([]workflow.Step, error) {
	var out []workflow.Step
	for _, st := range s.steps {
		if st.WorkflowID == workflowID {
			out = append(out, st)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].SortOrder != out[j].SortOrder {
			return out[i].SortOrder < out[j].SortOrder
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func (s *fakeStore) AddStep(_ context.Context, workflowID int64, name string, kind workflow.StepKind) (workflow.Step, error) {
	n, err := workflow.NormalizeName(name)
	if err != nil {
		return workflow.Step{}, err
	}
	if !kind.Valid() {
		return workflow.Step{}, fmt.Errorf("unknown step kind %q", kind)
	}
	if _, ok := s.workflows[workflowID]; !ok {
		return workflow.Step{}, fmt.Errorf("workflow %d: %w", workflowID, workflow.ErrWorkflowNotFound)
	}
	var order int64
	for _, st := range s.steps {
		if st.WorkflowID == workflowID {
			order = max(order, st.SortOrder+1)
		}
	}
	st := workflow.Step{ID: s.allocID(), WorkflowID: workflowID, Name: n, Kind: kind, SortOrder: order}
	s.steps[st.ID] = st
	return st, nil
}

func (s *fakeStore) UpdateStep(_ context.Context, st workflow.Step) error {
	n, err := workflow.NormalizeName(st.Name)
	if err != nil {
		return err
	}
	if !st.Kind.Valid() {
		return fmt.Errorf("unknown step kind %q", st.Kind)
	}
	cur, ok := s.steps[st.ID]
	if !ok {
		return fmt.Errorf("step %d: %w", st.ID, workflow.ErrStepNotFound)
	}
	cur.Name, cur.Kind, cur.PromptMD, cur.Model, cur.PermissionMode = n, st.Kind, st.PromptMD, st.Model, st.PermissionMode
	s.steps[st.ID] = cur
	return nil
}

func (s *fakeStore) SetStepPrompt(_ context.Context, id int64, prompt string) error {
	st, ok := s.steps[id]
	if !ok {
		return fmt.Errorf("step %d: %w", id, workflow.ErrStepNotFound)
	}
	st.PromptMD = prompt
	s.steps[id] = st
	return nil
}

// ReorderSteps refuses anything but exactly the workflow's step ids, the
// same way the store does, and applies nothing on refusal.
func (s *fakeStore) ReorderSteps(ctx context.Context, workflowID int64, ids []int64) error {
	current, _ := s.ListSteps(ctx, workflowID)
	if len(current) != len(ids) {
		return fmt.Errorf("reordering workflow %d: got %d ids for %d steps", workflowID, len(ids), len(current))
	}
	known := make(map[int64]bool, len(current))
	for _, st := range current {
		known[st.ID] = true
	}
	for _, id := range ids {
		if !known[id] {
			return fmt.Errorf("reordering workflow %d: step %d is not in it", workflowID, id)
		}
		delete(known, id)
	}
	for i, id := range ids {
		st := s.steps[id]
		st.SortOrder = int64(i)
		s.steps[id] = st
	}
	return nil
}

// DeleteStep removes the step and every edge touching it; the fake has
// no runs, so the store's active-run check has nothing to refuse.
func (s *fakeStore) DeleteStep(_ context.Context, id int64) error {
	if _, ok := s.steps[id]; !ok {
		return fmt.Errorf("step %d: %w", id, workflow.ErrStepNotFound)
	}
	delete(s.steps, id)
	kept := s.edges[:0]
	for _, e := range s.edges {
		if e.FromStepID != id && e.ToStepID != id {
			kept = append(kept, e)
		}
	}
	s.edges = kept
	return nil
}

// ListEdges returns the edges leaving the workflow's steps, by the source
// step's sort order then outcome, as the SQL does.
func (s *fakeStore) ListEdges(ctx context.Context, workflowID int64) ([]workflow.Edge, error) {
	steps, _ := s.ListSteps(ctx, workflowID)
	position := make(map[int64]int, len(steps))
	for i, st := range steps {
		position[st.ID] = i
	}
	out := make([]workflow.Edge, 0, len(s.edges))
	for _, e := range s.edges {
		if _, ok := position[e.FromStepID]; ok {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if position[out[i].FromStepID] != position[out[j].FromStepID] {
			return position[out[i].FromStepID] < position[out[j].FromStepID]
		}
		return out[i].Outcome < out[j].Outcome
	})
	return out, nil
}

// SetEdge upserts on (from step, outcome), re-pointing the existing edge
// or adding one, with the store's checks in front.
func (s *fakeStore) SetEdge(ctx context.Context, fromStepID int64, outcome string, toStepID int64, maxIterations *int64) (workflow.Edge, error) {
	o, err := workflow.NormalizeOutcome(outcome)
	if err != nil {
		return workflow.Edge{}, err
	}
	if maxIterations != nil && *maxIterations < 1 {
		return workflow.Edge{}, fmt.Errorf("max iterations %d must be at least 1", *maxIterations)
	}
	from, err := s.GetStep(ctx, fromStepID)
	if err != nil {
		return workflow.Edge{}, err
	}
	to, err := s.GetStep(ctx, toStepID)
	if err != nil {
		return workflow.Edge{}, err
	}
	if from.WorkflowID != to.WorkflowID {
		return workflow.Edge{}, fmt.Errorf("edge %d -> %d: %w", fromStepID, toStepID, workflow.ErrCrossWorkflowEdge)
	}
	for i, e := range s.edges {
		if e.FromStepID == fromStepID && e.Outcome == o {
			s.edges[i].ToStepID, s.edges[i].MaxIterations = toStepID, maxIterations
			return s.edges[i], nil
		}
	}
	e := workflow.Edge{ID: s.allocID(), FromStepID: fromStepID, Outcome: o, ToStepID: toStepID, MaxIterations: maxIterations}
	s.edges = append(s.edges, e)
	return e, nil
}

func (s *fakeStore) DeleteEdge(_ context.Context, id int64) error {
	for i, e := range s.edges {
		if e.ID == id {
			s.edges = append(s.edges[:i], s.edges[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("deleting edge %d: no such edge", id)
}

func (s *fakeStore) Close() error { return nil }

// dial spins up a Server backed by store, bound to taskID and to no step
// run, and connects a client to it over an in-memory transport pair,
// returning a session ready for CallTool.
func dial(t *testing.T, store Store, taskID int64) *mcp.ClientSession {
	t.Helper()
	return dialStep(t, store, taskID, 0)
}

// dialStep is dial for a workflow step's session: a non-zero stepRunID
// adds the step tools, exactly as Server.Run does. The workflow authoring
// tools are registered for every session, also as Server.Run does.
func dialStep(t *testing.T, store Store, taskID, stepRunID int64) *mcp.ClientSession {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "tend-test"}, nil)
	registerTools(srv, store, taskID)
	registerWorkflowTools(srv, store)
	if stepRunID != 0 {
		registerStepTools(srv, store, stepRunID)
	}

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	ctx := context.Background()

	if _, err := srv.Connect(ctx, serverTransport, nil); err != nil {
		t.Fatalf("server Connect: %v", err)
	}

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client"}, nil)
	cs, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client Connect: %v", err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func callTool[Out any](t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) Out {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool(%s): %v", name, err)
	}
	if res.IsError {
		t.Fatalf("CallTool(%s) returned an error result: %+v", name, res.Content)
	}
	b, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshaling structured content: %v", err)
	}
	var out Out
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshaling into %T: %v", out, err)
	}
	return out
}

func TestGetCurrentTaskResolvesBoundTask(t *testing.T) {
	store := newFakeStore(task.Task{ID: 42, Title: "ship the thing", State: task.StateDoing})
	cs := dial(t, store, 42)

	got := callTool[taskOut](t, cs, "get_current_task", nil)
	if got.ID != 42 || got.Title != "ship the thing" || got.State != "doing" {
		t.Errorf("get_current_task = %+v, want id=42 title=%q state=doing", got, "ship the thing")
	}
}

func TestCreateSubtaskDefaultsToBoundTask(t *testing.T) {
	store := newFakeStore(task.Task{ID: 1, Title: "parent"})
	cs := dial(t, store, 1)

	got := callTool[taskOut](t, cs, "create_subtask", map[string]any{"title": "phase one"})
	if got.ParentID == nil || *got.ParentID != 1 {
		t.Errorf("create_subtask parent_id = %v, want 1 (the bound task)", got.ParentID)
	}
	if got.Title != "phase one" {
		t.Errorf("create_subtask title = %q, want %q", got.Title, "phase one")
	}
}

func TestCreateSubtaskAcceptsExplicitParentOverride(t *testing.T) {
	store := newFakeStore(
		task.Task{ID: 1, Title: "bound"},
		task.Task{ID: 2, Title: "other parent"},
	)
	cs := dial(t, store, 1)

	got := callTool[taskOut](t, cs, "create_subtask", map[string]any{"title": "x", "parent_id": 2})
	if got.ParentID == nil || *got.ParentID != 2 {
		t.Errorf("create_subtask parent_id = %v, want 2 (explicit override)", got.ParentID)
	}
}

func TestSetTaskStateDefaultsToBoundTaskAndReturnsUpdated(t *testing.T) {
	store := newFakeStore(task.Task{ID: 1, Title: "bound", State: task.StateTodo})
	cs := dial(t, store, 1)

	got := callTool[taskOut](t, cs, "set_task_state", map[string]any{"state": "doing"})
	if got.State != "doing" {
		t.Errorf("set_task_state state = %q, want doing", got.State)
	}
}

func TestSetTaskStateRejectsUnknownState(t *testing.T) {
	store := newFakeStore(task.Task{ID: 1, Title: "bound", State: task.StateTodo})
	cs := dial(t, store, 1)

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "set_task_state",
		Arguments: map[string]any{"state": "not-a-real-state"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Error("set_task_state with an invalid state should return an error result, not succeed")
	}
}

func TestSetTaskTagsEmptyListClears(t *testing.T) {
	store := newFakeStore(task.Task{ID: 1, Title: "bound"})
	store.tags[1] = []string{"tend"}
	cs := dial(t, store, 1)

	got := callTool[taskOut](t, cs, "set_task_tags", map[string]any{"tags": []string{}})
	if len(got.Tags) != 0 {
		t.Errorf("set_task_tags with an empty list should clear, got %v", got.Tags)
	}
}

func TestSetTaskTagsReplacesWholeList(t *testing.T) {
	store := newFakeStore(task.Task{ID: 1, Title: "bound"})
	store.tags[1] = []string{"old"}
	cs := dial(t, store, 1)

	got := callTool[taskOut](t, cs, "set_task_tags",
		map[string]any{"tags": []string{"alpha", "beta"}})
	if len(got.Tags) != 2 || got.Tags[0] != "alpha" || got.Tags[1] != "beta" {
		t.Errorf("Tags = %v, want [alpha beta] replacing the old list", got.Tags)
	}
}

func TestAppendTaskBodyKeepsExistingBody(t *testing.T) {
	store := newFakeStore(task.Task{ID: 1, Title: "bound", BodyMD: "## Context\nsee the spec"})
	cs := dial(t, store, 1)

	got := callTool[taskOut](t, cs, "append_task_body", map[string]any{"text": "PR: https://example.com/pr/1"})
	want := "## Context\nsee the spec\n\nPR: https://example.com/pr/1"
	if got.BodyMD != want {
		t.Errorf("append_task_body body = %q, want %q", got.BodyMD, want)
	}
}

func TestAppendTaskBodyOnEmptyBodyHasNoLeadingSeparator(t *testing.T) {
	store := newFakeStore(task.Task{ID: 1, Title: "bound"})
	cs := dial(t, store, 1)

	got := callTool[taskOut](t, cs, "append_task_body", map[string]any{"text": "first note"})
	if got.BodyMD != "first note" {
		t.Errorf("append_task_body on empty body = %q, want %q", got.BodyMD, "first note")
	}
}

func TestAppendTaskBodyAcceptsExplicitTaskOverride(t *testing.T) {
	store := newFakeStore(
		task.Task{ID: 1, Title: "bound", BodyMD: "untouched"},
		task.Task{ID: 2, Title: "other", BodyMD: "a"},
	)
	cs := dial(t, store, 1)

	got := callTool[taskOut](t, cs, "append_task_body", map[string]any{"text": "b", "task_id": 2})
	if got.ID != 2 || got.BodyMD != "a\n\nb" {
		t.Errorf("append_task_body(task_id=2) = %+v, want id=2 body %q", got, "a\n\nb")
	}
	if store.tasks[1].BodyMD != "untouched" {
		t.Errorf("bound task body mutated to %q", store.tasks[1].BodyMD)
	}
}

// TestNoLogEntryTool pins the deliberate absence of add_log_entry: log
// entries are the user's standup notes, and an agent that wants to leave
// a record on a task must go through update_task_body instead.
func TestNoLogEntryTool(t *testing.T) {
	store := newFakeStore(task.Task{ID: 7, Title: "bound"})
	cs := dial(t, store, 7)

	tools, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	for _, tool := range tools.Tools {
		if strings.Contains(tool.Name, "log") {
			t.Errorf("tool %q exposed; agents must not write log entries", tool.Name)
		}
	}
}

func TestListSubtasksDefaultsToBoundTask(t *testing.T) {
	parent := int64(1)
	store := newFakeStore(
		task.Task{ID: 1, Title: "bound"},
		task.Task{ID: 2, Title: "child", ParentID: &parent},
	)
	cs := dial(t, store, 1)

	got := callTool[subtasksOut](t, cs, "list_subtasks", nil)
	if len(got.Tasks) != 1 || got.Tasks[0].ID != 2 {
		t.Errorf("list_subtasks = %+v, want one child (id=2)", got.Tasks)
	}
}

// TestEveryOutputSchemaIsAnObject guards the whole tool surface against
// the failure that took it down once: MCP's outputSchema describes the
// structuredContent object, so a handler returning a bare slice yields a
// top-level array schema. Clients validate the entire tools/list
// response, so one such tool makes every tend tool unavailable. Dialled
// with a step run so the step tools are covered too.
func TestEveryOutputSchemaIsAnObject(t *testing.T) {
	store := newFakeStore(task.Task{ID: 1, Title: "bound"})
	sr := seedStepRun(store)
	cs := dialStep(t, store, 1, sr)

	tools, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools.Tools) == 0 {
		t.Fatal("ListTools returned no tools")
	}

	for _, tool := range tools.Tools {
		if tool.OutputSchema == nil {
			continue
		}
		b, err := json.Marshal(tool.OutputSchema)
		if err != nil {
			t.Fatalf("marshaling %s output schema: %v", tool.Name, err)
		}
		var schema struct {
			Type any `json:"type"`
		}
		if err := json.Unmarshal(b, &schema); err != nil {
			t.Fatalf("unmarshaling %s output schema: %v", tool.Name, err)
		}
		if schema.Type != "object" {
			t.Errorf("%s outputSchema type = %v, want \"object\"", tool.Name, schema.Type)
		}
	}
}

func TestCreateTaskIsNotScopedToBoundTask(t *testing.T) {
	store := newFakeStore(task.Task{ID: 1, Title: "bound"})
	cs := dial(t, store, 1)

	got := callTool[taskOut](t, cs, "create_task", map[string]any{"title": "unrelated work"})
	if got.ParentID != nil {
		t.Errorf("create_task parent_id = %v, want nil (top-level, not scoped to the bound task)", got.ParentID)
	}
}

// Gate 3's acceptance: a session bound to a task can read its project.
func TestGetCurrentProject(t *testing.T) {
	store := newFakeStore(task.Task{ID: 1, Title: "bound", ProjectID: 2})
	store.projects = append(store.projects, task.Project{ID: 2, Name: "tend", LiveCount: 3})
	cs := dial(t, store, 1)

	got := callTool[projectOut](t, cs, "get_current_project", map[string]any{})
	if got.Name != "tend" || got.ID != 2 {
		t.Errorf("get_current_project = %+v, want the bound task's project tend", got)
	}
	if got.Tasks != 3 {
		t.Errorf("live_task_count = %d, want 3", got.Tasks)
	}
}

func TestListProjects(t *testing.T) {
	store := newFakeStore(task.Task{ID: 1, Title: "bound"})
	store.projects = append(store.projects, task.Project{ID: 2, Name: "tend"})
	cs := dial(t, store, 1)

	got := callTool[projectsOut](t, cs, "list_projects", map[string]any{})
	if len(got.Projects) != 2 {
		t.Fatalf("list_projects returned %d projects, want 2", len(got.Projects))
	}
	names := map[string]bool{}
	for _, p := range got.Projects {
		names[p.Name] = true
	}
	if !names["Unsorted"] || !names["tend"] {
		t.Errorf("list_projects = %+v, want Unsorted and tend", got.Projects)
	}
}

// Moving a task takes its sub-tree along, the same guarantee the store
// makes: a child in a different project from its parent is incoherent.
func TestSetTaskProjectMovesSubtree(t *testing.T) {
	parentID := int64(1)
	store := newFakeStore(
		task.Task{ID: 1, Title: "parent", ProjectID: task.DefaultProjectID},
		task.Task{ID: 2, Title: "child", ProjectID: task.DefaultProjectID, ParentID: &parentID},
	)
	store.projects = append(store.projects, task.Project{ID: 2, Name: "tend"})
	cs := dial(t, store, 1)

	got := callTool[taskOut](t, cs, "set_task_project", map[string]any{"project": "tend"})
	if got.ProjectID != 2 {
		t.Errorf("task ProjectID = %d, want tend (2)", got.ProjectID)
	}
	if child := store.tasks[2]; child.ProjectID != 2 {
		t.Errorf("child ProjectID = %d, want tend (2): the sub-tree moves too", child.ProjectID)
	}
}

// parent_id 0 is the wire form of "top level": a pointer could not tell
// an explicit null from an omitted field over MCP.
func TestMoveTaskPromotesToTopLevel(t *testing.T) {
	parentID := int64(1)
	store := newFakeStore(
		task.Task{ID: 1, Title: "parent"},
		task.Task{ID: 2, Title: "child", ParentID: &parentID},
	)
	cs := dial(t, store, 2)

	got := callTool[taskOut](t, cs, "move_task", map[string]any{"parent_id": 0})
	if got.ID != 2 {
		t.Fatalf("move_task returned task %d, want the bound task 2", got.ID)
	}
	if got.ParentID != nil {
		t.Errorf("move_task parent_id = %v, want nil (promoted to the top level)", *got.ParentID)
	}
}

func TestMoveTaskDemotesUnderAnotherTask(t *testing.T) {
	store := newFakeStore(
		task.Task{ID: 1, Title: "bound"},
		task.Task{ID: 2, Title: "will become a sub-task"},
		task.Task{ID: 3, Title: "new parent"},
	)
	cs := dial(t, store, 1)

	got := callTool[taskOut](t, cs, "move_task", map[string]any{"task_id": 2, "parent_id": 3})
	if got.ID != 2 {
		t.Fatalf("move_task returned task %d, want the explicit override 2", got.ID)
	}
	if got.ParentID == nil || *got.ParentID != 3 {
		t.Errorf("move_task parent_id = %v, want 3", got.ParentID)
	}
	if store.tasks[1].ParentID != nil {
		t.Errorf("bound task re-parented to %v; the override must not touch it", *store.tasks[1].ParentID)
	}
}

// Moving a task under its own sub-task would orphan the whole chain
// from the tree; the store refuses, and the tool surfaces that as an
// error result rather than a success.
func TestMoveTaskRejectsCycle(t *testing.T) {
	parentID := int64(1)
	store := newFakeStore(
		task.Task{ID: 1, Title: "parent"},
		task.Task{ID: 2, Title: "child", ParentID: &parentID},
	)
	cs := dial(t, store, 1)

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "move_task",
		Arguments: map[string]any{"task_id": 1, "parent_id": 2},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Error("move_task under the task's own sub-task should return an error result, not succeed")
	}
	if got := store.tasks[1].ParentID; got != nil {
		t.Errorf("a refused move still re-parented the task to %d", *got)
	}
}

// A parent in another project pulls the moved task and its children into
// that project: a child in a different project from its parent is
// incoherent, the same guarantee set_task_project makes.
func TestMoveTaskSubtreeFollowsParentProject(t *testing.T) {
	movedID := int64(2)
	store := newFakeStore(
		task.Task{ID: 1, Title: "new parent", ProjectID: 2},
		task.Task{ID: 2, Title: "moved", ProjectID: task.DefaultProjectID},
		task.Task{ID: 3, Title: "grandchild", ProjectID: task.DefaultProjectID, ParentID: &movedID},
	)
	store.projects = append(store.projects, task.Project{ID: 2, Name: "tend"})
	cs := dial(t, store, 2)

	got := callTool[taskOut](t, cs, "move_task", map[string]any{"parent_id": 1})
	if got.ParentID == nil || *got.ParentID != 1 {
		t.Errorf("move_task parent_id = %v, want 1", got.ParentID)
	}
	if got.ProjectID != 2 {
		t.Errorf("moved task ProjectID = %d, want the parent's project (2)", got.ProjectID)
	}
	child := callTool[taskOut](t, cs, "get_task", map[string]any{"task_id": 3})
	if child.ProjectID != 2 {
		t.Errorf("grandchild ProjectID = %d, want 2: the sub-tree follows the new parent's project", child.ProjectID)
	}
}

// An agent guessing at a project name gets an error it can act on, not a
// new project.
func TestSetTaskProjectUnknownNameIsAnError(t *testing.T) {
	store := newFakeStore(task.Task{ID: 1, Title: "bound"})
	cs := dial(t, store, 1)

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "set_task_project",
		Arguments: map[string]any{"project": "nope"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Error("an unknown project name should return an error result")
	}
	if len(store.projects) != 1 {
		t.Errorf("a failed lookup created a project: %+v", store.projects)
	}
}
