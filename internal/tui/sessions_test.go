package tui

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jwstover/tend/internal/agent"
	"github.com/jwstover/tend/internal/store"
	"github.com/jwstover/tend/internal/task"
)

// launched writes the row launchSessionCmd would have written right
// before handing the terminal to claude, so a test can drive the return
// half (sessionFinishedMsg) against a row that exists the way it does in
// production. tmux is the wrapping tmux session's name, "" for a launch
// without tmux.
func launched(t *testing.T, s *store.Store, tk task.Task, externalID, cwd, tmux string) task.Session {
	t.Helper()
	sess, err := s.CreateSession(context.Background(), tk.ID, externalID, cwd, tk.Title, tmux)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	return sess
}

// finished builds the sessionFinishedMsg a clean return of that launched
// session's handoff would produce. Callers set backgrounded/err/run ids on
// the result for the other outcomes.
func finished(sess task.Session) sessionFinishedMsg {
	return sessionFinishedMsg{
		sessionRowID: sess.ID,
		taskID:       sess.TaskID,
		externalID:   sess.ExternalID,
		cwd:          sess.Cwd,
		label:        sess.Label,
		tmuxSession:  sess.TmuxSession,
	}
}

// isolateLaunchFiles points every file a launch writes — the tmux config
// under XDG_CONFIG_HOME and the MCP/hook temp files under TMPDIR — at
// per-test directories, so exercising the launch path leaves nothing in
// the developer's real config or temp directories.
func isolateLaunchFiles(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("TMPDIR", t.TempDir())
}

// stepR presses `r` on the selected task and runs the resulting load
// command by hand, stopping short of anything that would shell out to
// claude — mirrors the o/url-picker tests' "step once" idiom.
func stepR(t *testing.T, m tea.Model) tea.Model {
	t.Helper()
	m2, cmd := m.Update(keyPress('r'))
	if cmd == nil {
		t.Fatal("r did not produce a load command")
	}
	m3, _ := m2.Update(cmd())
	return m3
}

func TestSessionsKeyNoSessionsOpensCwdPrompt(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	if _, err := s.AddTask(ctx, "do the thing"); err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	m = drive(t, m, refreshMsg{})

	m = stepR(t, m)
	a := m.(app)
	if a.promptKind != promptSessionCwd {
		t.Fatalf("promptKind = %v, want promptSessionCwd", a.promptKind)
	}
	if a.sessionPicker.open {
		t.Error("picker should be skipped with no existing sessions")
	}
	if a.prompt.Value() != a.startCwd {
		t.Errorf("prompt prefilled with %q, want startCwd %q", a.prompt.Value(), a.startCwd)
	}
}

func TestSessionsKeyWithSessionsOpensPicker(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	parent, err := s.AddTask(ctx, "ongoing work")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	if _, err := s.CreateSession(ctx, parent.ID, "ext-1", "/tmp/work", parent.Title, ""); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	m = drive(t, m, refreshMsg{})

	m = stepR(t, m)
	a := m.(app)
	if !a.sessionPicker.open {
		t.Fatal("picker not open with an existing session")
	}
	if len(a.sessionPicker.items) != 1 || a.sessionPicker.items[0].ExternalID != "ext-1" {
		t.Errorf("sessionPicker.items = %+v, want one session ext-1", a.sessionPicker.items)
	}
	if a.sessionPicker.sel != 0 {
		t.Errorf("sessionPicker.sel = %d, want 0 (+ new session)", a.sessionPicker.sel)
	}

	content := ansi.Strip(m.View().Content)
	for _, want := range []string{"claude sessions", "+ new session", "work"} {
		if !strings.Contains(content, want) {
			t.Errorf("picker missing %q:\n%s", want, content)
		}
	}
}

