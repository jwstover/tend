package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/jwstover/tend/internal/task"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "tend.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// mustAdd captures a task or fails the test, for the many session tests
// that need a parent row and don't care how it got there.
func mustAdd(t *testing.T, s *Store, title string) task.Task {
	t.Helper()
	created, err := s.AddTask(context.Background(), title)
	if err != nil {
		t.Fatalf("AddTask(%q): %v", title, err)
	}
	return created
}

func TestAddTaskDefaults(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	got, err := s.AddTask(ctx, "  buy milk ")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	if got.ID == 0 {
		t.Error("expected a non-zero id")
	}
	if got.Title != "buy milk" {
		t.Errorf("Title = %q, want %q (normalized)", got.Title, "buy milk")
	}
	if got.State != task.StateInbox {
		t.Errorf("State = %q, want %q", got.State, task.StateInbox)
	}
	if got.BodyMD != "" {
		t.Errorf("BodyMD = %q, want empty", got.BodyMD)
	}
	if got.Project != nil || got.Priority != nil || got.Due != nil || got.SnoozeUntil != nil || got.ParentID != nil || got.CompletedAt != nil {
		t.Error("expected all optional fields to be nil on capture")
	}
	if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Error("expected timestamps to be set")
	}
}

func TestAddTaskWithBody(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	got, err := s.AddTaskWithBody(ctx, " PROJ-1: fix it ", "https://example.atlassian.net/browse/PROJ-1\n")
	if err != nil {
		t.Fatalf("AddTaskWithBody: %v", err)
	}
	if got.Title != "PROJ-1: fix it" {
		t.Errorf("Title = %q, want normalized title", got.Title)
	}
	if got.BodyMD != "https://example.atlassian.net/browse/PROJ-1\n" {
		t.Errorf("BodyMD = %q, want the link", got.BodyMD)
	}
	if got.State != task.StateInbox {
		t.Errorf("State = %q, want %q", got.State, task.StateInbox)
	}

	if _, err := s.AddTaskWithBody(ctx, "   ", "body"); !errors.Is(err, task.ErrEmptyTitle) {
		t.Errorf("blank title error = %v, want ErrEmptyTitle", err)
	}
}

func TestAddTaskRejectsBlankTitle(t *testing.T) {
	s := newTestStore(t)
	_, err := s.AddTask(context.Background(), "   ")
	if !errors.Is(err, task.ErrEmptyTitle) {
		t.Fatalf("AddTask error = %v, want ErrEmptyTitle", err)
	}
}

func TestListLiveFiltering(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	live, err := s.AddTask(ctx, "live one")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}

	// Drive rows into non-live conditions with raw SQL: the state and
	// snooze mutators arrive in Gate 3, but the live-view query they
	// feed must filter correctly from day one.
	future := time.Now().AddDate(0, 0, 7).Format("2006-01-02")
	past := time.Now().AddDate(0, 0, -7).Format("2006-01-02")
	fixtures := []struct {
		title  string
		column string
		value  string
		live   bool
	}{
		{"done task", "state", "done", false},
		{"someday task", "state", "someday", false},
		{"snoozed future", "snooze_until", future, false},
		{"snoozed past", "snooze_until", past, true},
	}
	wantTitles := map[string]bool{live.Title: true}
	for _, f := range fixtures {
		created, err := s.AddTask(ctx, f.title)
		if err != nil {
			t.Fatalf("AddTask(%q): %v", f.title, err)
		}
		if _, err := s.db.ExecContext(ctx,
			"UPDATE tasks SET "+f.column+" = ? WHERE id = ?", f.value, created.ID,
		); err != nil {
			t.Fatalf("fixture update for %q: %v", f.title, err)
		}
		if f.live {
			wantTitles[f.title] = true
		}
	}

	got, err := s.ListLive(ctx)
	if err != nil {
		t.Fatalf("ListLive: %v", err)
	}
	gotTitles := map[string]bool{}
	for _, tk := range got {
		gotTitles[tk.Title] = true
	}
	if len(gotTitles) != len(wantTitles) {
		t.Errorf("ListLive returned %v, want titles %v", gotTitles, wantTitles)
	}
	for title := range wantTitles {
		if !gotTitles[title] {
			t.Errorf("ListLive missing %q", title)
		}
	}
}

