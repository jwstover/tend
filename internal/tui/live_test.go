package tui

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jwstover/tend/internal/task"
)

// stubWatcher scripts Changed's answers in order; once the script runs
// out it reports "no change" forever.
type stubWatcher struct {
	mu      sync.Mutex
	answers []bool
	err     error
	calls   int
}

func (w *stubWatcher) Changed(context.Context) (bool, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.calls++
	if w.err != nil {
		return false, w.err
	}
	if len(w.answers) == 0 {
		return false, nil
	}
	next := w.answers[0]
	w.answers = w.answers[1:]
	return next, nil
}

// The goroutine's whole contract: one dbChangedMsg per tick that reports
// a change, nothing for a quiet tick, and it stops when ctx is canceled.
func TestRunChangeWatcherSendsOnlyOnChange(t *testing.T) {
	w := &stubWatcher{answers: []bool{false, true, false, true}}
	var mu sync.Mutex
	var got []tea.Msg
	send := func(msg tea.Msg) {
		mu.Lock()
		got = append(got, msg)
		mu.Unlock()
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runChangeWatcher(ctx, w, time.Millisecond, send)
		close(done)
	}()

	waitFor(t, "two dbChangedMsg sends", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(got) == 2
	})
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runChangeWatcher did not return after cancel")
	}

	mu.Lock()
	defer mu.Unlock()
	for _, msg := range got {
		if _, ok := msg.(dbChangedMsg); !ok {
			t.Errorf("sent %T, want dbChangedMsg", msg)
		}
	}
	if len(got) != 2 {
		t.Errorf("sent %d messages, want exactly 2 (one per true answer)", len(got))
	}
}

// A read error must neither send nor stop the loop: the next tick simply
// tries again.
func TestRunChangeWatcherSkipsErrors(t *testing.T) {
	w := &stubWatcher{err: errors.New("boom")}
	sent := false
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runChangeWatcher(ctx, w, time.Millisecond, func(tea.Msg) { sent = true })
		close(done)
	}()
	waitFor(t, "several failed ticks", func() bool {
		w.mu.Lock()
		defer w.mu.Unlock()
		return w.calls >= 3
	})
	cancel()
	<-done
	if sent {
		t.Error("a failing Changed produced a send")
	}
}

// The user-visible promise: a write from elsewhere shows up on the next
// dbChangedMsg, with the cursor still on the task the user was looking at
// even though rows were added above it and the selected task moved to a
// different section.
func TestDBChangedReloadsAndKeepsSelectionByID(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	first, err := s.AddTask(ctx, "first task")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	second, err := s.AddTask(ctx, "second task")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	m = drive(t, m, refreshMsg{})
	m = drive(t, m, keyPress('j'))
	if sel, ok := m.(app).selected(); !ok || sel.ID != second.ID {
		t.Fatalf("selection after j = %+v, want second task", sel)
	}
	before := m.(app).projects
	var liveBefore int64
	for _, p := range before {
		if p.ID == task.DefaultProjectID {
			liveBefore = p.LiveCount
		}
	}

	// "Another process" writes: a new inbox task that sorts above, and the
	// selected task promoted to doing, which moves it into the section
	// rendered first. The store's pool is another connection from the
	// watcher's point of view too, so writing through s is the same as
	// writing from a second tend.
	if _, err := s.AddTask(ctx, "agent created this"); err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	if err := s.SetState(ctx, second.ID, task.StateDoing); err != nil {
		t.Fatalf("SetState: %v", err)
	}
	if err := s.SetState(ctx, first.ID, task.StateTodo); err != nil {
		t.Fatalf("SetState: %v", err)
	}

	m = drive(t, m, dbChangedMsg{})
	a := m.(app)

	content := ansi.Strip(m.View().Content)
	if !strings.Contains(content, "agent created this") {
		t.Errorf("new task missing after live reload:\n%s", content)
	}
	if sel, ok := a.selected(); !ok || sel.ID != second.ID {
		t.Errorf("selection after live reload = %+v, want the second task still", sel)
	}
	if sel, _ := a.selected(); sel.State != task.StateDoing {
		t.Errorf("selected task state = %q, want doing (reloaded row)", sel.State)
	}
	var liveAfter int64
	for _, p := range a.projects {
		if p.ID == task.DefaultProjectID {
			liveAfter = p.LiveCount
		}
	}
	if liveAfter != liveBefore+1 {
		t.Errorf("project live count = %d, want %d (projects column reloaded too)", liveAfter, liveBefore+1)
	}
	if a.status.text != "" {
		t.Errorf("live reload flashed %q; a background update should be silent", a.status.text)
	}
	if a.liveReloadInFlight || a.liveReloadDeferred {
		t.Errorf("bookkeeping not settled: inFlight=%v deferred=%v", a.liveReloadInFlight, a.liveReloadDeferred)
	}
}

