package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/jwstover/tend/internal/task"
)

func ptr(n int64) *int64 { return &n }

// sectionTitles flattens sections to "label: title, title" lines so a test
// can assert order and membership in one comparison.
func sectionTitles(sections []section) []string {
	out := make([]string, 0, len(sections))
	for _, s := range sections {
		titles := make([]string, 0, len(s.tasks))
		for _, t := range s.tasks {
			titles = append(titles, t.Title)
		}
		out = append(out, s.label+": "+strings.Join(titles, ", "))
	}
	return out
}

func TestGroupTasksByPriority(t *testing.T) {
	tasks := []task.Task{
		{ID: 1, Title: "b first", Priority: ptr(2)},
		{ID: 2, Title: "unset"},
		{ID: 3, Title: "a", Priority: ptr(1)},
		{ID: 4, Title: "b second", Priority: ptr(2)},
		{ID: 5, Title: "child", Priority: ptr(1), ParentID: ptr(3)},
		{ID: 6, Title: "out of range", Priority: ptr(9)},
	}
	got := sectionTitles(groupTasks(groupByPriority, tasks, nil, DefaultStyles()))
	want := []string{
		"priority A: a",
		"priority B: b first, b second",
		"no priority: unset, out of range",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("sections:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	for _, s := range groupTasks(groupByPriority, tasks, nil, DefaultStyles()) {
		if s.count != len(s.tasks) {
			t.Errorf("%s: count %d, want %d", s.label, s.count, len(s.tasks))
		}
	}
}

func TestGroupTasksByAgent(t *testing.T) {
	tasks := []task.Task{
		{ID: 1, Title: "never launched"},
		{ID: 2, Title: "idle one"},
		{ID: 3, Title: "waiting"},
		{ID: 4, Title: "busy"},
		{ID: 5, Title: "unknown status"},
		{ID: 6, Title: "finished"},
		{ID: 7, Title: "odd status"},
	}
	sessions := map[int64]task.SessionStatus{
		2: task.SessionIdle,
		3: task.SessionBlocked,
		4: task.SessionWorking,
		5: task.SessionUnknown,
		6: task.SessionEnded,
		7: task.SessionStatus("weird"),
	}
	got := sectionTitles(groupTasks(groupByAgent, tasks, sessions, DefaultStyles()))
	want := []string{
		"session blocked: waiting",
		"session working: busy",
		"session idle: idle one",
		"session ended: finished",
		"session weird: odd status",
		"no session: never launched, unknown status",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("sections:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	// With no status map at all every task is "no session" — the shape the
	// list has when SessionStatuses failed and degraded to nil.
	got = sectionTitles(groupTasks(groupByAgent, tasks, nil, DefaultStyles()))
	if len(got) != 1 || !strings.HasPrefix(got[0], "no session: ") {
		t.Errorf("nil sessions: %v, want one no-session section", got)
	}
}

// Every heading, whatever produced it, keeps its glyph in the column the
// row state dots use, and its label is what renders.
func TestGroupHeadingsRenderLabel(t *testing.T) {
	styles := DefaultStyles()
	d := taskDelegate{styles: styles}
	tasks := []task.Task{{ID: 1, Title: "x", State: task.StateTodo, Priority: ptr(3)}}
	row := ansi.Strip(d.renderRow(listItem{t: tasks[0]}, false, 80))
	dotCol := runeIndex(row, styles.Glyphs.State[task.StateTodo])

	for _, g := range []groupBy{groupByState, groupByPriority, groupByAgent} {
		for _, s := range groupTasks(g, tasks, nil, styles) {
			heading := ansi.Strip(d.renderHeading(s.sectionItem, 80))
			if !strings.Contains(heading, s.label) {
				t.Errorf("%s heading %q lacks label %q", g, heading, s.label)
			}
			if got := runeIndex(heading, s.glyph); got != dotCol {
				t.Errorf("%s heading glyph at column %d, want %d:\n%q", g, got, dotCol, heading)
			}
			if !strings.HasSuffix(heading, " 1") {
				t.Errorf("%s heading %q should end with its count", g, heading)
			}
		}
	}
}

func TestGroupChordRegroupsList(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)

	add := func(title string, p *int64) int64 {
		t.Helper()
		tk, err := s.AddTask(ctx, title)
		if err != nil {
			t.Fatalf("AddTask: %v", err)
		}
		if err := s.SetState(ctx, tk.ID, task.StateTodo); err != nil {
			t.Fatalf("SetState: %v", err)
		}
		if err := s.SetPriority(ctx, tk.ID, p); err != nil {
			t.Fatalf("SetPriority: %v", err)
		}
		return tk.ID
	}
	add("low", ptr(3))
	add("none", nil)
	highID := add("high", ptr(1))

	m = drive(t, m, refreshMsg{})
	content := ansi.Strip(m.View().Content)
	if strings.Contains(content, "priority A") {
		t.Fatalf("priority headings before regrouping:\n%s", content)
	}

	// Park the cursor on "high", then regroup: the cursor follows the task.
	var a app
	for i := 0; i < 5; i++ {
		a = m.(app)
		if sel, ok := a.selected(); ok && sel.ID == highID {
			break
		}
		m = drive(t, m, keyPress('j'))
	}
	if sel, ok := m.(app).selected(); !ok || sel.ID != highID {
		t.Fatalf("could not park the cursor on high: %+v", sel)
	}

	m = drive(t, m, keyPress('g'))
	if !m.(app).groupPending {
		t.Fatal("g should open the grouping chord")
	}
	if !strings.Contains(ansi.Strip(m.View().Content), "group by") {
		t.Errorf("chord panel not shown:\n%s", ansi.Strip(m.View().Content))
	}
	m = drive(t, m, keyPress('p'))
	a = m.(app)
	if a.groupPending {
		t.Error("p should close the chord")
	}
	if a.groupBy != groupByPriority {
		t.Errorf("groupBy = %v, want priority", a.groupBy)
	}
	content = ansi.Strip(m.View().Content)
	hi, lo, no := strings.Index(content, "priority A"), strings.Index(content, "priority C"), strings.Index(content, "no priority")
	if hi == -1 || lo == -1 || no == -1 {
		t.Fatalf("missing priority headings:\n%s", content)
	}
	if hi >= lo || lo >= no {
		t.Errorf("headings out of order (A=%d C=%d none=%d):\n%s", hi, lo, no, content)
	}
	if strings.Contains(content, "○ todo") {
		t.Errorf("state heading still present under priority grouping:\n%s", content)
	}
	if !strings.Contains(content, "by priority") {
		t.Errorf("header should name the grouping:\n%s", content)
	}
	if sel, ok := a.selected(); !ok || sel.ID != highID {
		t.Errorf("selection after regroup = %+v, want high (#%d)", sel, highID)
	}

	// Back to state grouping.
	m = drive(t, m, keyPress('g'))
	m = drive(t, m, keyPress('s'))
	content = ansi.Strip(m.View().Content)
	if !strings.Contains(content, "○ todo") || strings.Contains(content, "priority A") {
		t.Errorf("gs should restore state grouping:\n%s", content)
	}
	if header := strings.SplitN(content, "\n", 2)[0]; strings.Contains(header, "by state") {
		t.Errorf("default grouping should not be announced in the header:\n%s", header)
	}
}

func TestGroupChordGGJumpsToTop(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	for _, title := range []string{"first", "second", "third"} {
		if _, err := s.AddTask(ctx, title); err != nil {
			t.Fatalf("AddTask: %v", err)
		}
	}
	m = drive(t, m, refreshMsg{})
	m = drive(t, m, keyPress('j'))
	m = drive(t, m, keyPress('j'))
	if sel, ok := m.(app).selected(); !ok || sel.Title != "third" {
		t.Fatalf("selection after jj = %+v, want third", sel)
	}
	m = drive(t, m, keyPress('g'))
	m = drive(t, m, keyPress('g'))
	a := m.(app)
	if a.groupPending {
		t.Error("second g should close the chord")
	}
	if sel, ok := a.selected(); !ok || sel.Title != "first" {
		t.Errorf("selection after gg = %+v, want first", sel)
	}
	if a.groupBy != groupByState {
		t.Errorf("gg changed the grouping to %v", a.groupBy)
	}
}

func TestGroupChordOtherKeyCancels(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	if _, err := s.AddTask(ctx, "only"); err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	m = drive(t, m, refreshMsg{})
	m = drive(t, m, keyPress('g'))
	m = drive(t, m, keyPress('z'))
	a := m.(app)
	if a.groupPending || a.groupBy != groupByState {
		t.Errorf("z should cancel the chord unchanged: pending=%v groupBy=%v", a.groupPending, a.groupBy)
	}
}

// C keeps working under a non-default grouping: the completed tasks it
// loads slot into the priority buckets rather than needing a done section.
func TestToggleCompletedUnderPriorityGrouping(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)

	done, err := s.AddTask(ctx, "finished thing")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	if err := s.SetPriority(ctx, done.ID, ptr(1)); err != nil {
		t.Fatalf("SetPriority: %v", err)
	}
	if err := s.SetState(ctx, done.ID, task.StateDone); err != nil {
		t.Fatalf("SetState: %v", err)
	}
	if _, err := s.AddTask(ctx, "pending thing"); err != nil {
		t.Fatalf("AddTask: %v", err)
	}

	m = drive(t, m, refreshMsg{})
	m = drive(t, m, keyPress('g'))
	m = drive(t, m, keyPress('p'))
	content := ansi.Strip(m.View().Content)
	if strings.Contains(content, "finished thing") || strings.Contains(content, "priority A") {
		t.Fatalf("completed task should be hidden by default:\n%s", content)
	}

	m = drive(t, m, keyPress('C'))
	a := m.(app)
	content = ansi.Strip(m.View().Content)
	if a.groupBy != groupByPriority {
		t.Errorf("C reset the grouping to %v", a.groupBy)
	}
	if !strings.Contains(content, "finished thing") || !strings.Contains(content, "priority A") {
		t.Errorf("completed task should appear under its priority after C:\n%s", content)
	}
	if !strings.Contains(content, "pending thing") || !strings.Contains(content, "no priority") {
		t.Errorf("live task should still show under no priority:\n%s", content)
	}

	m = drive(t, m, keyPress('C'))
	content = ansi.Strip(m.View().Content)
	if strings.Contains(content, "finished thing") {
		t.Errorf("completed task should hide after second C:\n%s", content)
	}
	if m.(app).groupBy != groupByPriority {
		t.Errorf("second C reset the grouping")
	}
}