func TestTriageMutators(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	created, err := s.AddTask(ctx, "needs triage")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}

	inbox, err := s.ListInbox(ctx)
	if err != nil {
		t.Fatalf("ListInbox: %v", err)
	}
	if len(inbox) != 1 || inbox[0].ID != created.ID {
		t.Fatalf("ListInbox = %+v, want the one captured task", inbox)
	}

	proj := "home"
	if err := s.SetProject(ctx, created.ID, &proj); err != nil {
		t.Fatalf("SetProject: %v", err)
	}
	if err := s.SetDue(ctx, created.ID, ptr("2026-12-01")); err != nil {
		t.Fatalf("SetDue: %v", err)
	}
	if err := s.SetDue(ctx, created.ID, ptr("not a date")); err == nil {
		t.Error("SetDue with invalid date should fail")
	}
	if err := s.SetBody(ctx, created.ID, "## context\nhttps://example.com"); err != nil {
		t.Fatalf("SetBody: %v", err)
	}
	if err := s.SetState(ctx, created.ID, task.StateDone); err != nil {
		t.Fatalf("SetState(done): %v", err)
	}
	if err := s.SetState(ctx, created.ID, "bogus"); err == nil {
		t.Error("SetState with unknown state should fail")
	}

	got, err := s.GetTask(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.Project == nil || *got.Project != "home" {
		t.Errorf("Project = %v, want home", got.Project)
	}
	if got.Due == nil || *got.Due != "2026-12-01" {
		t.Errorf("Due = %v, want 2026-12-01", got.Due)
	}
	if got.BodyMD == "" {
		t.Error("BodyMD not saved")
	}
	if got.State != task.StateDone {
		t.Errorf("State = %s, want done", got.State)
	}
	if got.CompletedAt == nil {
		t.Error("CompletedAt not stamped on done")
	}
	if !got.UpdatedAt.After(created.UpdatedAt) && got.UpdatedAt.Equal(created.UpdatedAt) {
		// updated_at has second resolution; equal is acceptable, going
		// backwards is not.
		if got.UpdatedAt.Before(created.UpdatedAt) {
			t.Error("UpdatedAt went backwards")
		}
	}

	// Leaving done clears the completion stamp.
	if err := s.SetState(ctx, created.ID, task.StateTodo); err != nil {
		t.Fatalf("SetState(todo): %v", err)
	}
	got, err = s.GetTask(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.CompletedAt != nil {
		t.Error("CompletedAt should clear when leaving done")
	}

	// Clearing project/due with nil.
	if err := s.SetProject(ctx, created.ID, nil); err != nil {
		t.Fatalf("SetProject(nil): %v", err)
	}
	if err := s.SetDue(ctx, created.ID, nil); err != nil {
		t.Fatalf("SetDue(nil): %v", err)
	}
	got, _ = s.GetTask(ctx, created.ID)
	if got.Project != nil || got.Due != nil {
		t.Errorf("Project/Due not cleared: %v %v", got.Project, got.Due)
	}
}

func TestSetPriority(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	created, err := s.AddTask(ctx, "rank me")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}

	one := int64(1)
	if err := s.SetPriority(ctx, created.ID, &one); err != nil {
		t.Fatalf("SetPriority(1): %v", err)
	}
	got, err := s.GetTask(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.Priority == nil || *got.Priority != 1 {
		t.Errorf("Priority = %v, want 1", got.Priority)
	}

	for _, bad := range []int64{0, 5, -1} {
		if err := s.SetPriority(ctx, created.ID, &bad); err == nil {
			t.Errorf("SetPriority(%d) should fail", bad)
		}
	}

	if err := s.SetPriority(ctx, created.ID, nil); err != nil {
		t.Fatalf("SetPriority(nil): %v", err)
	}
	got, _ = s.GetTask(ctx, created.ID)
	if got.Priority != nil {
		t.Errorf("Priority not cleared: %v", got.Priority)
	}
}

func TestListLiveOrdersByPriorityWithinState(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Captured in an order that would sort wrong by id alone.
	fixtures := []struct {
		title    string
		priority *int64
	}{
		{"no priority", nil},
		{"priority C", ptrInt64(3)},
		{"priority A", ptrInt64(1)},
	}
	for _, f := range fixtures {
		created, err := s.AddTask(ctx, f.title)
		if err != nil {
			t.Fatalf("AddTask(%q): %v", f.title, err)
		}
		if err := s.SetState(ctx, created.ID, task.StateTodo); err != nil {
			t.Fatalf("SetState: %v", err)
		}
		if f.priority != nil {
			if err := s.SetPriority(ctx, created.ID, f.priority); err != nil {
				t.Fatalf("SetPriority: %v", err)
			}
		}
	}

	got, err := s.ListLive(ctx)
	if err != nil {
		t.Fatalf("ListLive: %v", err)
	}
	want := []string{"priority A", "priority C", "no priority"}
	if len(got) != len(want) {
		t.Fatalf("ListLive returned %d tasks, want %d", len(got), len(want))
	}
	for i, title := range want {
		if got[i].Title != title {
			t.Errorf("ListLive[%d] = %q, want %q", i, got[i].Title, title)
		}
	}
}

func TestSubTasks(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	parent, err := s.AddTask(ctx, "parent")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	c1, err := s.AddChild(ctx, parent.ID, "child one")
	if err != nil {
		t.Fatalf("AddChild: %v", err)
	}
	if c1.ParentID == nil || *c1.ParentID != parent.ID {
		t.Fatalf("child ParentID = %v, want %d", c1.ParentID, parent.ID)
	}
	if _, err := s.AddChild(ctx, parent.ID, "child two"); err != nil {
		t.Fatalf("AddChild: %v", err)
	}

	kids, err := s.ListChildren(ctx, parent.ID)
	if err != nil {
		t.Fatalf("ListChildren: %v", err)
	}
	if len(kids) != 2 {
		t.Fatalf("ListChildren returned %d tasks, want 2", len(kids))
	}
}

func TestChildCounts(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	parent, err := s.AddTask(ctx, "parent")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	c1, err := s.AddChild(ctx, parent.ID, "child one")
	if err != nil {
		t.Fatalf("AddChild: %v", err)
	}
	if _, err := s.AddChild(ctx, parent.ID, "child two"); err != nil {
		t.Fatalf("AddChild: %v", err)
	}
	if err := s.SetState(ctx, c1.ID, task.StateDone); err != nil {
		t.Fatalf("SetState: %v", err)
	}
	// A childless task must not appear in the map.
	if _, err := s.AddTask(ctx, "loner"); err != nil {
		t.Fatalf("AddTask: %v", err)
	}

	counts, err := s.ChildCounts(ctx)
	if err != nil {
		t.Fatalf("ChildCounts: %v", err)
	}
	if len(counts) != 1 {
		t.Fatalf("ChildCounts returned %d entries, want 1: %v", len(counts), counts)
	}
	if got := counts[parent.ID]; got.Done != 1 || got.Total != 2 {
		t.Errorf("ChildCounts[%d] = %+v, want {Done:1 Total:2}", parent.ID, got)
	}
}

func TestCountInbox(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if n, err := s.CountInbox(ctx); err != nil || n != 0 {
		t.Fatalf("CountInbox on empty store = %d, %v; want 0, nil", n, err)
	}
	captured, err := s.AddTask(ctx, "one")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	if _, err := s.AddTask(ctx, "two"); err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	if err := s.SetState(ctx, captured.ID, task.StateTodo); err != nil {
		t.Fatalf("SetState: %v", err)
	}

	if n, err := s.CountInbox(ctx); err != nil || n != 1 {
		t.Errorf("CountInbox = %d, %v; want 1, nil", n, err)
	}
}

func ptr(s string) *string { return &s }

func ptrInt64(n int64) *int64 { return &n }

func TestOpenIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tend.db")
	ctx := context.Background()

	s1, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if _, err := s1.AddTask(ctx, "persists"); err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	s1.Close()

	// Reopening must rerun migrations as a no-op and see existing data.
	s2, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer s2.Close()
	got, err := s2.ListLive(ctx)
	if err != nil {
		t.Fatalf("ListLive: %v", err)
	}
	if len(got) != 1 || got[0].Title != "persists" {
		t.Fatalf("ListLive after reopen = %+v, want the one persisted task", got)
	}
}