func TestSessionPickerEnterOnNewRowOpensPromptWithLastCwd(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	parent, err := s.AddTask(ctx, "ongoing work")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	if _, err := s.CreateSession(ctx, parent.ID, "ext-1", "/tmp/work", parent.Title, ""); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	m = drive(t, m, refreshMsg{})
	m = stepR(t, m) // picker open, sel = 0 (+ new session)

	m = drive(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	a := m.(app)
	if a.sessionPicker.open {
		t.Error("picker still open after choosing + new session")
	}
	if a.promptKind != promptSessionCwd {
		t.Fatalf("promptKind = %v, want promptSessionCwd", a.promptKind)
	}
	if a.prompt.Value() != "/tmp/work" {
		t.Errorf("prompt prefilled with %q, want the last session's cwd /tmp/work", a.prompt.Value())
	}
}

func TestSessionPickerDigitResumesAndClosesPicker(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	parent, err := s.AddTask(ctx, "ongoing work")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	if _, err := s.CreateSession(ctx, parent.ID, "ext-1", "/tmp/work", parent.Title, ""); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	m = drive(t, m, refreshMsg{})
	m = stepR(t, m)

	// Step once so the resulting resume command (which shells out) isn't run.
	m2, cmd := m.Update(keyPress('1'))
	if m2.(app).sessionPicker.open {
		t.Error("picker still open after choosing a session by digit")
	}
	if cmd == nil {
		t.Error("choosing a session did not produce a resume command")
	}
}

func TestSessionPickerEscDismisses(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	parent, err := s.AddTask(ctx, "ongoing work")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	if _, err := s.CreateSession(ctx, parent.ID, "ext-1", "/tmp/work", parent.Title, ""); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	m = drive(t, m, refreshMsg{})
	m = stepR(t, m)

	m = drive(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.(app).sessionPicker.open {
		t.Error("picker still open after esc")
	}
}

// pickerWithSessions opens the picker on a task holding one session per
// label, created in order so ListSessionsForTask (last_active_at DESC, id
// DESC) returns them last label first.
func pickerWithSessions(t *testing.T, labels ...string) tea.Model {
	t.Helper()
	ctx := context.Background()
	m, s := newTestApp(t)
	parent, err := s.AddTask(ctx, "ongoing work")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	for i, l := range labels {
		if _, err := s.CreateSession(ctx, parent.ID, "ext-"+strconv.Itoa(i), "/tmp/work", l, ""); err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
	}
	m = drive(t, m, refreshMsg{})
	return stepR(t, m)
}

// sessionLabels returns n distinct labels, s-01 .. s-n, none a substring
// of another so a view assertion on one cannot match a neighbour.
func sessionLabels(n int) []string {
	labels := make([]string, n)
	for i := range labels {
		labels[i] = fmt.Sprintf("s-%02d", i+1)
	}
	return labels
}

// Twenty-five sessions used to render as twenty-five rows and push the
// box off the top of the screen. The picker now shows pickerMaxRows
// at a time, newest first, and says how many are scrolled off.
func TestSessionPickerClampsToMaxRows(t *testing.T) {
	m := pickerWithSessions(t, sessionLabels(25)...)
	content := ansi.Strip(m.View().Content)
	for _, want := range []string{"s-25", "s-16", "↓ 15 more"} {
		if !strings.Contains(content, want) {
			t.Errorf("picker missing %q:\n%s", want, content)
		}
	}
	for _, dont := range []string{"s-15", "s-01", "↑"} {
		if strings.Contains(content, dont) {
			t.Errorf("picker shows %q, which should be scrolled off:\n%s", dont, content)
		}
	}
	if got := strings.Count(content, "\n"); got > 29 {
		t.Errorf("view is %d lines tall, want it to fit the 30-row terminal", got+1)
	}
}

// Moving the highlight past the last visible row scrolls the window by
// one, so the highlight is always on screen; the digit shortcuts renumber
// with it.
func TestSessionPickerScrollsWithTheHighlight(t *testing.T) {
	m := pickerWithSessions(t, sessionLabels(25)...)
	for range 11 { // 0 → 11: the eleventh session, one past the window
		m = drive(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	}
	a := m.(app)
	if a.sessionPicker.sel != 11 || a.sessionPicker.top != 1 {
		t.Fatalf("sel, top = %d, %d; want 11, 1", a.sessionPicker.sel, a.sessionPicker.top)
	}
	content := ansi.Strip(m.View().Content)
	for _, want := range []string{"s-24", "s-15", "↑ 1 more", "↓ 14 more"} {
		if !strings.Contains(content, want) {
			t.Errorf("picker missing %q after scrolling:\n%s", want, content)
		}
	}
	if strings.Contains(content, "s-25") {
		t.Errorf("newest session still shown after the window scrolled past it:\n%s", content)
	}

	// Back up to the top row pulls the window back too.
	for range 11 {
		m = drive(t, m, tea.KeyPressMsg{Code: tea.KeyUp})
	}
	a = m.(app)
	if a.sessionPicker.sel != 0 || a.sessionPicker.top != 0 {
		t.Errorf("sel, top = %d, %d after moving back up; want 0, 0", a.sessionPicker.sel, a.sessionPicker.top)
	}
}

// A digit picks the row carrying that number in the window, not the n-th
// session overall, so what the user reads is what they get.
func TestSessionPickerDigitPicksVisibleRow(t *testing.T) {
	m := pickerWithSessions(t, sessionLabels(25)...)
	for range 11 {
		m = drive(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	}
	a := m.(app)
	rows := a.sessionPicker.matches()
	if got := rows[a.sessionPicker.top].Label; got != "s-24" {
		t.Fatalf("first visible row is %q, want s-24", got)
	}
	m2, cmd := m.Update(keyPress('1'))
	if m2.(app).sessionPicker.open {
		t.Error("picker still open after choosing a session by digit")
	}
	if cmd == nil {
		t.Error("choosing a visible row by digit did not produce a resume command")
	}
}

// A short terminal shows fewer rows still, so the picker's chrome (title,
// filter, "+ new session", the overflow row) never leaves the screen.
func TestSessionPickerFitsAShortTerminal(t *testing.T) {
	m := pickerWithSessions(t, sessionLabels(25)...)
	m = drive(t, m, tea.WindowSizeMsg{Width: 100, Height: 14})
	content := ansi.Strip(m.View().Content)
	for _, want := range []string{"claude sessions", "+ new session", "s-25", "s-21", "↓ 20 more"} {
		if !strings.Contains(content, want) {
			t.Errorf("picker missing %q on a 14-row terminal:\n%s", want, content)
		}
	}
	if strings.Contains(content, "s-20") {
		t.Errorf("sixth session shown on a terminal with room for five:\n%s", content)
	}
}

// Typing narrows the list to the sessions whose label matches, and enter
// resumes the highlighted match rather than the first session overall.
func TestSessionPickerTypeToFilter(t *testing.T) {
	m := pickerWithSessions(t, "alpha repo", "beta repo", "gamma repo")
	for _, r := range "bta" { // a subsequence, not a substring
		m = drive(t, m, keyPress(r))
	}
	a := m.(app)
	if a.sessionPicker.query != "bta" {
		t.Fatalf("query = %q, want bta", a.sessionPicker.query)
	}
	rows := a.sessionPicker.matches()
	if len(rows) != 1 || rows[0].Label != "beta repo" {
		t.Fatalf("matches = %+v, want just beta repo", rows)
	}
	content := ansi.Strip(m.View().Content)
	if !strings.Contains(content, "beta repo") || strings.Contains(content, "alpha repo") {
		t.Errorf("filtered picker should show beta and not alpha:\n%s", content)
	}

	m = drive(t, m, tea.KeyPressMsg{Code: tea.KeyDown}) // onto the one match
	m2, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m2.(app).sessionPicker.open {
		t.Error("picker still open after enter on a filtered match")
	}
	if cmd == nil {
		t.Error("enter on a filtered match did not produce a resume command")
	}
}

// A filter nothing matches says so, keeps "+ new session" reachable, and
// backspace brings the list back.
func TestSessionPickerNoMatchThenBackspace(t *testing.T) {
	m := pickerWithSessions(t, "alpha repo", "beta repo")
	for _, r := range "zzz" {
		m = drive(t, m, keyPress(r))
	}
	content := ansi.Strip(m.View().Content)
	if !strings.Contains(content, "no matching sessions") || !strings.Contains(content, "+ new session") {
		t.Errorf("empty filter result should say so and keep the new row:\n%s", content)
	}
	for range 3 {
		m = drive(t, m, tea.KeyPressMsg{Code: tea.KeyBackspace})
	}
	a := m.(app)
	if a.sessionPicker.query != "" || len(a.sessionPicker.matches()) != 2 {
		t.Errorf("query = %q with %d matches after backspacing; want the full list back",
			a.sessionPicker.query, len(a.sessionPicker.matches()))
	}
}

// The launch half: launchSessionCmd writes the session row *before* it
// hands the terminal over, with status starting, so the session's own
// hooks land on a row from its first turn. The Cmd is run by hand and
// stopped at the message it yields — the exec itself is bubbletea's to
// perform and never happens here — and the store is inspected in between,
// exactly the window a SessionStart/Stop hook fires in.
func TestLaunchSessionCmdWritesStartingRowBeforeHandoff(t *testing.T) {
	stubClaudeInstalled(t)
	isolateLaunchFiles(t)
	ctx := context.Background()
	m, s := newTestApp(t)
	parent, err := s.AddTask(ctx, "ongoing work")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}

	msg := m.(app).launchSessionCmd(parent.ID, "/tmp/new-work", parent.Title)()
	if e, ok := msg.(errMsg); ok {
		t.Fatalf("launch produced an error instead of the handoff: %v", e.err)
	}
	if msg == nil {
		t.Fatal("launch produced no message; want the exec handoff")
	}

	sessions, err := s.ListSessionsForTask(ctx, parent.ID)
	if err != nil {
		t.Fatalf("ListSessionsForTask: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("sessions = %+v, want the row written ahead of the handoff", sessions)
	}
	sess := sessions[0]
	if sess.Cwd != "/tmp/new-work" || sess.Label != parent.Title || sess.ExternalID == "" {
		t.Errorf("session = %+v, want cwd, label and a pinned external id recorded", sess)
	}
	if sess.Status != task.SessionStarting {
		t.Errorf("Status = %q, want %q right after launch", sess.Status, task.SessionStarting)
	}
	if sess.StatusUpdatedAt.IsZero() {
		t.Error("StatusUpdatedAt is zero; want the settle floor to count from launch")
	}
	// tmux_session is what the row will be attached/polled by, so it has
	// to match what the launch actually wrapped claude in.
	if agent.TmuxInstalled() && sess.TmuxSession != agent.SessionName(sess.ExternalID) {
		t.Errorf("TmuxSession = %q, want %q", sess.TmuxSession, agent.SessionName(sess.ExternalID))
	}
	if !agent.TmuxInstalled() && sess.TmuxSession != "" {
		t.Errorf("TmuxSession = %q, want empty without tmux", sess.TmuxSession)
	}

	// The acceptance case: the first turn's Stop hook now finds a row,
	// and the session reads idle without ever having been resumed.
	if err := s.SetSessionStatus(ctx, sess.ExternalID, task.SessionIdle); err != nil {
		t.Fatalf("SetSessionStatus: %v", err)
	}
	sessions, err = s.ListSessionsForTask(ctx, parent.ID)
	if err != nil {
		t.Fatalf("ListSessionsForTask: %v", err)
	}
	if sessions[0].Status != task.SessionIdle {
		t.Errorf("Status after the first Stop hook = %q, want %q", sessions[0].Status, task.SessionIdle)
	}
}

// A launch that never gets as far as the handoff — claude missing —
// writes nothing: the row is only worth having once the process is about
// to start.
func TestLaunchSessionCmdWithoutClaudeWritesNoRow(t *testing.T) {
	prev := checkInstalled
	checkInstalled = func() error { return context.DeadlineExceeded }
	t.Cleanup(func() { checkInstalled = prev })
	ctx := context.Background()
	m, s := newTestApp(t)
	parent, err := s.AddTask(ctx, "ongoing work")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}

	msg := m.(app).launchSessionCmd(parent.ID, "/tmp/new-work", parent.Title)()
	if _, ok := msg.(errMsg); !ok {
		t.Fatalf("launch produced %T, want errMsg", msg)
	}
	sessions, err := s.ListSessionsForTask(ctx, parent.ID)
	if err != nil {
		t.Fatalf("ListSessionsForTask: %v", err)
	}
	if len(sessions) != 0 {
		t.Errorf("sessions = %+v, want none for a launch that never started", sessions)
	}
}

// The return half of a clean exit keeps the launch-time row (there is no
// second write of it) and reports the session recorded.
func TestSessionFinishedMsgKeepsLaunchRow(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	parent, err := s.AddTask(ctx, "ongoing work")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	sess := launched(t, s, parent, "ext-new", "/tmp/new-work", "")
	m = drive(t, m, refreshMsg{})

	m = drive(t, m, finished(sess))

	waitFor(t, "session recorded", func() bool {
		return m.(app).status.text == "session recorded"
	})
	sessions, err := s.ListSessionsForTask(ctx, parent.ID)
	if err != nil {
		t.Fatalf("ListSessionsForTask: %v", err)
	}
	if len(sessions) != 1 || sessions[0].ID != sess.ID || sessions[0].ExternalID != "ext-new" {
		t.Errorf("sessions = %+v, want exactly the launch-time row", sessions)
	}
}

// A handoff that returns an error was never a session: the row written
// ahead of it is taken back so it does not linger as a phantom in the
// SESSIONS section or as a poller candidate.
func TestSessionFinishedMsgErrDeletesLaunchRow(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	parent, err := s.AddTask(ctx, "ongoing work")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	sess := launched(t, s, parent, "ext-fail", "/tmp/x", "tend-ext-fail")
	m = drive(t, m, refreshMsg{})

	msg := finished(sess)
	msg.err = context.DeadlineExceeded
	m = drive(t, m, msg)
	if !m.(app).status.isErr {
		t.Error("expected an error flash after a failed session")
	}
	waitFor(t, "launch row deleted", func() bool {
		sessions, err := s.ListSessionsForTask(ctx, parent.ID)
		return err == nil && len(sessions) == 0
	})
}

func TestSessionResumedMsgTouchesSession(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	parent, err := s.AddTask(ctx, "ongoing work")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	sess, err := s.CreateSession(ctx, parent.ID, "ext-1", "/tmp/work", parent.Title, "")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	m = drive(t, m, refreshMsg{})

	m = drive(t, m, sessionResumedMsg{
		sessionRowID: sess.ID,
		taskID:       parent.ID,
		cwd:          sess.Cwd,
		externalID:   sess.ExternalID,
	})

	waitFor(t, "session touched", func() bool {
		sessions, err := s.ListSessionsForTask(ctx, parent.ID)
		return err == nil && len(sessions) == 1
	})
	if !strings.Contains(m.(app).status.text, "resumed") {
		t.Errorf("status = %q, want it to mention resume", m.(app).status.text)
	}
}

func TestDetailPaneShowsSessions(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	parent, err := s.AddTask(ctx, "ongoing work")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	if _, err := s.CreateSession(ctx, parent.ID, "ext-1", "/home/me/code/my-project", "fixed the flaky test", ""); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	m = drive(t, m, refreshMsg{})
	m = drive(t, m, keyPress(']')) // open detail

	content := ansi.Strip(m.View().Content)
	for _, want := range []string{"SESSIONS  1", "fixed the flaky test"} {
		if !strings.Contains(content, want) {
			t.Errorf("detail pane missing %q:\n%s", want, content)
		}
	}
}

// The brief an interactive session is launched with comes from the store,
// not the loaded view state, and carries every relation the task has:
// project, tags, parent, sub-tasks with their blocked marker, both
// dependency directions and the log. A task the store cannot read yields
// no block at all rather than a wrong one.
func TestSessionBriefPromptGathersTaskContext(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	a := m.(app)

	parent, err := s.AddTask(ctx, "epic")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	tk, err := s.AddChild(ctx, parent.ID, "ship the thing")
	if err != nil {
		t.Fatalf("AddChild: %v", err)
	}
	if err := s.SetBody(ctx, tk.ID, "## Plan\n\nCarefully."); err != nil {
		t.Fatalf("SetBody: %v", err)
	}
	if err := s.SetState(ctx, tk.ID, task.StateDoing); err != nil {
		t.Fatalf("SetState: %v", err)
	}
	if err := s.SetTags(ctx, tk.ID, []string{"cli"}); err != nil {
		t.Fatalf("SetTags: %v", err)
	}
	child, err := s.AddChild(ctx, tk.ID, "sub step")
	if err != nil {
		t.Fatalf("AddChild: %v", err)
	}
	prereq, err := s.AddTask(ctx, "prereq")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	if err := s.AddDependency(ctx, tk.ID, prereq.ID); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}
	if err := s.AddDependency(ctx, child.ID, prereq.ID); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}
	after, err := s.AddTask(ctx, "follow-up")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	if err := s.AddDependency(ctx, after.ID, tk.ID); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}
	if _, err := s.AddLogEntry(ctx, &tk.ID, "Wired the flag."); err != nil {
		t.Fatalf("AddLogEntry: %v", err)
	}

	got := a.sessionBriefPrompt(tk.ID)
	for _, want := range []string{
		"## Task #" + itoa(tk.ID) + ": ship the thing",
		"- State: doing",
		"- Project: Unsorted",
		"- Tags: cli",
		"- Parent task: #" + itoa(parent.ID) + ` "epic" (inbox)`,
		"### Sub-tasks (0 of 1 done)",
		`- [ ] #` + itoa(child.ID) + ` "sub step" (inbox), waiting on 1 open task(s)`,
		"### Blocked by (1 open)",
		`- [ ] #` + itoa(prereq.ID) + ` "prereq" (inbox)`,
		"### Blocks",
		`- #` + itoa(after.ID) + ` "follow-up" (inbox)`,
		"### Description\n\n## Plan\n\nCarefully.\n",
		"### Log (newest first)",
		": Wired the flag.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("brief missing %q\n---\n%s", want, got)
		}
	}

	if got := a.sessionBriefPrompt(999999); got != "" {
		t.Errorf("brief for a missing task = %q, want empty", got)
	}
}

