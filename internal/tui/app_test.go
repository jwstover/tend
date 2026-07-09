package tui

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jwstover/tend/internal/store"
	"github.com/jwstover/tend/internal/task"
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
		queue = append(queue, collect(cmd)...)
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
	s, err := store.Open(ctx, filepath.Join(t.TempDir(), "tend.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	var m tea.Model = newApp(ctx, s, "")
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
	if !strings.Contains(content, "● inbox") {
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
	// (The header nudge renders "● N in inbox", which "● inbox" skips.)
	doing := strings.Index(content, "◐ doing")
	todo := strings.Index(content, "○ todo")
	inbox := strings.Index(content, "● inbox")
	if doing == -1 || todo == -1 || inbox == -1 {
		t.Fatalf("missing section headings:\n%s", content)
	}
	if doing >= todo || todo >= inbox {
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

func TestToggleCompletedVisibility(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)

	done, err := s.AddTask(ctx, "finished thing")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	if err := s.SetState(ctx, done.ID, task.StateDone); err != nil {
		t.Fatalf("SetState: %v", err)
	}
	if _, err := s.AddTask(ctx, "pending thing"); err != nil {
		t.Fatalf("AddTask: %v", err)
	}

	m = drive(t, m, refreshMsg{})
	content := ansi.Strip(m.View().Content)
	if strings.Contains(content, "finished thing") {
		t.Fatalf("completed task should be hidden by default:\n%s", content)
	}
	if !strings.Contains(content, "pending thing") {
		t.Fatalf("non-completed task should show:\n%s", content)
	}

	// C reveals the completed section without dropping other tasks.
	m = drive(t, m, keyPress('C'))
	content = ansi.Strip(m.View().Content)
	if !strings.Contains(content, "finished thing") {
		t.Errorf("completed task should appear after C:\n%s", content)
	}
	if !strings.Contains(content, "pending thing") {
		t.Errorf("non-completed task should still show:\n%s", content)
	}

	// C again hides it.
	m = drive(t, m, keyPress('C'))
	if strings.Contains(ansi.Strip(m.View().Content), "finished thing") {
		t.Errorf("completed task should hide after second C:\n%s", ansi.Strip(m.View().Content))
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

func TestTriageShowsOneCardWithProgress(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	if _, err := s.AddTask(ctx, "first capture"); err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	if _, err := s.AddTask(ctx, "second capture"); err != nil {
		t.Fatalf("AddTask: %v", err)
	}

	m = drive(t, m, keyPress('i'))
	content := ansi.Strip(m.View().Content)
	if !strings.Contains(content, "first capture") {
		t.Errorf("triage card missing the current task title:\n%s", content)
	}
	if strings.Contains(content, "second capture") {
		t.Errorf("triage shows more than the current card:\n%s", content)
	}
	// The capture sits inside a bordered card with the inbox dot.
	if !strings.Contains(content, "┌") || !strings.Contains(content, "└") {
		t.Errorf("triage card missing its border:\n%s", content)
	}
	if !strings.Contains(lineWith(content, "first capture"), "●") {
		t.Errorf("card title line missing the inbox dot:\n%s", content)
	}
	if !strings.Contains(content, "captured from shell · no body yet") {
		t.Errorf("card missing the no-body line:\n%s", content)
	}
	// Session progress: bar label and header counter.
	if !strings.Contains(content, "0 done · 2 left") {
		t.Errorf("progress label missing:\n%s", content)
	}
	if !strings.Contains(content, "1 of 2") || !strings.Contains(content, "processing inbox") {
		t.Errorf("header missing the triage counter:\n%s", content)
	}
	// The action grid is on screen.
	for _, want := range []string{"→ todo", "→ someday", "edit body in $EDITOR", "skip for now"} {
		if !strings.Contains(content, want) {
			t.Errorf("action grid missing %q:\n%s", want, content)
		}
	}
}

func TestTriageCardNotesExistingBody(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	captured, err := s.AddTask(ctx, "fleshed out")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	if err := s.SetBody(ctx, captured.ID, "some context"); err != nil {
		t.Fatalf("SetBody: %v", err)
	}

	m = drive(t, m, keyPress('i'))
	content := ansi.Strip(m.View().Content)
	if !strings.Contains(content, "body present") {
		t.Errorf("card does not note the existing body:\n%s", content)
	}
	if strings.Contains(content, "no body yet") {
		t.Errorf("card claims no body despite one existing:\n%s", content)
	}
}

func TestTriageProgressAdvancesOnAction(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	first, err := s.AddTask(ctx, "first capture")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	if _, err := s.AddTask(ctx, "second capture"); err != nil {
		t.Fatalf("AddTask: %v", err)
	}

	m = drive(t, m, keyPress('i'))
	m = drive(t, m, keyPress('t')) // current card → todo

	got, err := s.GetTask(ctx, first.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.State != task.StateTodo {
		t.Errorf("state after t = %s, want todo", got.State)
	}
	content := ansi.Strip(m.View().Content)
	if !strings.Contains(content, "1 done · 1 left") {
		t.Errorf("progress did not advance after action:\n%s", content)
	}
	if !strings.Contains(content, "2 of 2") {
		t.Errorf("header counter did not advance:\n%s", content)
	}
	if !strings.Contains(content, "second capture") {
		t.Errorf("next card not shown after action:\n%s", content)
	}
}

func TestTriageSkipCyclesToBack(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	first, err := s.AddTask(ctx, "first capture")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	if _, err := s.AddTask(ctx, "second capture"); err != nil {
		t.Fatalf("AddTask: %v", err)
	}

	m = drive(t, m, keyPress('i'))
	m = drive(t, m, tea.KeyPressMsg{Code: tea.KeyEnter}) // skip
	content := ansi.Strip(m.View().Content)
	if !strings.Contains(content, "second capture") || strings.Contains(content, "first capture") {
		t.Errorf("skip did not advance to the next card:\n%s", content)
	}
	// Skip is pure TUI state: nothing processed, nothing mutated.
	if !strings.Contains(content, "0 done · 2 left") {
		t.Errorf("skip changed the progress count:\n%s", content)
	}
	got, err := s.GetTask(ctx, first.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.State != task.StateInbox {
		t.Errorf("skip mutated the task state to %s", got.State)
	}

	// A second skip wraps back around to the first card.
	m = drive(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	content = ansi.Strip(m.View().Content)
	if !strings.Contains(content, "first capture") {
		t.Errorf("skipped card did not cycle back to the front:\n%s", content)
	}
}

func TestTriageActionsTargetCurrentCard(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	first, err := s.AddTask(ctx, "first capture")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	second, err := s.AddTask(ctx, "second capture")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}

	m = drive(t, m, keyPress('i'))
	m = drive(t, m, tea.KeyPressMsg{Code: tea.KeyEnter}) // skip onto the second card
	m = drive(t, m, keyPress('d'))                       // → doing

	got, err := s.GetTask(ctx, second.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.State != task.StateDoing {
		t.Errorf("second task state = %s, want doing", got.State)
	}
	got, err = s.GetTask(ctx, first.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.State != task.StateInbox {
		t.Errorf("first task mutated to %s; action hit the wrong card", got.State)
	}

	// The prompt-based actions target the current card too.
	m = drive(t, m, keyPress('P'))
	a := m.(app)
	if a.promptKind != promptProject || a.promptTarget != first.ID {
		t.Errorf("P prompt targets (%v, %d), want (promptProject, %d)",
			a.promptKind, a.promptTarget, first.ID)
	}
}

func TestTriageInboxZero(t *testing.T) {
	m, _ := newTestApp(t)

	m = drive(t, m, keyPress('i'))
	content := ansi.Strip(m.View().Content)
	for _, want := range []string{
		"inbox zero",
		"Nothing left to process. The dump is clean.",
		`tend add "…"`,
		"esc back to list",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("inbox-zero screen missing %q:\n%s", want, content)
		}
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

	// `c` arms the chord and shows the which-key panel.
	m = drive(t, m, keyPress('c'))
	if !m.(app).statePending {
		t.Fatal("statePending = false after c, want true")
	}
	content := ansi.Strip(m.View().Content)
	for _, want := range []string{"state", "t todo", "x done", "esc cancel"} {
		if !strings.Contains(content, want) {
			t.Errorf("which-key panel missing %q:\n%s", want, content)
		}
	}

	// The next state key applies the mutation and dismisses the panel.
	m = drive(t, m, keyPress('x'))
	if strings.Contains(ansi.Strip(m.View().Content), "esc cancel") {
		t.Errorf("panel still visible after chord resolved:\n%s", ansi.Strip(m.View().Content))
	}
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

func TestPriorityChord(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	captured, err := s.AddTask(ctx, "rank me")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	m = drive(t, m, refreshMsg{})

	// `p` arms the chord and shows the which-key panel.
	m = drive(t, m, keyPress('p'))
	if !m.(app).priorityPending {
		t.Fatal("priorityPending = false after p, want true")
	}
	content := ansi.Strip(m.View().Content)
	for _, want := range []string{"priority", "a A (highest)", "n none", "esc cancel"} {
		if !strings.Contains(content, want) {
			t.Errorf("which-key panel missing %q:\n%s", want, content)
		}
	}

	// The next priority key applies the mutation and dismisses the panel.
	m = drive(t, m, keyPress('a'))
	if strings.Contains(ansi.Strip(m.View().Content), "esc cancel") {
		t.Errorf("panel still visible after chord resolved:\n%s", ansi.Strip(m.View().Content))
	}
	got, err := s.GetTask(ctx, captured.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.Priority == nil || *got.Priority != 1 {
		t.Errorf("priority after p,a = %v, want 1", got.Priority)
	}
	if m.(app).priorityPending {
		t.Error("priorityPending still true after chord completed")
	}
	if !strings.Contains(ansi.Strip(m.View().Content), "⚑A") {
		t.Errorf("list missing priority badge:\n%s", ansi.Strip(m.View().Content))
	}

	// Esc cancels the chord without mutating.
	m = drive(t, m, keyPress('p'))
	m = drive(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.(app).priorityPending {
		t.Error("priorityPending still true after esc")
	}
	got, err = s.GetTask(ctx, captured.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.Priority == nil || *got.Priority != 1 {
		t.Errorf("priority after cancelled chord = %v, want 1", got.Priority)
	}

	// `p`,`n` clears the priority.
	m = drive(t, m, keyPress('p'))
	_ = drive(t, m, keyPress('n'))
	got, err = s.GetTask(ctx, captured.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.Priority != nil {
		t.Errorf("priority after p,n = %v, want nil", got.Priority)
	}
}

func TestProjectPromptFromListView(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	captured, err := s.AddTask(ctx, "file me")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	m = drive(t, m, refreshMsg{})

	m = drive(t, m, keyPress('P'))
	if m.(app).promptKind != promptProject {
		t.Fatalf("promptKind after P = %v, want promptProject", m.(app).promptKind)
	}
	for _, r := range "home" {
		m = drive(t, m, keyPress(r))
	}
	_ = drive(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})

	got, err := s.GetTask(ctx, captured.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.Project == nil || *got.Project != "home" {
		t.Errorf("project after P prompt = %v, want home", got.Project)
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

func TestLogEntrySaves(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	captured, err := s.AddTask(ctx, "log me")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	m = drive(t, m, refreshMsg{})

	m = drive(t, m, keyPress('U'))
	for _, r := range "first line" {
		m = drive(t, m, keyPress(r))
	}
	m = drive(t, m, tea.KeyPressMsg{Code: tea.KeyEnter}) // newline, not submit
	for _, r := range "second line" {
		m = drive(t, m, keyPress(r))
	}
	m = drive(t, m, tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModCtrl}) // ctrl+enter submits

	if m.(app).modal.Active() {
		t.Error("modal still active after submit")
	}
	notes := allLogEntries(t, s)
	if len(notes) != 1 {
		t.Fatalf("log entries = %+v, want exactly one", notes)
	}
	if notes[0].TaskID == nil || *notes[0].TaskID != captured.ID {
		t.Errorf("note TaskID = %v, want attached to #%d", notes[0].TaskID, captured.ID)
	}
	if notes[0].Body != "first line\nsecond line" {
		t.Errorf("note body = %q, want the multi-line entry", notes[0].Body)
	}
	// Notes are their own table now; the task body stays untouched.
	got, err := s.GetTask(ctx, captured.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.BodyMD != "" {
		t.Errorf("body mutated by note capture:\n%s", got.BodyMD)
	}
}

func TestLogEntryAltEnterSubmits(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	if _, err := s.AddTask(ctx, "log me"); err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	m = drive(t, m, refreshMsg{})

	m = drive(t, m, keyPress('U'))
	for _, r := range "new note" {
		m = drive(t, m, keyPress(r))
	}
	_ = drive(t, m, tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModAlt}) // alt+enter also submits

	notes := allLogEntries(t, s)
	if len(notes) != 1 || notes[0].Body != "new note" {
		t.Errorf("log entries = %+v, want the alt+enter note", notes)
	}
}

// allLogEntries is a test helper: every note from the epoch to well past now.
func allLogEntries(t *testing.T, s *store.Store) []task.LogEntry {
	t.Helper()
	notes, err := s.ListLogEntries(context.Background(),
		time.Unix(0, 0), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("ListLogEntries: %v", err)
	}
	return notes
}

func TestLogEntryEscCancels(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	captured, err := s.AddTask(ctx, "log me")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	m = drive(t, m, refreshMsg{})

	m = drive(t, m, keyPress('U'))
	for _, r := range "discard this" {
		m = drive(t, m, keyPress(r))
	}
	m = drive(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})

	if m.(app).modal.Active() {
		t.Error("modal still active after esc")
	}
	if _, err := s.GetTask(ctx, captured.ID); err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if notes := allLogEntries(t, s); len(notes) != 0 {
		t.Errorf("cancelled modal wrote a note: %+v", notes)
	}
}

func TestLogEntryEmptyIsNoop(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	captured, err := s.AddTask(ctx, "log me")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	m = drive(t, m, refreshMsg{})

	m = drive(t, m, keyPress('U'))
	m = drive(t, m, tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModCtrl})

	if m.(app).modal.Active() {
		t.Error("modal still active after empty submit")
	}
	if _, err := s.GetTask(ctx, captured.ID); err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if notes := allLogEntries(t, s); len(notes) != 0 {
		t.Errorf("empty submit wrote a note: %+v", notes)
	}
}

func TestLogEntryModalRenders(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	if _, err := s.AddTask(ctx, "log me"); err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	m = drive(t, m, refreshMsg{})

	m = drive(t, m, keyPress('U'))
	content := ansi.Strip(m.View().Content)
	if !strings.Contains(content, "note — #") {
		t.Errorf("view missing modal title:\n%s", content)
	}
	if !strings.Contains(content, "esc cancel") {
		t.Errorf("view missing modal help:\n%s", content)
	}

	// The modal floats over the list: the background view stays visible
	// and the box is vertically centered, not pinned to the first line.
	if !strings.Contains(content, "log me") {
		t.Errorf("background list hidden behind modal:\n%s", content)
	}
	lines := strings.Split(content, "\n")
	titleLine := -1
	for i, l := range lines {
		if strings.Contains(l, "note — #") {
			titleLine = i
			break
		}
	}
	if titleLine < 5 {
		t.Errorf("modal title on line %d, want vertically centered:\n%s", titleLine, content)
	}
}

// runeCol returns the rune column where sub starts in line, or -1.
func runeCol(line, sub string) int {
	i := strings.Index(line, sub)
	if i < 0 {
		return -1
	}
	return len([]rune(line[:i]))
}

// lineWith returns the first rendered line containing the substring.
func lineWith(content, sub string) string {
	for _, l := range strings.Split(content, "\n") {
		if strings.Contains(l, sub) {
			return l
		}
	}
	return ""
}

func TestExpandCollapseBranch(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	parent, err := s.AddTask(ctx, "parent task")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	child, err := s.AddChild(ctx, parent.ID, "child step")
	if err != nil {
		t.Fatalf("AddChild: %v", err)
	}
	if _, err := s.AddChild(ctx, child.ID, "grandchild step"); err != nil {
		t.Fatalf("AddChild: %v", err)
	}
	m = drive(t, m, refreshMsg{})

	// Collapsed by default: a ▸ caret, no child rows.
	content := ansi.Strip(m.View().Content)
	if strings.Contains(content, "child step") {
		t.Fatalf("children visible before expansion:\n%s", content)
	}
	if !strings.Contains(lineWith(content, "parent task"), "▸") {
		t.Errorf("parent row missing closed caret:\n%s", content)
	}

	// ⏎ expands one level: the caret flips and the child slides in with
	// its checkbox; the grandchild stays hidden behind the child's caret.
	m = drive(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	content = ansi.Strip(m.View().Content)
	if !strings.Contains(lineWith(content, "parent task"), "▾") {
		t.Errorf("parent caret did not flip open:\n%s", content)
	}
	childLine := lineWith(content, "child step")
	if !strings.Contains(childLine, "▢") || !strings.Contains(childLine, "▸") {
		t.Errorf("child row missing checkbox or caret: %q", childLine)
	}
	if strings.Contains(content, "grandchild step") {
		t.Errorf("grandchild visible before its branch expanded:\n%s", content)
	}

	// j descends onto the child; l expands its own branch one indent
	// deeper.
	m = drive(t, m, keyPress('j'))
	if sel, ok := m.(app).selected(); !ok || sel.ID != child.ID {
		t.Fatalf("selection after j = %+v, want the child", sel)
	}
	m = drive(t, m, keyPress('l'))
	content = ansi.Strip(m.View().Content)
	grandLine := lineWith(content, "grandchild step")
	if grandLine == "" {
		t.Fatalf("grandchild missing after l:\n%s", content)
	}
	if gi, ci := runeCol(grandLine, "grandchild step"), runeCol(childLine, "child step"); gi <= ci {
		t.Errorf("grandchild not indented deeper (grand=%d child=%d):\n%q\n%q", gi, ci, grandLine, childLine)
	}

	// h closes the child's branch; a second h closes the parent's branch
	// and lands the cursor back on the parent.
	m = drive(t, m, keyPress('h'))
	if strings.Contains(ansi.Strip(m.View().Content), "grandchild step") {
		t.Error("grandchild still visible after h")
	}
	m = drive(t, m, keyPress('h'))
	content = ansi.Strip(m.View().Content)
	if strings.Contains(content, "child step") {
		t.Errorf("child still visible after collapsing the parent:\n%s", content)
	}
	if sel, ok := m.(app).selected(); !ok || sel.ID != parent.ID {
		t.Errorf("selection after collapse = %+v, want the parent", sel)
	}
}

func TestExpandHintInFooter(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	parent, err := s.AddTask(ctx, "parent task")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	if _, err := s.AddChild(ctx, parent.ID, "child step"); err != nil {
		t.Fatalf("AddChild: %v", err)
	}
	if _, err := s.AddTask(ctx, "a leaf"); err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	m = drive(t, m, refreshMsg{})

	if !strings.Contains(ansi.Strip(m.View().Content), "⏎ expand") {
		t.Errorf("footer missing expand hint on a parent:\n%s", ansi.Strip(m.View().Content))
	}
	m = drive(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if !strings.Contains(ansi.Strip(m.View().Content), "⏎ collapse") {
		t.Errorf("footer missing collapse hint on an expanded parent:\n%s", ansi.Strip(m.View().Content))
	}
	m = drive(t, m, keyPress('j')) // the child — a leaf
	content := ansi.Strip(m.View().Content)
	if strings.Contains(content, "⏎ expand") || strings.Contains(content, "⏎ collapse") {
		t.Errorf("footer shows expansion hint on a leaf:\n%s", content)
	}
}

func TestToggleChildDoneWithX(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	parent, err := s.AddTask(ctx, "parent task")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	child, err := s.AddChild(ctx, parent.ID, "child step")
	if err != nil {
		t.Fatalf("AddChild: %v", err)
	}
	m = drive(t, m, refreshMsg{})

	m = drive(t, m, tea.KeyPressMsg{Code: tea.KeyEnter}) // expand
	m = drive(t, m, keyPress('j'))                       // onto the child
	m = drive(t, m, keyPress('x'))                       // check it

	got, err := s.GetTask(ctx, child.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.State != task.StateDone {
		t.Fatalf("child state after x = %s, want done", got.State)
	}
	content := ansi.Strip(m.View().Content)
	if !strings.Contains(lineWith(content, "child step"), "▣") {
		t.Errorf("done child missing checked box:\n%s", content)
	}
	if !strings.Contains(lineWith(content, "parent task"), "1/1") {
		t.Errorf("parent count not 1/1 after child done:\n%s", content)
	}

	// x again un-checks: done child → todo.
	if sel, ok := m.(app).selected(); !ok || sel.ID != child.ID {
		t.Fatalf("selection drifted after refresh: %+v", sel)
	}
	_ = drive(t, m, keyPress('x'))
	got, err = s.GetTask(ctx, child.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.State != task.StateTodo {
		t.Errorf("child state after second x = %s, want todo", got.State)
	}
}

func TestXCompletesTopLevelTask(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	captured, err := s.AddTask(ctx, "finish me")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	m = drive(t, m, refreshMsg{})

	m = drive(t, m, keyPress('x'))
	got, err := s.GetTask(ctx, captured.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.State != task.StateDone {
		t.Errorf("state after x = %s, want done", got.State)
	}
	if strings.Contains(ansi.Strip(m.View().Content), "finish me") {
		t.Error("done task should leave the live view")
	}
}

func TestDetailFollowsOwningTask(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	parent, err := s.AddTask(ctx, "parent task")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	if _, err := s.AddChild(ctx, parent.ID, "first step"); err != nil {
		t.Fatalf("AddChild: %v", err)
	}
	if _, err := s.AddChild(ctx, parent.ID, "second step"); err != nil {
		t.Fatalf("AddChild: %v", err)
	}
	m = drive(t, m, refreshMsg{})

	m = drive(t, m, tea.KeyPressMsg{Code: tea.KeyEnter}) // expand
	m = drive(t, m, keyPress(']'))                       // open detail
	m = drive(t, m, keyPress('j'))                       // onto first step
	m = drive(t, m, keyPress('j'))                       // onto second step

	a := m.(app)
	if sel, ok := a.selected(); !ok || sel.Title != "second step" {
		t.Fatalf("selection = %+v, want second step", sel)
	}
	if a.detailID != parent.ID {
		t.Errorf("detailID = %d, want the owning task %d", a.detailID, parent.ID)
	}
	content := ansi.Strip(m.View().Content)
	if !strings.Contains(content, "SUB-TASKS  0/2") {
		t.Errorf("detail pane not showing the owner's checklist:\n%s", content)
	}
}

func TestEnterOnLeafTogglesDetail(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	if _, err := s.AddTask(ctx, "a leaf"); err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	m = drive(t, m, refreshMsg{})

	m = drive(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.(app).showDetail {
		t.Error("enter on a leaf should open the detail pane")
	}
	m = drive(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.(app).showDetail {
		t.Error("enter again should close the detail pane")
	}
}

func TestAddSubTaskAutoExpands(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	parent, err := s.AddTask(ctx, "parent task")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	m = drive(t, m, refreshMsg{})

	m = drive(t, m, keyPress('a'))
	for _, r := range "new step" {
		m = drive(t, m, keyPress(r))
	}
	m = drive(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})

	children, err := s.ListChildren(ctx, parent.ID)
	if err != nil {
		t.Fatalf("ListChildren: %v", err)
	}
	if len(children) != 1 || children[0].Title != "new step" {
		t.Fatalf("ListChildren = %+v, want the new sub-task", children)
	}
	content := ansi.Strip(m.View().Content)
	if !strings.Contains(lineWith(content, "new step"), "▢") {
		t.Errorf("new sub-task not visible under its auto-expanded parent:\n%s", content)
	}
}

func TestPaletteFilterAndRun(t *testing.T) {
	m, _ := newTestApp(t)

	m = drive(t, m, keyPress(':'))
	if !m.(app).paletteOpen {
		t.Fatal("palette not open after :")
	}
	content := ansi.Strip(m.View().Content)
	for _, want := range []string{"❯", "Toggle detail pane", "Triage the inbox", "Quit"} {
		if !strings.Contains(content, want) {
			t.Errorf("palette missing %q:\n%s", want, content)
		}
	}

	// Typing filters case-insensitively; "tri" leaves only the triage
	// command and ⏎ runs it.
	for _, r := range "tri" {
		m = drive(t, m, keyPress(r))
	}
	content = ansi.Strip(m.View().Content)
	if !strings.Contains(content, "Triage the inbox") {
		t.Fatalf("filtered palette missing the triage command:\n%s", content)
	}
	if strings.Contains(content, "Quick-add to inbox") {
		t.Errorf("filter kept a non-matching command:\n%s", content)
	}
	m = drive(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	a := m.(app)
	if a.paletteOpen {
		t.Error("palette still open after enter")
	}
	if a.mode != modeTriage {
		t.Errorf("mode after running triage command = %v, want modeTriage", a.mode)
	}
}

func TestPaletteEscDismissesAndSwallowsKeys(t *testing.T) {
	m, _ := newTestApp(t)

	m = drive(t, m, keyPress(':'))
	// While open, list keys are query text, not navigation/quit.
	m = drive(t, m, keyPress('q'))
	a := m.(app)
	if !a.paletteOpen || a.paletteQuery != "q" {
		t.Fatalf("palette state after typing q = (%v, %q), want (true, q)", a.paletteOpen, a.paletteQuery)
	}
	m = drive(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.(app).paletteOpen {
		t.Error("palette still open after esc")
	}
}

func TestPaletteTypedQuitAlias(t *testing.T) {
	m, _ := newTestApp(t)

	m = drive(t, m, keyPress(':'))
	for _, r := range "quit" {
		m = drive(t, m, keyPress(r))
	}
	// The alias match must run tea.Quit on enter.
	m2, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	quit := false
	for _, msg := range collect(cmd) {
		if _, ok := msg.(tea.QuitMsg); ok {
			quit = true
		}
	}
	if !quit {
		t.Errorf("`:quit` did not produce tea.QuitMsg")
	}
	if m2.(app).paletteOpen {
		t.Error("palette still open after running quit")
	}
}

func TestPaletteAddWithArgument(t *testing.T) {
	m, s := newTestApp(t)

	m = drive(t, m, keyPress(':'))
	for _, r := range "add buy milk" {
		m = drive(t, m, keyPress(r))
	}
	content := ansi.Strip(m.View().Content)
	if !strings.Contains(content, `Add task: "buy milk"`) {
		t.Fatalf("palette missing the synthetic add entry:\n%s", content)
	}
	m = drive(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})

	inbox, err := s.ListInbox(context.Background())
	if err != nil {
		t.Fatalf("ListInbox: %v", err)
	}
	if len(inbox) != 1 || inbox[0].Title != "buy milk" {
		t.Fatalf("ListInbox = %+v, want the palette-added task", inbox)
	}
	// The capture flash leads with the inbox plus glyph.
	footer := lineWith(ansi.Strip(m.View().Content), "captured to inbox: buy milk")
	if !strings.Contains(footer, "✚") {
		t.Errorf("capture flash missing ✚ glyph: %q", footer)
	}
}

func TestHelpOverlay(t *testing.T) {
	m, _ := newTestApp(t)

	// The footer advertises it in list mode.
	if !strings.Contains(ansi.Strip(m.View().Content), "? help") {
		t.Errorf("footer missing the help hint:\n%s", ansi.Strip(m.View().Content))
	}

	// The full reference outgrew the default 30-row test window (the
	// splice drops top rows); give it room so every group is visible.
	m = drive(t, m, tea.WindowSizeMsg{Width: 100, Height: 45})
	m = drive(t, m, keyPress('?'))
	if !m.(app).helpOpen {
		t.Fatal("help not open after ?")
	}
	content := ansi.Strip(m.View().Content)
	for _, want := range []string{"NAVIGATE", "CAPTURE & FIND", "PROCESS", "STANDUP",
		"command palette", "quick-add to inbox", "change state (chord)", "esc close"} {
		if !strings.Contains(content, want) {
			t.Errorf("help overlay missing %q:\n%s", want, content)
		}
	}

	// Non-closing keys are swallowed; ? again closes.
	m = drive(t, m, keyPress('j'))
	if !m.(app).helpOpen {
		t.Fatal("help dismissed by a swallowed key")
	}
	m = drive(t, m, keyPress('?'))
	if m.(app).helpOpen {
		t.Error("help still open after second ?")
	}
	m = drive(t, m, keyPress('?'))
	m = drive(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.(app).helpOpen {
		t.Error("help still open after esc")
	}
}

func TestLoadingFrameBeforeFirstLoad(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, filepath.Join(t.TempDir(), "tend.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	var m tea.Model = newApp(ctx, s, "/data/tend/tend.db")
	m = drive(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	content := ansi.Strip(m.View().Content)
	if !strings.Contains(content, "loading tasks…") {
		t.Fatalf("pre-load frame missing the loading line:\n%s", content)
	}
	if !strings.Contains(content, "reading /data/tend/tend.db") {
		t.Errorf("pre-load frame missing the db path:\n%s", content)
	}

	// The first load replaces the frame with the list.
	m = drive(t, m, m.Init()())
	if strings.Contains(ansi.Strip(m.View().Content), "loading tasks…") {
		t.Error("loading frame still shown after the first load")
	}
}

func TestFlashGlyphs(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	if _, err := s.AddTask(ctx, "finish me"); err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	m = drive(t, m, refreshMsg{})

	// Done: ✓ leads the state-change flash.
	m = drive(t, m, keyPress('x'))
	footer := lineWith(ansi.Strip(m.View().Content), "→ done")
	if footer == "" || !strings.Contains(footer, "✓") {
		t.Errorf("done flash missing ✓ glyph: %q", footer)
	}

	// Capture: ✚ leads the quick-add flash.
	m = drive(t, m, keyPress('n'))
	for _, r := range "new capture" {
		m = drive(t, m, keyPress(r))
	}
	m = drive(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	footer = lineWith(ansi.Strip(m.View().Content), "captured to inbox: new capture")
	if footer == "" || !strings.Contains(footer, "✚") {
		t.Errorf("capture flash missing ✚ glyph: %q", footer)
	}

	// Edit: ✎ leads the body-save flash.
	m = drive(t, m, refreshMsg{status: flash{kind: flashEdit, text: "body saved"}})
	footer = lineWith(ansi.Strip(m.View().Content), "body saved")
	if footer == "" || !strings.Contains(footer, "✎") {
		t.Errorf("edit flash missing ✎ glyph: %q", footer)
	}

	// Errors keep the plain red style — no glyph.
	m = drive(t, m, errMsg{err: fmt.Errorf("boom")})
	footer = lineWith(ansi.Strip(m.View().Content), "boom")
	if footer == "" || strings.Contains(footer, "✓") || strings.Contains(footer, "✚") {
		t.Errorf("error flash should be plain: %q", footer)
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
	for _, want := range []string{"Context", "a sub-task", "SUB-TASKS  0/1", "https://example.com/spec"} {
		if !strings.Contains(content, want) {
			t.Errorf("detail pane missing %q:\n%s", want, content)
		}
	}
}

// twoLinkApp returns an app with one selected task whose body has two links.
func twoLinkApp(t *testing.T) tea.Model {
	t.Helper()
	ctx := context.Background()
	m, s := newTestApp(t)
	parent, err := s.AddTask(ctx, "parent task")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	body := "see https://example.com/one and https://example.com/two"
	if err := s.SetBody(ctx, parent.ID, body); err != nil {
		t.Fatalf("SetBody: %v", err)
	}
	return drive(t, m, refreshMsg{})
}

func TestOpenURLPickerMultipleLinks(t *testing.T) {
	m := twoLinkApp(t)

	// `o` on a task with 2+ links opens the picker, listing every URL.
	m = drive(t, m, keyPress('o'))
	if !m.(app).urlPickerOpen {
		t.Fatal("picker not open after o with multiple links")
	}
	content := ansi.Strip(m.View().Content)
	for _, want := range []string{"open link", "https://example.com/one", "https://example.com/two"} {
		if !strings.Contains(content, want) {
			t.Errorf("picker missing %q:\n%s", want, content)
		}
	}

	// ↓ moves the selection; esc dismisses without opening anything.
	m = drive(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	if sel := m.(app).urlPickerSel; sel != 1 {
		t.Errorf("selection after ↓ = %d, want 1", sel)
	}
	m = drive(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.(app).urlPickerOpen {
		t.Error("picker still open after esc")
	}
}

func TestURLPickerDigitOpens(t *testing.T) {
	m := twoLinkApp(t)
	m = drive(t, m, keyPress('o'))

	// Typing the index opens that link and closes the picker. Step once so
	// the resulting open command isn't run (it shells out to the OS opener).
	m2, cmd := m.Update(keyPress('2'))
	if m2.(app).urlPickerOpen {
		t.Error("picker still open after typing an index")
	}
	if cmd == nil {
		t.Error("typing an index did not produce an open command")
	}

	// An out-of-range digit is ignored: the picker stays open.
	m3, _ := m.Update(keyPress('9'))
	if !m3.(app).urlPickerOpen {
		t.Error("picker closed on an out-of-range digit")
	}
}

func TestOpenURLSingleLinkSkipsPicker(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	parent, err := s.AddTask(ctx, "parent task")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	if err := s.SetBody(ctx, parent.ID, "see https://example.com/only"); err != nil {
		t.Fatalf("SetBody: %v", err)
	}
	m = drive(t, m, refreshMsg{})

	// One link opens directly — no picker. `o` resolves the task's links
	// off the update loop; feed the resolved message back by hand and stop
	// there, so the open command (which shells out) isn't run.
	m2, cmd := m.Update(keyPress('o'))
	if cmd == nil {
		t.Fatal("o did not produce a resolve command")
	}
	m3, cmd := m2.Update(cmd())
	if m3.(app).urlPickerOpen {
		t.Error("picker opened for a single link")
	}
	if cmd == nil {
		t.Error("single link did not produce an open command")
	}
}

func TestOpenURLIncludesLogEntryLinks(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	parent, err := s.AddTask(ctx, "parent task")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	if err := s.SetBody(ctx, parent.ID, "see https://example.com/body"); err != nil {
		t.Fatalf("SetBody: %v", err)
	}
	if _, err := s.AddLogEntry(ctx, &parent.ID, "filed https://example.com/from-log"); err != nil {
		t.Fatalf("AddLogEntry: %v", err)
	}
	m = drive(t, m, refreshMsg{})

	// The detail pane's link list counts the log entry's URL too.
	m = drive(t, m, keyPress(']'))
	content := ansi.Strip(m.View().Content)
	if !strings.Contains(content, "2 links detected") {
		t.Errorf("detail pane missing log link in link list:\n%s", content)
	}

	// `o` offers both links: body first, then the log entry's.
	m = drive(t, m, keyPress('o'))
	if !m.(app).urlPickerOpen {
		t.Fatal("picker not open when body and log each hold a link")
	}
	want := []link{{url: "https://example.com/body"}, {url: "https://example.com/from-log"}}
	if got := m.(app).urlPickerURLs; !slices.Equal(got, want) {
		t.Errorf("picker URLs = %v, want %v", got, want)
	}
}

func TestURLPickerShowsMarkdownLinkTitles(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	parent, err := s.AddTask(ctx, "parent task")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	body := "see [the spec](https://example.com/spec) and https://example.com/bare"
	if err := s.SetBody(ctx, parent.ID, body); err != nil {
		t.Fatalf("SetBody: %v", err)
	}
	m = drive(t, m, refreshMsg{})

	// The detail pane's link list shows the markdown title, not its URL;
	// the bare link still shows as a URL.
	m = drive(t, m, keyPress(']'))
	content := ansi.Strip(m.View().Content)
	for _, want := range []string{"[1] the spec", "[2] https://example.com/bare"} {
		if !strings.Contains(content, want) {
			t.Errorf("detail link list missing %q:\n%s", want, content)
		}
	}
	if strings.Contains(content, "[1] https://example.com/spec") {
		t.Errorf("detail link list shows the titled link's URL instead of its title:\n%s", content)
	}

	// The picker shows the title too, and opening it targets the URL.
	m = drive(t, m, keyPress('o'))
	want := []link{
		{title: "the spec", url: "https://example.com/spec"},
		{url: "https://example.com/bare"},
	}
	if got := m.(app).urlPickerURLs; !slices.Equal(got, want) {
		t.Errorf("picker links = %v, want %v", got, want)
	}
	// Assert on the picker box alone — the detail pane behind it still
	// renders the body, URL included.
	box := ansi.Strip(m.(app).urlPickerView())
	if !strings.Contains(box, "the spec") {
		t.Errorf("picker missing markdown link title:\n%s", box)
	}
	if strings.Contains(box, "https://example.com/spec") {
		t.Errorf("picker shows the titled link's URL instead of its title:\n%s", box)
	}
}

func TestStandupViewRendersNotesAndReport(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	captured, err := s.AddTask(ctx, "ship the feature")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	if err := s.SetState(ctx, captured.ID, task.StateDone); err != nil {
		t.Fatalf("SetState: %v", err)
	}
	if _, err := s.AddLogEntry(ctx, nil, "paired with sam"); err != nil {
		t.Fatalf("AddLogEntry: %v", err)
	}
	m = drive(t, m, refreshMsg{})

	m = drive(t, m, keyPress('S'))
	if m.(app).mode != modeStandup {
		t.Fatal("S did not enter the standup view")
	}
	content := ansi.Strip(m.View().Content)
	for _, want := range []string{"standup", "NOTES", "REPORT",
		"paired with sam", "ship the feature", "Today", "Blockers"} {
		if !strings.Contains(content, want) {
			t.Errorf("standup view missing %q:\n%s", want, content)
		}
	}

	// esc returns to the list.
	m = drive(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.(app).mode != modeList {
		t.Error("esc did not leave the standup view")
	}
}

func TestStandupNoteCapture(t *testing.T) {
	m, s := newTestApp(t)

	m = drive(t, m, keyPress('S'))
	m = drive(t, m, keyPress('n')) // in standup, add means note
	for _, r := range "quick thought" {
		m = drive(t, m, keyPress(r))
	}
	m = drive(t, m, tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModCtrl})

	notes := allLogEntries(t, s)
	if len(notes) != 1 || notes[0].Body != "quick thought" {
		t.Fatalf("log entries = %+v, want the captured note", notes)
	}
	if notes[0].TaskID != nil {
		t.Errorf("note TaskID = %v, want freestanding (nil)", notes[0].TaskID)
	}
	// The refresh lands it in the notes pane immediately.
	if !strings.Contains(ansi.Strip(m.View().Content), "quick thought") {
		t.Errorf("captured note not visible in the pane:\n%s", ansi.Strip(m.View().Content))
	}
}

func TestGlobalNoteKey(t *testing.T) {
	m, s := newTestApp(t)

	// N captures a freestanding note without leaving the list view.
	m = drive(t, m, keyPress('N'))
	for _, r := range "from the list" {
		m = drive(t, m, keyPress(r))
	}
	m = drive(t, m, tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModCtrl})

	if m.(app).mode != modeList {
		t.Error("note capture should not change modes")
	}
	notes := allLogEntries(t, s)
	if len(notes) != 1 || notes[0].Body != "from the list" || notes[0].TaskID != nil {
		t.Fatalf("log entries = %+v, want one freestanding note", notes)
	}
}

func TestStandupWindowPaging(t *testing.T) {
	m, _ := newTestApp(t)
	m = drive(t, m, keyPress('S'))
	start := m.(app).standupSince

	m = drive(t, m, keyPress('h'))
	if got := m.(app).standupSince; !got.Equal(start.AddDate(0, 0, -1)) {
		t.Errorf("since after h = %v, want a day before %v", got, start)
	}
	m = drive(t, m, keyPress('l'))
	if got := m.(app).standupSince; !got.Equal(start) {
		t.Errorf("since after h,l = %v, want back at %v", got, start)
	}

	// l never pushes the window past today's local midnight.
	for range 10 {
		m = drive(t, m, keyPress('l'))
	}
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	if got := m.(app).standupSince; !got.Equal(today) {
		t.Errorf("since after many l = %v, want clamped to %v", got, today)
	}
}

func TestDetailPaneShowsTaskLog(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	captured, err := s.AddTask(ctx, "task with history")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	if _, err := s.AddLogEntry(ctx, &captured.ID, "earlier note"); err != nil {
		t.Fatalf("AddLogEntry: %v", err)
	}
	m = drive(t, m, refreshMsg{})

	m = drive(t, m, keyPress(']'))
	content := ansi.Strip(m.View().Content)
	if !strings.Contains(content, "LOG") {
		t.Errorf("detail pane missing the LOG section:\n%s", content)
	}
	if !strings.Contains(content, "earlier note") {
		t.Errorf("detail pane missing the seeded note:\n%s", content)
	}

	// A note captured with U lands in the open pane via the refresh.
	m = drive(t, m, keyPress('U'))
	for _, r := range "fresh note" {
		m = drive(t, m, keyPress(r))
	}
	m = drive(t, m, tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModCtrl})
	content = ansi.Strip(m.View().Content)
	if !strings.Contains(content, "fresh note") {
		t.Errorf("captured note not visible in the detail pane:\n%s", content)
	}
	// Newest first.
	if strings.Index(content, "fresh note") > strings.Index(content, "earlier note") {
		t.Errorf("log entries not newest-first:\n%s", content)
	}
}

func TestStandupNotesGroupedByTaskWithToggle(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	captured, err := s.AddTask(ctx, "ship the retry fix")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	if _, err := s.AddLogEntry(ctx, &captured.ID, "still flaky on CI"); err != nil {
		t.Fatalf("AddLogEntry: %v", err)
	}
	if _, err := s.AddLogEntry(ctx, nil, "paired with sam"); err != nil {
		t.Fatalf("AddLogEntry: %v", err)
	}

	m = drive(t, m, keyPress('S'))
	content := ansi.Strip(m.View().Content)
	// Grouped by default: the task title heads its notes, freestanding
	// notes sit under "general", and no bare day header shows.
	if !strings.Contains(content, "ship the retry fix") {
		t.Errorf("grouped notes missing the task title:\n%s", content)
	}
	if !strings.Contains(content, "general") {
		t.Errorf("grouped notes missing the freestanding group:\n%s", content)
	}
	title := strings.Index(content, "ship the retry fix")
	body := strings.Index(content, "still flaky on CI")
	if title == -1 || body == -1 || title > body {
		t.Errorf("note body should follow its task header:\n%s", content)
	}

	// s flips to the flat chronology: day headers appear.
	m = drive(t, m, keyPress('s'))
	if !m.(app).standupChrono {
		t.Fatal("s did not switch to chronological order")
	}
	content = ansi.Strip(m.View().Content)
	dayHeader := time.Now().Format("Mon Jan 2")
	if !strings.Contains(content, dayHeader) {
		t.Errorf("chronological notes missing the %q day header:\n%s", dayHeader, content)
	}
	if !strings.Contains(content, "ship the retry fix: ") {
		t.Errorf("chronological entry missing its task-title prefix:\n%s", content)
	}

	// s again returns to grouped.
	m = drive(t, m, keyPress('s'))
	if m.(app).standupChrono {
		t.Error("second s did not return to grouped order")
	}
}

func TestStandupNotesSplitByDay(t *testing.T) {
	m, _ := newTestApp(t)
	m = drive(t, m, keyPress('S'))

	// Inject a two-day window directly; AddLogEntry always stamps now.
	id := int64(7)
	yesterday := time.Now().AddDate(0, 0, -1)
	m = drive(t, m, standupLoadedMsg{notes: []task.LogEntry{
		{TaskID: &id, TaskTitle: "ship it", Body: "note from yesterday", CreatedAt: yesterday},
		{TaskID: &id, TaskTitle: "ship it", Body: "note from today", CreatedAt: time.Now()},
	}})

	content := ansi.Strip(m.View().Content)
	yHeader := strings.Index(content, "Yesterday · "+yesterday.Format("Mon Jan 2"))
	tHeader := strings.Index(content, "Today · "+time.Now().Format("Mon Jan 2"))
	if yHeader == -1 || tHeader == -1 || yHeader > tHeader {
		t.Fatalf("day sections missing or out of order:\n%s", content)
	}
	yNote := strings.Index(content, "note from yesterday")
	tNote := strings.Index(content, "note from today")
	if yHeader >= yNote || yNote >= tHeader || tHeader >= tNote {
		t.Errorf("notes not under their day headers:\n%s", content)
	}
	// The task group repeats per day rather than pooling both notes.
	if strings.Count(content, "ship it") != 2 {
		t.Errorf("task header should appear once per day:\n%s", content)
	}
}

func TestQuitKeyClosesViewBeforeQuitting(t *testing.T) {
	// q backs out of non-default views (standup, detail, triage, help)
	// and only quits from the bare list.
	pressQ := func(m tea.Model) (tea.Model, bool) {
		t.Helper()
		m, cmd := m.Update(keyPress('q'))
		for _, msg := range collect(cmd) {
			if _, ok := msg.(tea.QuitMsg); ok {
				return m, true
			}
			m = drive(t, m, msg)
		}
		return m, false
	}

	m, _ := newTestApp(t)

	m = drive(t, m, keyPress('S'))
	m, quit := pressQ(m)
	if quit || m.(app).mode != modeList {
		t.Errorf("q in standup: quit=%v mode=%v, want back to list", quit, m.(app).mode)
	}

	m = drive(t, m, keyPress('i'))
	m, quit = pressQ(m)
	if quit || m.(app).mode != modeList {
		t.Errorf("q in triage: quit=%v mode=%v, want back to list", quit, m.(app).mode)
	}

	m = drive(t, m, keyPress('?'))
	m, quit = pressQ(m)
	if quit || m.(app).helpOpen {
		t.Errorf("q in help: quit=%v helpOpen=%v, want overlay closed", quit, m.(app).helpOpen)
	}

	m = drive(t, m, keyPress(']'))
	m, quit = pressQ(m)
	if quit || m.(app).showDetail {
		t.Errorf("q with detail open: quit=%v showDetail=%v, want pane closed", quit, m.(app).showDetail)
	}

	if _, quit = pressQ(m); !quit {
		t.Errorf("q on the bare list did not quit")
	}

	// ctrl+c quits from anywhere, even inside a view.
	m = drive(t, m, keyPress('S'))
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	quit = false
	for _, msg := range collect(cmd) {
		if _, ok := msg.(tea.QuitMsg); ok {
			quit = true
		}
	}
	if !quit {
		t.Errorf("ctrl+c in standup did not quit")
	}
}