// eventsSince is a test helper: all events from the epoch to well past now.
func eventsSince(t *testing.T, s *Store) []task.Event {
	t.Helper()
	events, err := s.ListEvents(context.Background(),
		time.Unix(0, 0), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	return events
}

func TestEventTriggers(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	created, err := s.AddTask(ctx, "log me")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}

	events := eventsSince(t, s)
	if len(events) != 1 {
		t.Fatalf("after AddTask got %d events, want 1: %+v", len(events), events)
	}
	ev := events[0]
	if ev.Kind != task.EventCreated || ev.TaskID != created.ID || ev.TaskTitle != "log me" {
		t.Errorf("created event = %+v", ev)
	}
	if ev.New == nil || *ev.New != string(task.StateInbox) {
		t.Errorf("created event New = %v, want inbox", ev.New)
	}

	if err := s.SetState(ctx, created.ID, task.StateDoing); err != nil {
		t.Fatalf("SetState: %v", err)
	}
	events = eventsSince(t, s)
	if len(events) != 2 {
		t.Fatalf("after SetState got %d events, want 2: %+v", len(events), events)
	}
	ev = events[1]
	if ev.Kind != task.EventState {
		t.Errorf("Kind = %q, want state", ev.Kind)
	}
	if ev.Old == nil || *ev.Old != string(task.StateInbox) || ev.New == nil || *ev.New != string(task.StateDoing) {
		t.Errorf("state event = old %v new %v, want inbox -> doing", ev.Old, ev.New)
	}

	// Re-setting the same state must not write a no-op event.
	if err := s.SetState(ctx, created.ID, task.StateDoing); err != nil {
		t.Fatalf("SetState (no-op): %v", err)
	}
	if events = eventsSince(t, s); len(events) != 2 {
		t.Fatalf("no-op SetState wrote an event: %+v", events)
	}

	// Metadata changes are deliberately not logged.
	if err := s.SetProject(ctx, created.ID, ptr("work")); err != nil {
		t.Fatalf("SetProject: %v", err)
	}
	if err := s.SetPriority(ctx, created.ID, ptrInt64(1)); err != nil {
		t.Fatalf("SetPriority: %v", err)
	}
	if events = eventsSince(t, s); len(events) != 2 {
		t.Fatalf("metadata change wrote an event: %+v", events)
	}
}

