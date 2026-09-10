package tui

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jwstover/tend/internal/store"
	"github.com/jwstover/tend/internal/task"
	"github.com/jwstover/tend/internal/workflow"
)

// stubKillSession pins the tmux kill behind killSession and records the
// session names it was asked to end.
func stubKillSession(t *testing.T) *[]string {
	t.Helper()
	var names []string
	prev := killSession
	killSession = func(name string) error {
		names = append(names, name)
		return nil
	}
	t.Cleanup(func() { killSession = prev })
	return &names
}

// wideApp is newTestApp at a width where the agents view shows all three
// columns: projects, sessions and the pane.
func wideApp(t *testing.T) (tea.Model, *store.Store) {
	t.Helper()
	m, s := newTestApp(t)
	m = drive(t, m, tea.WindowSizeMsg{Width: 160, Height: 40})
	return m, s
}

// enterAgents drives `A` through to the view.
func enterAgents(t *testing.T, m tea.Model) tea.Model {
	t.Helper()
	m = drive(t, m, keyPress('A'))
	if a := m.(app); a.mode != modeAgents {
		t.Fatalf("mode = %v after A, status %+v; want the agents view", a.mode, a.status)
	}
	return m
}

// mustSession creates an interactive session row for a task.
func mustSession(t *testing.T, s *store.Store, tk task.Task, externalID, tmux string) task.Session {
	t.Helper()
	sess, err := s.CreateSession(context.Background(), tk.ID, externalID, "/tmp/work", tk.Title, tmux)
	if err != nil {
		t.Fatalf("CreateSession(%s): %v", externalID, err)
	}
	return sess
}

// The view lists the selected project's sessions across its tasks, live
// ones by default with `C` adding the ended ones, and the projects column
// re-scopes it.
func TestAgentsViewListsProjectSessions(t *testing.T) {
	ctx := context.Background()
	m, s := wideApp(t)
	proj, err := s.CreateProject(ctx, "hapi")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	inProj, err := s.AddTaskIn(ctx, proj.ID, "fix the scheduler")
	if err != nil {
		t.Fatalf("AddTaskIn: %v", err)
	}
	other, err := s.AddTask(ctx, "unrelated chore")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	live := mustSession(t, s, inProj, "ext-live", "tend-ext-live")
	mustSession(t, s, inProj, "ext-ended", "tend-ext-ended")
	if err := s.SetSessionStatus(ctx, "ext-ended", task.SessionEnded); err != nil {
		t.Fatalf("SetSessionStatus: %v", err)
	}
	if err := s.UpdateSessionLabel(ctx, "ext-live", "rewrote the poller"); err != nil {
		t.Fatalf("UpdateSessionLabel: %v", err)
	}
	mustSession(t, s, other, "ext-other", "tend-ext-other")
	m = drive(t, m, refreshMsg{})

	// All projects: both live sessions, the ended one hidden.
	m = enterAgents(t, m)
	a := m.(app)
	if len(a.av.rows) != 2 || a.hiddenAgents() != 1 {
		t.Fatalf("rows = %d hidden = %d, want 2 live rows and 1 ended hidden", len(a.av.rows), a.hiddenAgents())
	}
	content := ansi.Strip(m.View().Content)
	for _, want := range []string{"agents", "2 sessions", "SESSIONS", "1 ended hidden",
		"rewrote the poller", "fix the scheduler", "just now", "unrelated chore"} {
		if !strings.Contains(content, want) {
			t.Errorf("agents view missing %q:\n%s", want, content)
		}
	}
	if !strings.Contains(content, a.styles.Glyphs.Session[task.SessionStarting]) {
		t.Errorf("rows carry no status glyph:\n%s", content)
	}

	// `C` shows the ended session too.
	m = drive(t, m, keyPress('C'))
	if a := m.(app); len(a.av.rows) != 3 || !a.av.showEnded {
		t.Fatalf("rows after C = %d showEnded=%v, want all 3", len(a.av.rows), a.av.showEnded)
	}
	m = drive(t, m, keyPress('C'))

	// The projects column re-scopes: h focuses it, j moves to Unsorted,
	// then to hapi.
	m = drive(t, m, keyPress('h'))
	if a := m.(app); a.focus != paneProjects {
		t.Fatalf("focus = %v after h, want the projects column", a.focus)
	}
	m = drive(t, m, keyPress('j'))
	a = m.(app)
	if p, ok := a.selectedProject(); !ok || p.ID != task.DefaultProjectID {
		t.Fatalf("selected project = %+v, want Unsorted", p)
	}
	if len(a.av.rows) != 1 || a.av.rows[0].sess.TaskID != other.ID {
		t.Fatalf("rows for Unsorted = %+v, want the unrelated chore's session only", a.av.rows)
	}
	m = drive(t, m, keyPress('j'))
	a = m.(app)
	if len(a.av.rows) != 1 || a.av.rows[0].sess.ID != live.ID {
		t.Fatalf("rows for hapi = %+v, want its live session only", a.av.rows)
	}
	if !strings.Contains(ansi.Strip(m.View().Content), "hapi") {
		t.Error("header does not name the scoped project")
	}

	// esc/q leave for the list.
	m = drive(t, m, keyPress('l'))
	m = drive(t, m, keyPress('q'))
	if m.(app).mode != modeList {
		t.Error("q did not leave the agents view")
	}
}

