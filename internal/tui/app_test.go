package tui

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jwstover/td/internal/store"
	"github.com/jwstover/td/internal/task"
)

// drive applies a message and, like the Bubble Tea runtime, keeps feeding
// resulting command output back into Update until no commands remain.
func drive(t *testing.T, m tea.Model, msg tea.Msg) tea.Model {
	t.Helper()
	queue := []tea.Msg{msg}
	for len(queue) > 0 {
		var next tea.Msg
		next, queue = queue[0], queue[1:]
		var cmd tea.Cmd
		m, cmd = m.Update(next)
		for _, out := range collect(cmd) {
			queue = append(queue, out)
		}
	}
	return m
}

// collect runs a command tree and gathers produced messages. Commands
// that block (e.g. cursor blink timers) are abandoned after a short wait.
func collect(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	ch := make(chan tea.Msg, 1)
	go func() { ch <- cmd() }()
	var msg tea.Msg
	select {
	case msg = <-ch:
	case <-time.After(100 * time.Millisecond):
		return nil
	}
	if msg == nil {
		return nil
	}
	switch batch := msg.(type) {
	case tea.BatchMsg:
		var out []tea.Msg
		for _, c := range batch {
			out = append(out, collect(c)...)
		}
		return out
	}
	return []tea.Msg{msg}
}

func keyPress(r rune) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: r, Text: string(r)}
}

func newTestApp(t *testing.T) (tea.Model, *store.Store) {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(ctx, filepath.Join(t.TempDir(), "td.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	var m tea.Model = newApp(ctx, s)
	m = drive(t, m, m.Init()())
	m = drive(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	return m, s
}

func TestAppRendersLiveTasks(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	if _, err := s.AddTask(ctx, "write the report"); err != nil {
		t.Fatalf("AddTask: %v", err)
	}

	m = drive(t, m, refreshMsg{})
	content := ansi.Strip(m.View().Content)
	if !strings.Contains(content, "write the report") {
		t.Errorf("view missing task title:\n%s", content)
	}
	if !strings.Contains(content, "INBOX (1)") {
		t.Errorf("view missing state section heading:\n%s", content)
	}
}

func TestListGroupsTasksByState(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)

	states := map[string]task.State{
		"capture me": task.StateInbox,
		"queue me":   task.StateTodo,
		"start me":   task.StateDoing,
	}
	for title, st := range states {
		captured, err := s.AddTask(ctx, title)
		if err != nil {
			t.Fatalf("AddTask: %v", err)
		}
		if err := s.SetState(ctx, captured.ID, st); err != nil {
			t.Fatalf("SetState: %v", err)
		}
	}

	m = drive(t, m, refreshMsg{})
	content := ansi.Strip(m.View().Content)

	// Headings appear in display order: doing, then todo, then inbox.
	doing := strings.Index(content, "DOING (1)")
	todo := strings.Index(content, "TODO (1)")
	inbox := strings.Index(content, "INBOX (1)")
	if doing == -1 || todo == -1 || inbox == -1 {
		t.Fatalf("missing section headings:\n%s", content)
	}
	if !(doing < todo && todo < inbox) {
		t.Errorf("headings out of order (doing=%d todo=%d inbox=%d):\n%s", doing, todo, inbox, content)
	}

	// The cursor starts on the first task, not the heading, and j/k
	// navigation lands on tasks only.
	if sel, ok := m.(app).selected(); !ok || sel.Title != "start me" {
		t.Errorf("initial selection = %+v, want start me", sel)
	}
	m = drive(t, m, keyPress('j'))
	if sel, ok := m.(app).selected(); !ok || sel.Title != "queue me" {
		t.Errorf("selection after j = %+v, want queue me", sel)
	}
	m = drive(t, m, keyPress('k'))
	if sel, ok := m.(app).selected(); !ok || sel.Title != "start me" {
		t.Errorf("selection after k = %+v, want start me", sel)
	}
}

func TestTriageStateKey(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	captured, err := s.AddTask(ctx, "triage me")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}

	m = drive(t, m, keyPress('i')) // enter triage
	if !strings.Contains(ansi.Strip(m.View().Content), "triage me") {
		t.Fatalf("triage view missing inbox task:\n%s", ansi.Strip(m.View().Content))
	}

	m = drive(t, m, keyPress('x')) // mark done
	got, err := s.GetTask(ctx, captured.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.State != task.StateDone {
		t.Errorf("state after x = %s, want done", got.State)
	}
	if strings.Contains(ansi.Strip(m.View().Content), "triage me") {
		t.Error("done task should leave the triage view")
	}
}

func TestChangeStateChord(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	captured, err := s.AddTask(ctx, "finish me")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	m = drive(t, m, refreshMsg{})

	// `c` arms the chord and shows the state hint.
	m = drive(t, m, keyPress('c'))
	if !m.(app).statePending {
		t.Fatal("statePending = false after c, want true")
	}
	if !strings.Contains(ansi.Strip(m.View().Content), "state →") {
		t.Errorf("footer missing chord hint:\n%s", ansi.Strip(m.View().Content))
	}

	// The next state key applies the mutation.
	m = drive(t, m, keyPress('x'))
	got, err := s.GetTask(ctx, captured.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.State != task.StateDone {
		t.Errorf("state after c,x = %s, want done", got.State)
	}
	if m.(app).statePending {
		t.Error("statePending still true after chord completed")
	}

	// A non-state key cancels the chord without mutating or navigating.
	m = drive(t, m, keyPress('c'))
	m = drive(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.(app).statePending {
		t.Error("statePending still true after esc")
	}
	got, err = s.GetTask(ctx, captured.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.State != task.StateDone {
		t.Errorf("state after cancelled chord = %s, want done", got.State)
	}
}

func TestQuickAddPrompt(t *testing.T) {
	m, s := newTestApp(t)

	m = drive(t, m, keyPress('n'))
	for _, r := range "ship it" {
		m = drive(t, m, keyPress(r))
	}
	m = drive(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})

	live, err := s.ListLive(context.Background())
	if err != nil {
		t.Fatalf("ListLive: %v", err)
	}
	if len(live) != 1 || live[0].Title != "ship it" {
		t.Fatalf("ListLive = %+v, want the quick-added task", live)
	}
	if !strings.Contains(ansi.Strip(m.View().Content), "ship it") {
		t.Errorf("view missing quick-added task:\n%s", ansi.Strip(m.View().Content))
	}
}

func TestDetailPaneShowsBodyAndSubtasks(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	parent, err := s.AddTask(ctx, "parent task")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	if err := s.SetBody(ctx, parent.ID, "# Context\nsee https://example.com/spec"); err != nil {
		t.Fatalf("SetBody: %v", err)
	}
	if _, err := s.AddChild(ctx, parent.ID, "a sub-task"); err != nil {
		t.Fatalf("AddChild: %v", err)
	}

	m = drive(t, m, refreshMsg{})
	m = drive(t, m, keyPress(']')) // open detail

	content := ansi.Strip(m.View().Content)
	for _, want := range []string{"Context", "a sub-task", "sub-tasks 0/1", "https://example.com/spec"} {
		if !strings.Contains(content, want) {
			t.Errorf("detail pane missing %q:\n%s", want, content)
		}
	}
}