func TestEventTriggerOnCascadeDelete(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	parent, err := s.AddTask(ctx, "parent")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	child, err := s.AddChild(ctx, parent.ID, "child")
	if err != nil {
		t.Fatalf("AddChild: %v", err)
	}

	if err := s.DeleteTask(ctx, parent.ID); err != nil {
		t.Fatalf("DeleteTask: %v", err)
	}

	deleted := make(map[int64]task.Event)
	for _, ev := range eventsSince(t, s) {
		if ev.Kind == task.EventDeleted {
			deleted[ev.TaskID] = ev
		}
	}
	if len(deleted) != 2 {
		t.Fatalf("got %d deleted events, want 2 (parent + cascaded child): %v", len(deleted), deleted)
	}
	if ev, ok := deleted[parent.ID]; !ok || ev.TaskTitle != "parent" {
		t.Errorf("parent deleted event = %+v", ev)
	}
	if ev, ok := deleted[child.ID]; !ok || ev.TaskTitle != "child" {
		t.Errorf("child deleted event = %+v", ev)
	}
}

func TestListEventsWindow(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if _, err := s.AddTask(ctx, "in window"); err != nil {
		t.Fatalf("AddTask: %v", err)
	}

	now := time.Now()
	got, err := s.ListEvents(ctx, now.Add(-time.Minute), now.Add(time.Minute))
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("events in window = %d, want 1", len(got))
	}
	if got[0].CreatedAt.IsZero() {
		t.Error("expected CreatedAt to be parsed")
	}

	// A window entirely in the past sees nothing.
	got, err = s.ListEvents(ctx, now.Add(-2*time.Hour), now.Add(-time.Hour))
	if err != nil {
		t.Fatalf("ListEvents (past): %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("events in past window = %+v, want none", got)
	}
}

