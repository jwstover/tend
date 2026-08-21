package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jwstover/tend/internal/task"
)

// fakeStore is an in-memory stand-in for *store.Store, just enough of
// mcpserver.Store's method set to exercise the tool surface without a
// real SQLite file.
type fakeStore struct {
	tasks  map[int64]task.Task
	nextID int64
	logs   []task.LogEntry
}

func newFakeStore(seed ...task.Task) *fakeStore {
	s := &fakeStore{tasks: make(map[int64]task.Task)}
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

func (s *fakeStore) SetProject(_ context.Context, id int64, project *string) error {
	t, ok := s.tasks[id]
	if !ok {
		return errors.New("no such task")
	}
	t.Project = project
	s.tasks[id] = t
	return nil
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

func (s *fakeStore) AddLogEntry(_ context.Context, taskID *int64, body string) (task.LogEntry, error) {
	e := task.LogEntry{ID: int64(len(s.logs) + 1), TaskID: taskID, Body: body}
	s.logs = append(s.logs, e)
	return e, nil
}

func (s *fakeStore) Close() error { return nil }

// dial spins up a Server backed by store, bound to taskID, and connects
// a client to it over an in-memory transport pair, returning a session
// ready for CallTool.
func dial(t *testing.T, store Store, taskID int64) *mcp.ClientSession {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "tend-test"}, nil)
	registerTools(srv, store, taskID)

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

func TestSetTaskProjectEmptyStringClears(t *testing.T) {
	proj := "tend"
	store := newFakeStore(task.Task{ID: 1, Title: "bound", Project: &proj})
	cs := dial(t, store, 1)

	got := callTool[taskOut](t, cs, "set_task_project", map[string]any{"project": ""})
	if got.Project != nil {
		t.Errorf("set_task_project with empty string should clear, got %v", *got.Project)
	}
}

func TestAddLogEntryDefaultsToBoundTask(t *testing.T) {
	store := newFakeStore(task.Task{ID: 7, Title: "bound"})
	cs := dial(t, store, 7)

	got := callTool[logOut](t, cs, "add_log_entry", map[string]any{"body": "made progress"})
	if got.TaskID == nil || *got.TaskID != 7 {
		t.Errorf("add_log_entry task_id = %v, want 7 (the bound task)", got.TaskID)
	}
	if got.Body != "made progress" {
		t.Errorf("add_log_entry body = %q, want %q", got.Body, "made progress")
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
// response, so one such tool makes every tend tool unavailable.
func TestEveryOutputSchemaIsAnObject(t *testing.T) {
	cs := dial(t, newFakeStore(task.Task{ID: 1, Title: "bound"}), 1)

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
