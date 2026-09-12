package task

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func i64(v int64) *int64   { return &v }
func str(v string) *string { return &v }

// The full brief lays every section out: facts, parent, sub-tasks with
// blocked markers, both dependency directions, the body, and the log.
func TestSessionSystemPromptFull(t *testing.T) {
	created := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	b := SessionBrief{
		Task: Task{
			ID: 42, Title: "Ship the thing", BodyMD: "## Plan\n\nDo it carefully.\n",
			State: StateDoing, ParentID: i64(7), ProjectID: 3, Priority: i64(1),
			Due: str("2026-09-20"), CreatedAt: created, UpdatedAt: created.Add(24 * time.Hour),
		},
		Project: Project{ID: 3, Name: "tend", Cwd: "/Users/me/code/tend"},
		Tags:    []string{"cli", "urgent"},
		Parent:  &Task{ID: 7, Title: "Epic", State: StateTodo},
		Children: []Task{
			{ID: 43, Title: "first", State: StateDone},
			{ID: 44, Title: "second", State: StateTodo},
		},
		ChildBlockers: map[int64]BlockerCount{44: {Open: 1, Total: 1}},
		Blockers: []Task{
			{ID: 10, Title: "prereq", State: StateTodo},
			{ID: 11, Title: "landed", State: StateDone},
		},
		Blocking: []Task{{ID: 50, Title: "follow-up", State: StateTodo}},
		Log: []LogEntry{
			{Body: "Wired the flag.", CreatedAt: created.Add(48 * time.Hour)},
		},
	}
	got := SessionSystemPrompt(b)

	for _, want := range []string{
		`bound to tend task #42: "Ship the thing"`,
		"mcp__tend__get_current_task",
		"## Task #42: Ship the thing",
		"- State: doing",
		"- Project: tend (default cwd /Users/me/code/tend)",
		"- Priority: A",
		"- Due: 2026-09-20",
		"- Tags: cli, urgent",
		"- Created: " + created.Local().Format("2006-01-02"),
		`- Parent task: #7 "Epic" (todo)`,
		"### Sub-tasks (1 of 2 done)",
		`- [x] #43 "first" (done)`,
		`- [ ] #44 "second" (todo), waiting on 1 open task(s)`,
		"### Blocked by (1 open)",
		`- [ ] #10 "prereq" (todo)`,
		`- [x] #11 "landed" (done)`,
		"### Blocks",
		`- #50 "follow-up" (todo)`,
		"### Description\n\n## Plan\n\nDo it carefully.\n",
		"### Log (newest first)",
		": Wired the flag.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt missing %q\n---\n%s", want, got)
		}
	}
}

// A bare task renders the facts and description only: no empty sub-task,
// dependency or log sections, and a parent line that says there is none.
func TestSessionSystemPromptBareTask(t *testing.T) {
	got := SessionSystemPrompt(SessionBrief{Task: Task{ID: 1, Title: "Lone", State: StateInbox}})

	for _, want := range []string{
		"## Task #1: Lone",
		"- State: inbox",
		"- Parent task: none (a top-level task)",
		"### Description\n\n(no description)\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt missing %q\n---\n%s", want, got)
		}
	}
	for _, absent := range []string{"### Sub-tasks", "### Blocked by", "### Blocks", "### Log", "- Project:", "- Priority:", "- Due:", "- Tags:", "- Created:"} {
		if strings.Contains(got, absent) {
			t.Errorf("prompt has %q for a bare task\n---\n%s", absent, got)
		}
	}
}

// review is the one state whose label differs from its stored name; the
// brief uses the label, as the UI does.
func TestSessionSystemPromptUsesStateLabel(t *testing.T) {
	got := SessionSystemPrompt(SessionBrief{Task: Task{ID: 1, Title: "x", State: StateReview}})
	if !strings.Contains(got, "- State: in review\n") {
		t.Errorf("prompt does not label review state\n---\n%s", got)
	}
}

// A parent id with no loaded parent still names the parent by id, so a
// failed parent lookup degrades to less detail rather than a wrong claim.
func TestSessionSystemPromptParentIDOnly(t *testing.T) {
	got := SessionSystemPrompt(SessionBrief{Task: Task{ID: 2, Title: "child", State: StateTodo, ParentID: i64(9)}})
	if !strings.Contains(got, "- Parent task: #9\n") {
		t.Errorf("prompt does not name the parent by id\n---\n%s", got)
	}
}

// The log is capped to the most recent entries, newest first as the store
// lists them, and the heading says how many were dropped.
func TestSessionSystemPromptCapsLog(t *testing.T) {
	var log []LogEntry
	for i := 0; i < briefLogLimit+5; i++ {
		log = append(log, LogEntry{Body: fmt.Sprintf("entry %d", i), CreatedAt: time.Now().Add(-time.Duration(i) * time.Hour)})
	}
	got := SessionSystemPrompt(SessionBrief{Task: Task{ID: 1, Title: "x", State: StateTodo}, Log: log})

	if want := fmt.Sprintf("### Log (newest first; the %d most recent of %d entries)", briefLogLimit, briefLogLimit+5); !strings.Contains(got, want) {
		t.Errorf("prompt missing capped heading %q\n---\n%s", want, got)
	}
	if !strings.Contains(got, ": entry 0\n") {
		t.Errorf("prompt dropped the newest entry\n---\n%s", got)
	}
	if strings.Contains(got, fmt.Sprintf(": entry %d\n", briefLogLimit)) {
		t.Errorf("prompt kept an entry past the cap\n---\n%s", got)
	}
}