func TestLogEntries(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if _, err := s.AddLogEntry(ctx, nil, "   "); !errors.Is(err, task.ErrEmptyNote) {
		t.Fatalf("AddLogEntry(blank) error = %v, want ErrEmptyNote", err)
	}

	free, err := s.AddLogEntry(ctx, nil, "  freestanding note ")
	if err != nil {
		t.Fatalf("AddLogEntry: %v", err)
	}
	if free.Body != "freestanding note" || free.TaskID != nil {
		t.Errorf("freestanding entry = %+v, want trimmed body and nil TaskID", free)
	}

	attached, err := s.AddLogEntry(ctx, ptrInt64(42), "attached note")
	if err != nil {
		t.Fatalf("AddLogEntry (attached): %v", err)
	}
	if attached.TaskID == nil || *attached.TaskID != 42 {
		t.Errorf("attached entry TaskID = %v, want 42", attached.TaskID)
	}

	now := time.Now()
	got, err := s.ListLogEntries(ctx, now.Add(-time.Minute), now.Add(time.Minute))
	if err != nil {
		t.Fatalf("ListLogEntries: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("entries in window = %d, want 2", len(got))
	}

	past, err := s.ListLogEntries(ctx, now.Add(-2*time.Hour), now.Add(-time.Hour))
	if err != nil {
		t.Fatalf("ListLogEntries (past): %v", err)
	}
	if len(past) != 0 {
		t.Fatalf("entries in past window = %+v, want none", past)
	}
}

func TestListTaskLog(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if _, err := s.AddLogEntry(ctx, ptrInt64(1), "first on #1"); err != nil {
		t.Fatalf("AddLogEntry: %v", err)
	}
	if _, err := s.AddLogEntry(ctx, ptrInt64(2), "on another task"); err != nil {
		t.Fatalf("AddLogEntry: %v", err)
	}
	if _, err := s.AddLogEntry(ctx, nil, "freestanding"); err != nil {
		t.Fatalf("AddLogEntry: %v", err)
	}
	if _, err := s.AddLogEntry(ctx, ptrInt64(1), "second on #1"); err != nil {
		t.Fatalf("AddLogEntry: %v", err)
	}

	got, err := s.ListTaskLog(ctx, 1)
	if err != nil {
		t.Fatalf("ListTaskLog: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ListTaskLog = %+v, want the two #1 notes only", got)
	}
	// Newest first: the detail pane leads with the latest note.
	if got[0].Body != "second on #1" || got[1].Body != "first on #1" {
		t.Errorf("ListTaskLog order = [%q, %q], want newest first", got[0].Body, got[1].Body)
	}
}

func TestListLogEntriesJoinsTaskTitle(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	created, err := s.AddTask(ctx, "ship it")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	doomed, err := s.AddTask(ctx, "doomed task")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	if _, err := s.AddLogEntry(ctx, &created.ID, "attached"); err != nil {
		t.Fatalf("AddLogEntry: %v", err)
	}
	if _, err := s.AddLogEntry(ctx, &doomed.ID, "orphaned"); err != nil {
		t.Fatalf("AddLogEntry: %v", err)
	}
	if _, err := s.AddLogEntry(ctx, nil, "freestanding"); err != nil {
		t.Fatalf("AddLogEntry: %v", err)
	}
	if err := s.DeleteTask(ctx, doomed.ID); err != nil {
		t.Fatalf("DeleteTask: %v", err)
	}

	now := time.Now()
	got, err := s.ListLogEntries(ctx, now.Add(-time.Minute), now.Add(time.Minute))
	if err != nil {
		t.Fatalf("ListLogEntries: %v", err)
	}
	titles := make(map[string]string, len(got))
	for _, n := range got {
		titles[n.Body] = n.TaskTitle
	}
	if titles["attached"] != "ship it" {
		t.Errorf("attached note title = %q, want joined task title", titles["attached"])
	}
	if titles["orphaned"] != "" {
		t.Errorf("orphaned note title = %q, want empty after task delete", titles["orphaned"])
	}
	if titles["freestanding"] != "" {
		t.Errorf("freestanding note title = %q, want empty", titles["freestanding"])
	}
}

func TestAgentSessions(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	parent, err := s.AddTask(ctx, "fix the bug")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}

	if empty, err := s.ListSessionsForTask(ctx, parent.ID); err != nil || len(empty) != 0 {
		t.Fatalf("ListSessionsForTask before any sessions = %+v, %v, want empty", empty, err)
	}

	first, err := s.CreateSession(ctx, parent.ID, "ext-1", "/tmp/work", parent.Title, "")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if first.TaskID != parent.ID || first.ExternalID != "ext-1" || first.Cwd != "/tmp/work" || first.Label != parent.Title {
		t.Errorf("CreateSession result = %+v, want it to echo the inputs", first)
	}

	second, err := s.CreateSession(ctx, parent.ID, "ext-2", "/tmp/other", parent.Title, "")
	if err != nil {
		t.Fatalf("CreateSession (second): %v", err)
	}

	got, err := s.ListSessionsForTask(ctx, parent.ID)
	if err != nil {
		t.Fatalf("ListSessionsForTask: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("sessions for task = %d, want 2", len(got))
	}
	// Newest (most recently active) first; both were just created in the
	// same instant, so id descending is the tiebreak.
	if got[0].ID != second.ID || got[1].ID != first.ID {
		t.Errorf("ListSessionsForTask order = %+v, want newest first", got)
	}

	if err := s.TouchSession(ctx, first.ID); err != nil {
		t.Fatalf("TouchSession: %v", err)
	}
}

func TestUpdateSessionLabel(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	parent, err := s.AddTask(ctx, "fix the bug")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	if _, err := s.CreateSession(ctx, parent.ID, "ext-1", "/tmp/work", parent.Title, ""); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	if err := s.UpdateSessionLabel(ctx, "ext-1", "fixed the flaky test"); err != nil {
		t.Fatalf("UpdateSessionLabel: %v", err)
	}

	got, err := s.ListSessionsForTask(ctx, parent.ID)
	if err != nil {
		t.Fatalf("ListSessionsForTask: %v", err)
	}
	if len(got) != 1 || got[0].Label != "fixed the flaky test" {
		t.Errorf("sessions = %+v, want label updated to %q", got, "fixed the flaky test")
	}
}

func TestAgentSessionsCascadeDeleteWithTask(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	parent, err := s.AddTask(ctx, "parent")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	if _, err := s.CreateSession(ctx, parent.ID, "ext-1", "/tmp/work", parent.Title, ""); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	if err := s.DeleteTask(ctx, parent.ID); err != nil {
		t.Fatalf("DeleteTask: %v", err)
	}

	got, err := s.ListSessionsForTask(ctx, parent.ID)
	if err != nil {
		t.Fatalf("ListSessionsForTask after delete: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("sessions survived task delete: %+v, want cascaded away", got)
	}
}

func TestCreateSessionStoresTmuxName(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	parent, err := s.AddTask(ctx, "fix the bug")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}

	sess, err := s.CreateSession(ctx, parent.ID, "ext-1", "/tmp/work", parent.Title, "tend-ext-1")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if sess.TmuxSession != "tend-ext-1" {
		t.Errorf("TmuxSession = %q, want tend-ext-1", sess.TmuxSession)
	}
	if sess.NeedsRecap {
		t.Error("NeedsRecap = true on a fresh session, want false")
	}

	got, err := s.ListSessionsForTask(ctx, parent.ID)
	if err != nil {
		t.Fatalf("ListSessionsForTask: %v", err)
	}
	if len(got) != 1 || got[0].TmuxSession != "tend-ext-1" {
		t.Errorf("sessions = %+v, want the tmux name to survive a round trip", got)
	}
}

// A session launched without tmux stores an empty name, which reads as
// "not attachable" — the same answer has-session would give.
func TestCreateSessionWithoutTmux(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	parent, err := s.AddTask(ctx, "fix the bug")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	sess, err := s.CreateSession(ctx, parent.ID, "ext-1", "/tmp/work", parent.Title, "")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if sess.TmuxSession != "" {
		t.Errorf("TmuxSession = %q, want empty", sess.TmuxSession)
	}
}

func TestSetSessionNeedsRecap(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	parent, err := s.AddTask(ctx, "fix the bug")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	if _, err := s.CreateSession(ctx, parent.ID, "ext-1", "/tmp/work", parent.Title, "tend-ext-1"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	if err := s.SetSessionNeedsRecap(ctx, "ext-1", true); err != nil {
		t.Fatalf("SetSessionNeedsRecap(true): %v", err)
	}
	got, err := s.ListSessionsForTask(ctx, parent.ID)
	if err != nil {
		t.Fatalf("ListSessionsForTask: %v", err)
	}
	if len(got) != 1 || !got[0].NeedsRecap {
		t.Fatalf("sessions = %+v, want NeedsRecap true", got)
	}

	// Phase 4.2 clears it; make sure the round trip works both ways now.
	if err := s.SetSessionNeedsRecap(ctx, "ext-1", false); err != nil {
		t.Fatalf("SetSessionNeedsRecap(false): %v", err)
	}
	got, err = s.ListSessionsForTask(ctx, parent.ID)
	if err != nil {
		t.Fatalf("ListSessionsForTask: %v", err)
	}
	if len(got) != 1 || got[0].NeedsRecap {
		t.Fatalf("sessions = %+v, want NeedsRecap cleared", got)
	}
}

// Status starts at the honest default rather than guessing: a session
// tend has never heard from is unknown, not idle.
func TestSessionStatusDefaultsToUnknown(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	parent := mustAdd(t, s, "do the thing")

	sess, err := s.CreateSession(ctx, parent.ID, "ext-1", "/tmp/work", parent.Title, "tend-ext-1")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if sess.Status != task.SessionUnknown {
		t.Errorf("Status = %q, want %q", sess.Status, task.SessionUnknown)
	}
	if !sess.StatusUpdatedAt.IsZero() {
		t.Errorf("StatusUpdatedAt = %v, want zero — nothing has been observed yet", sess.StatusUpdatedAt)
	}
}

func TestSetSessionStatusRoundTrips(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	parent := mustAdd(t, s, "do the thing")
	if _, err := s.CreateSession(ctx, parent.ID, "ext-1", "/tmp/work", parent.Title, "tend-ext-1"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	if err := s.SetSessionStatus(ctx, "ext-1", task.SessionBlocked); err != nil {
		t.Fatalf("SetSessionStatus: %v", err)
	}
	sessions, err := s.ListSessionsForTask(ctx, parent.ID)
	if err != nil {
		t.Fatalf("ListSessionsForTask: %v", err)
	}
	if sessions[0].Status != task.SessionBlocked {
		t.Errorf("Status = %q, want %q", sessions[0].Status, task.SessionBlocked)
	}
	if sessions[0].StatusUpdatedAt.IsZero() {
		t.Error("StatusUpdatedAt is zero after a status write")
	}
}

// A hook fired during a brand-new session's first run legitimately
// matches no row — agent_sessions rows are written when the terminal
// handoff returns, not at launch. That must not be an error, or every
// such session would print a failure into the user's transcript.
func TestSetSessionStatusUnknownSessionIsNotAnError(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	if err := s.SetSessionStatus(ctx, "never-seen", task.SessionIdle); err != nil {
		t.Fatalf("SetSessionStatus on an unknown session: %v", err)
	}
}

func TestListSessionsNeedingRecap(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	parent := mustAdd(t, s, "do the thing")
	for _, ext := range []string{"ext-1", "ext-2"} {
		if _, err := s.CreateSession(ctx, parent.ID, ext, "/tmp/work", parent.Title, "tend-"+ext); err != nil {
			t.Fatalf("CreateSession %s: %v", ext, err)
		}
	}
	if err := s.SetSessionNeedsRecap(ctx, "ext-2", true); err != nil {
		t.Fatalf("SetSessionNeedsRecap: %v", err)
	}

	owed, err := s.ListSessionsNeedingRecap(ctx)
	if err != nil {
		t.Fatalf("ListSessionsNeedingRecap: %v", err)
	}
	if len(owed) != 1 || owed[0].ExternalID != "ext-2" {
		t.Fatalf("owed = %+v, want just ext-2", owed)
	}
}

// Deliberately not filtered on status: a host that dies never gets to
// fire SessionEnd, and a status-gated query would strand that session's
// recap forever.
func TestListSessionsNeedingRecapIgnoresStatus(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	parent := mustAdd(t, s, "do the thing")
	if _, err := s.CreateSession(ctx, parent.ID, "ext-1", "/tmp/work", parent.Title, "tend-ext-1"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := s.SetSessionNeedsRecap(ctx, "ext-1", true); err != nil {
		t.Fatalf("SetSessionNeedsRecap: %v", err)
	}

	owed, err := s.ListSessionsNeedingRecap(ctx)
	if err != nil {
		t.Fatalf("ListSessionsNeedingRecap: %v", err)
	}
	if len(owed) != 1 {
		t.Fatalf("owed = %d, want the session with status still unknown", len(owed))
	}
}

// The two-instance guard: claiming is what decides who runs the expensive
// recap call, so exactly one caller may win.
func TestClaimSessionRecapIsExclusive(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	parent := mustAdd(t, s, "do the thing")
	if _, err := s.CreateSession(ctx, parent.ID, "ext-1", "/tmp/work", parent.Title, "tend-ext-1"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := s.SetSessionNeedsRecap(ctx, "ext-1", true); err != nil {
		t.Fatalf("SetSessionNeedsRecap: %v", err)
	}

	first, err := s.ClaimSessionRecap(ctx, "ext-1")
	if err != nil {
		t.Fatalf("ClaimSessionRecap: %v", err)
	}
	if !first {
		t.Fatal("first claim = false, want the debt claimed")
	}
	second, err := s.ClaimSessionRecap(ctx, "ext-1")
	if err != nil {
		t.Fatalf("second ClaimSessionRecap: %v", err)
	}
	if second {
		t.Error("second claim = true, want false — two instances would both recap")
	}

	owed, err := s.ListSessionsNeedingRecap(ctx)
	if err != nil {
		t.Fatalf("ListSessionsNeedingRecap: %v", err)
	}
	if len(owed) != 0 {
		t.Errorf("owed = %+v, want the claim to have cleared the debt", owed)
	}
}

func TestClaimSessionRecapWithNoDebt(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	got, err := s.ClaimSessionRecap(ctx, "never-seen")
	if err != nil {
		t.Fatalf("ClaimSessionRecap: %v", err)
	}
	if got {
		t.Error("claimed a debt that was never owed")
	}
}