// A command tmux would refuse as too long is run directly instead of
// wrapped: no tmux name, the inner command handed back as is. That is the
// guard behind the brief's move to a file; with the brief inline, a task
// with a long body used to die at launch with tmux's "command too long".
// Holds with or without tmux installed, since both paths return no name.
func TestWrapInTmuxRunsDirectWhenCommandTooLong(t *testing.T) {
	brief := strings.Repeat("a body line the brief used to carry inline\n", 500)
	inner := agent.LaunchCmdWith("/tmp/work", "abc-123", "fix the bug", "", "", agent.LaunchOpts{AppendSystemPrompt: brief})
	wrapped, name, confPath := wrapInTmux(inner, "abc-123")
	if wrapped != inner {
		t.Errorf("oversized command was wrapped: %v", wrapped.Args[:min(len(wrapped.Args), 8)])
	}
	if name != "" || confPath != "" {
		t.Errorf("oversized command got a tmux name %q / conf %q, want none", name, confPath)
	}

	// The same brief by file is small enough to wrap wherever tmux is.
	byFile := agent.LaunchCmdWith("/tmp/work", "abc-123", "fix the bug", "", "", agent.LaunchOpts{AppendSystemPromptFile: "/tmp/tend-system-prompt-1.md"})
	wrapped, name, _ = wrapInTmux(byFile, "abc-123")
	if agent.TmuxInstalled() && name == "" {
		t.Errorf("a by-file launch should be wrapped when tmux is installed: %v", wrapped.Args)
	}
	if !agent.TmuxInstalled() && wrapped != byFile {
		t.Errorf("without tmux the command should come back as is")
	}
}

// itoa renders a task id the way the brief does.
func itoa(v int64) string { return strconv.FormatInt(v, 10) }