// A change that lands while the user is typing into a prompt is held, not
// applied — and then applied by the key that closes the prompt.
func TestDBChangedDefersWhilePromptOpen(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	if _, err := s.AddTask(ctx, "existing"); err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	m = drive(t, m, refreshMsg{})

	m = drive(t, m, keyPress('n')) // quick-add prompt
	if m.(app).promptKind != promptAdd {
		t.Fatalf("promptKind = %v, want promptAdd", m.(app).promptKind)
	}
	if _, err := s.AddTask(ctx, "arrived mid-typing"); err != nil {
		t.Fatalf("AddTask: %v", err)
	}

	m = drive(t, m, dbChangedMsg{})
	if !m.(app).liveReloadDeferred {
		t.Error("change while prompt open was not deferred")
	}
	if strings.Contains(ansi.Strip(m.View().Content), "arrived mid-typing") {
		t.Error("list rebuilt under an open prompt")
	}

	m = drive(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	a := m.(app)
	if a.promptKind != promptNone {
		t.Fatalf("esc did not close the prompt")
	}
	if a.liveReloadDeferred {
		t.Error("deferred reload still pending after the prompt closed")
	}
	if !strings.Contains(ansi.Strip(m.View().Content), "arrived mid-typing") {
		t.Errorf("deferred reload not applied after esc:\n%s", ansi.Strip(m.View().Content))
	}
}

// Same for a `/` filter being typed: the list owns the keyboard, and a
// rebuild under it would reset what the user has typed so far.
func TestDBChangedDefersWhileFiltering(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	if _, err := s.AddTask(ctx, "alpha"); err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	m = drive(t, m, refreshMsg{})

	m = drive(t, m, keyPress('/'))
	m = drive(t, m, keyPress('a'))
	if !m.(app).list.SettingFilter() {
		t.Fatal("list is not in filter-setting state")
	}
	if _, err := s.AddTask(ctx, "another alpha"); err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	m = drive(t, m, dbChangedMsg{})
	if !m.(app).liveReloadDeferred {
		t.Error("change while filtering was not deferred")
	}

	m = drive(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	a := m.(app)
	if a.list.SettingFilter() {
		t.Fatal("esc did not leave filter-setting state")
	}
	if a.liveReloadDeferred {
		t.Error("deferred reload still pending after the filter closed")
	}
	if !strings.Contains(ansi.Strip(m.View().Content), "another alpha") {
		t.Errorf("deferred reload not applied after esc:\n%s", ansi.Strip(m.View().Content))
	}
}

// Two changes arriving faster than a reload completes fan out as at most
// two loads, never a third — and never zero: a change noticed while a load
// is out re-checks once the load settles, since the pragma read that
// noticed it is consumed and would otherwise be lost.
func TestDBChangedCoalescesWhileReloadInFlight(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	if _, err := s.AddTask(ctx, "existing"); err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	m = drive(t, m, refreshMsg{})

	// Feed Update directly rather than through drive, so the reload the
	// first dbChangedMsg issues is still "in flight" when the second
	// arrives.
	a := m.(app)
	var cmd tea.Cmd
	m, cmd = a.Update(dbChangedMsg{})
	if cmd == nil {
		t.Fatal("first dbChangedMsg issued no reload")
	}
	a = m.(app)
	if !a.liveReloadInFlight {
		t.Fatal("first dbChangedMsg did not mark a reload in flight")
	}
	m, second := a.Update(dbChangedMsg{})
	a = m.(app)
	if second != nil {
		t.Error("second dbChangedMsg issued a reload while one was in flight")
	}
	if !a.liveReloadDeferred {
		t.Error("second dbChangedMsg was dropped rather than deferred")
	}

	// A write that happens between the two — after the first reload's
	// queries might already have run — must still appear once everything
	// settles.
	if _, err := s.AddTask(ctx, "slipped in between"); err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	m = drive(t, m, tea.BatchMsg{cmd})
	a = m.(app)
	if a.liveReloadInFlight || a.liveReloadDeferred {
		t.Errorf("bookkeeping not settled: inFlight=%v deferred=%v", a.liveReloadInFlight, a.liveReloadDeferred)
	}
	if !strings.Contains(ansi.Strip(m.View().Content), "slipped in between") {
		t.Errorf("write during an in-flight reload was lost:\n%s", ansi.Strip(m.View().Content))
	}
}

// Standup and workflows reload through the same path (an agent finishing
// a step, or a session recap landing as a note, is exactly what those
// views exist to show), but without the position jump a fresh entry does.
func TestDBChangedReloadsStandupWithoutJumping(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	m = drive(t, m, keyPress('S'))
	if m.(app).mode != modeStandup {
		t.Fatalf("mode = %v, want standup", m.(app).mode)
	}
	if _, err := s.AddLogEntry(ctx, nil, "written by a recap"); err != nil {
		t.Fatalf("AddLogEntry: %v", err)
	}
	m = drive(t, m, dbChangedMsg{})
	a := m.(app)
	if len(a.standupNotes) != 1 || a.standupNotes[0].Body != "written by a recap" {
		t.Errorf("standup notes after live reload = %+v, want the new note", a.standupNotes)
	}
	if a.standupJumpToLatest {
		t.Error("live reload asked the standup view to jump to latest")
	}
}