// A headless session's row says so, its pane names the run, and the log
// tails from the step's stream-json on the poll tick; `v` in the pane
// flips to raw and `v` from the list opens the run view, which comes back
// here.
func TestAgentsViewHeadlessPaneTailsLog(t *testing.T) {
	stubRunnerAlive(t, true)
	m, s := wideApp(t)
	l := newLiveRun(t, s, workflow.RunRunning)
	if err := os.WriteFile(l.stepRun.LogPath, []byte(stepLogFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	m = drive(t, m, refreshMsg{})
	m = enterAgents(t, m)

	a := m.(app)
	row, ok := a.selectedAgent()
	if !ok || !row.headless() || row.run == nil || row.run.ID != l.run.ID || !a.av.follow {
		t.Fatalf("selected = %+v follow=%v, want the headless step session with its run, following", row, a.av.follow)
	}
	content := ansi.Strip(m.View().Content)
	for _, want := range []string{"headless", "ship it", "implement", "running", "LOG", "following",
		"Looking at the scheduler.", "⚙ Bash: go test ./...", "v to watch"} {
		if !strings.Contains(content, want) {
			t.Errorf("agents view missing %q:\n%s", want, content)
		}
	}
	if strings.Contains(content, `"type":"assistant"`) {
		t.Errorf("rendered pane leaks raw JSON:\n%s", content)
	}

	// The log grows; the poll tick appends.
	more := `{"type":"assistant","message":{"content":[{"type":"text","text":"All green."}]}}` + "\n"
	f, err := os.OpenFile(l.stepRun.LogPath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(more); err != nil {
		t.Fatal(err)
	}
	f.Close()
	m = drive(t, m, sessionsPolledMsg{changed: true})
	a = m.(app)
	if got := strings.Join(a.av.log.rendered, "\n"); !strings.Contains(got, "Looking at the scheduler.") || !strings.HasSuffix(got, "All green.") {
		t.Errorf("tailed log = %q, want the earlier lines kept and the new one appended", got)
	}

	// Into the pane, then raw.
	m = drive(t, m, keyPress('l'))
	if a := m.(app); a.focus != paneDetail {
		t.Fatalf("focus = %v after l, want the pane", a.focus)
	}
	m = drive(t, m, keyPress('v'))
	content = ansi.Strip(m.View().Content)
	if !strings.Contains(content, `"type":"assistant"`) || !strings.Contains(content, "raw") {
		t.Errorf("raw toggle did not show the stream-json lines:\n%s", content)
	}

	// `h` back to the sessions, where `v` watches the run; leaving it comes
	// back to the agents view.
	m = drive(t, m, keyPress('h'))
	if a := m.(app); a.focus != paneTasks || a.mode != modeAgents {
		t.Fatalf("focus=%v mode=%v after h, want the sessions list", a.focus, a.mode)
	}
	m = drive(t, m, keyPress('v'))
	if a := m.(app); a.mode != modeRun || a.rv.runID != l.run.ID {
		t.Fatalf("mode = %v run %d after v, want the run view on run %d", a.mode, a.rv.runID, l.run.ID)
	}
	m = drive(t, m, esc())
	if m.(app).mode != modeAgents {
		t.Error("leaving the run view did not return to the agents view")
	}
}

// Joining a headless session is refused while its run is live and not
// paused: the runner still owns that transcript.
func TestAgentsViewJoinHeadlessRefusedWhileRunLive(t *testing.T) {
	stubRunnerAlive(t, true)
	m, s := wideApp(t)
	newLiveRun(t, s, workflow.RunRunning)
	m = drive(t, m, refreshMsg{})
	m = enterAgents(t, m)

	m = drive(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	a := m.(app)
	if !a.status.isErr || !strings.Contains(a.status.text, "pause the run first") {
		t.Errorf("status = %+v, want a refusal that says to pause the run", a.status)
	}
}

// `dd` on an interactive session kills its tmux session and marks the row
// ended -- never deletes it -- and the row then hides with the other ended
// ones.
func TestAgentsViewKillInteractiveSession(t *testing.T) {
	ctx := context.Background()
	killed := stubKillSession(t)
	m, s := wideApp(t)
	tk, err := s.AddTask(ctx, "fix the bug")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	sess := mustSession(t, s, tk, "ext-1", "tend-ext-1")
	m = drive(t, m, refreshMsg{})
	m = enterAgents(t, m)

	// A single d only arms the chord and shows the kill panel.
	m = drive(t, m, keyPress('d'))
	a := m.(app)
	if !a.deletePending {
		t.Fatal("first d did not arm the chord")
	}
	if content := ansi.Strip(m.View().Content); !strings.Contains(content, "kill session") {
		t.Errorf("panel does not say kill session:\n%s", content)
	}
	m = drive(t, m, esc())
	if a := m.(app); a.deletePending || a.mode != modeAgents {
		t.Fatal("esc did not back out of the chord in place")
	}
	if got, _ := s.ListSessionsForTask(ctx, tk.ID); got[0].Status != task.SessionStarting {
		t.Fatalf("a single d changed the session to %s", got[0].Status)
	}

	m = drive(t, m, keyPress('d'))
	m = drive(t, m, keyPress('d'))
	waitFor(t, "session ended", func() bool {
		got, err := s.ListSessionsForTask(ctx, tk.ID)
		return err == nil && len(got) == 1 && got[0].ID == sess.ID && got[0].Status == task.SessionEnded
	})
	if len(*killed) != 1 || (*killed)[0] != "tend-ext-1" {
		t.Errorf("killSession calls = %v, want the session's tmux name once", *killed)
	}
	m = drive(t, m, refreshMsg{})
	a = m.(app)
	if len(a.av.rows) != 0 || a.hiddenAgents() != 1 {
		t.Errorf("rows = %d hidden = %d after the kill, want the ended row hidden", len(a.av.rows), a.hiddenAgents())
	}
}

// `dd` on a headless session whose run is live cancels the run, leaving
// the process for the runner to stop.
func TestAgentsViewKillHeadlessCancelsRun(t *testing.T) {
	ctx := context.Background()
	stubRunnerAlive(t, true)
	killed := stubKillSession(t)
	m, s := wideApp(t)
	l := newLiveRun(t, s, workflow.RunRunning)
	m = drive(t, m, refreshMsg{})
	m = enterAgents(t, m)

	m = drive(t, m, keyPress('d'))
	if content := ansi.Strip(m.View().Content); !strings.Contains(content, "cancel its workflow run") {
		t.Errorf("panel does not say the run is cancelled:\n%s", content)
	}
	m = drive(t, m, keyPress('d'))
	waitFor(t, "run cancelled", func() bool {
		run, err := s.GetRun(ctx, l.run.ID)
		return err == nil && run.State == workflow.RunCancelled
	})
	if a := m.(app); a.deletePending || a.mode != modeAgents {
		t.Errorf("after dd: deletePending=%v mode=%v, want the chord closed in the agents view", a.deletePending, a.mode)
	}
	if len(*killed) != 0 {
		t.Errorf("a headless kill reached tmux: %v", *killed)
	}
}

// A background reload keeps the cursor on the same session by id even
// when a status change moves it to another row.
func TestAgentsViewReloadHoldsCursorByID(t *testing.T) {
	ctx := context.Background()
	m, s := wideApp(t)
	tk, err := s.AddTask(ctx, "fix the bug")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	older := mustSession(t, s, tk, "ext-1", "tend-ext-1")
	mustSession(t, s, tk, "ext-2", "tend-ext-2")
	m = drive(t, m, refreshMsg{})
	m = enterAgents(t, m)

	// Newest first: ext-2 then ext-1. Move onto ext-1.
	m = drive(t, m, keyPress('j'))
	a := m.(app)
	if row, ok := a.selectedAgent(); !ok || row.sess.ID != older.ID || a.av.cursor != 1 {
		t.Fatalf("selected = %+v at %d, want ext-1 on row 1", row.sess, a.av.cursor)
	}

	// ext-1 asks for the user: it sorts to the top on the next reload.
	if err := s.SetSessionStatus(ctx, "ext-1", task.SessionBlocked); err != nil {
		t.Fatalf("SetSessionStatus: %v", err)
	}
	m = drive(t, m, dbChangedMsg{})
	a = m.(app)
	if row, ok := a.selectedAgent(); !ok || row.sess.ID != older.ID || a.av.cursor != 0 {
		t.Errorf("selected = %+v at %d after the reload, want ext-1 followed to row 0", row.sess, a.av.cursor)
	}
	if a.liveReloadInFlight {
		t.Error("live reload still marked in flight after the load landed")
	}
}

// An interactive session's pane is its task's detail pane: body, log and
// sessions, under a heading naming the session.
func TestAgentsViewInteractivePaneShowsTask(t *testing.T) {
	ctx := context.Background()
	m, s := wideApp(t)
	tk, err := s.AddTask(ctx, "fix the bug")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	if err := s.SetBody(ctx, tk.ID, "Reproduce with the flaky fixture first."); err != nil {
		t.Fatalf("SetBody: %v", err)
	}
	if _, err := s.AddLogEntry(ctx, &tk.ID, "narrowed it to the scheduler"); err != nil {
		t.Fatalf("AddLogEntry: %v", err)
	}
	mustSession(t, s, tk, "ext-1", "tend-ext-1")
	m = drive(t, m, refreshMsg{})
	m = enterAgents(t, m)

	content := ansi.Strip(m.View().Content)
	for _, want := range []string{"tmux tend-ext-1", "#" + strconv.FormatInt(tk.ID, 10), "cwd", "/tmp/work",
		"Reproduce with the flaky fixture first.", "LOG", "narrowed it to the scheduler", "SESSIONS"} {
		if !strings.Contains(content, want) {
			t.Errorf("pane missing %q:\n%s", want, content)
		}
	}
}
