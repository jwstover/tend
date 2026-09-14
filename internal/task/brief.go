package task

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// SessionBrief is everything a Claude Code session launched from a task
// should know about it up front: the task itself, where it sits (project,
// parent, sub-tasks), what it waits on and what waits on it, and the
// notes and recaps logged against it. The TUI assembles it from the store
// right before a launch or resume and renders it with SessionSystemPrompt
// into claude's --append-system-prompt, so a session starts with the
// task's context instead of the user having to open with "review the
// current tend task".
//
// Every field but Task is optional: a brief with nothing else renders the
// task alone. Children and Log come in the order the store lists them
// (sub-tasks oldest first, log newest first). ChildBlockers is the batch
// blocker map (Store.BlockerCounts) so a sub-task can be marked as still
// waiting on something without a query per child.
type SessionBrief struct {
	Task          Task
	Project       Project
	Tags          []string
	Parent        *Task
	Children      []Task
	ChildBlockers map[int64]BlockerCount
	Blockers      []Task // the tasks this one waits on, open or done
	Blocking      []Task // the tasks waiting on this one
	Log           []LogEntry
}

// briefLogLimit caps how many log entries the brief carries. The log is
// newest first, so the cap keeps the recent recaps -- the ones that say
// where the work stands -- and drops the deep history, which a session
// that needs it can still read over MCP.
const briefLogLimit = 10

// briefBodyLimit caps the bytes of the task body the brief carries. The
// body is the one unbounded part of a brief: a task that has logged a few
// sessions' worth of implementation notes and reviews runs to tens or
// hundreds of KB, and the brief is sent as system prompt on every request
// of the session, so an uncapped body would spend a large slice of the
// context window on every turn (or, past the window, break the session
// outright). 48KB is roughly 12k tokens: room for a long description and
// several appended logs, small next to the window. The cut keeps both
// ends of the body -- the original description at the head and the most
// recent appended notes at the tail -- and drops the middle, saying so and
// where to read the whole thing (mcp__tend__get_current_task).
const briefBodyLimit = 48 << 10

// SessionSystemPrompt renders a brief as the system prompt block an
// interactive session gets (agent.LaunchOpts.AppendSystemPrompt). It says
// what the session is bound to, that the details are a launch-time
// snapshot the tend MCP tools can refresh and change, and then lays the
// task out as markdown: a facts list, the parent, the sub-tasks with
// their states, both dependency directions, the body verbatim, and the
// recent log. Sections with nothing in them are left out rather than
// rendered empty, so a bare task reads as a short block. Pure text
// assembly, no I/O, like StandupMarkdown.
func SessionSystemPrompt(b SessionBrief) string {
	t := b.Task
	var sb strings.Builder

	fmt.Fprintf(&sb, "This Claude Code session was launched from tend, a terminal task tracker, and is bound to tend task #%d: %q. ", t.ID, t.Title)
	sb.WriteString("The task is laid out below; treat it as the context for this session and do not ask the user to restate it. ")
	sb.WriteString("The details are a snapshot from when the session started. The tend MCP tools (mcp__tend__get_current_task, mcp__tend__list_subtasks, ")
	sb.WriteString("mcp__tend__append_task_body, mcp__tend__set_task_state, mcp__tend__create_subtask, ...) read and change the live task; ")
	sb.WriteString("record progress and decisions by appending to the task body, since log entries are the user's.\n\n")

	fmt.Fprintf(&sb, "## Task #%d: %s\n\n", t.ID, t.Title)
	fmt.Fprintf(&sb, "- State: %s\n", t.State.Label())
	if b.Project.Name != "" {
		if b.Project.Cwd != "" {
			fmt.Fprintf(&sb, "- Project: %s (default cwd %s)\n", b.Project.Name, b.Project.Cwd)
		} else {
			fmt.Fprintf(&sb, "- Project: %s\n", b.Project.Name)
		}
	}
	if p := PriorityLetter(t.Priority); p != "" {
		fmt.Fprintf(&sb, "- Priority: %s\n", p)
	}
	if t.Due != nil && *t.Due != "" {
		fmt.Fprintf(&sb, "- Due: %s\n", *t.Due)
	}
	if t.SnoozeUntil != nil && *t.SnoozeUntil != "" {
		fmt.Fprintf(&sb, "- Snoozed until: %s\n", *t.SnoozeUntil)
	}
	if len(b.Tags) > 0 {
		fmt.Fprintf(&sb, "- Tags: %s\n", strings.Join(b.Tags, ", "))
	}
	if !t.CreatedAt.IsZero() {
		fmt.Fprintf(&sb, "- Created: %s\n", t.CreatedAt.Local().Format("2006-01-02"))
	}
	if !t.UpdatedAt.IsZero() {
		fmt.Fprintf(&sb, "- Updated: %s\n", t.UpdatedAt.Local().Format("2006-01-02"))
	}
	if t.CompletedAt != nil {
		fmt.Fprintf(&sb, "- Completed: %s\n", t.CompletedAt.Local().Format("2006-01-02"))
	}
	switch {
	case b.Parent != nil:
		fmt.Fprintf(&sb, "- Parent task: %s\n", briefRef(*b.Parent))
	case t.ParentID != nil:
		fmt.Fprintf(&sb, "- Parent task: #%d\n", *t.ParentID)
	default:
		sb.WriteString("- Parent task: none (a top-level task)\n")
	}

	if len(b.Children) > 0 {
		done := 0
		for _, c := range b.Children {
			if c.State == StateDone {
				done++
			}
		}
		fmt.Fprintf(&sb, "\n### Sub-tasks (%d of %d done)\n\n", done, len(b.Children))
		for _, c := range b.Children {
			box := "[ ]"
			if c.State == StateDone {
				box = "[x]"
			}
			line := fmt.Sprintf("- %s %s", box, briefRef(c))
			if bc, ok := b.ChildBlockers[c.ID]; ok && bc.Blocked() {
				line += fmt.Sprintf(", waiting on %d open task(s)", bc.Open)
			}
			sb.WriteString(line + "\n")
		}
	}

	if len(b.Blockers) > 0 {
		open := len(OpenBlockers(b.Blockers))
		fmt.Fprintf(&sb, "\n### Blocked by (%d open)\n\n", open)
		for _, d := range b.Blockers {
			box := "[ ]"
			if d.State == StateDone {
				box = "[x]"
			}
			fmt.Fprintf(&sb, "- %s %s\n", box, briefRef(d))
		}
	}

	if len(b.Blocking) > 0 {
		sb.WriteString("\n### Blocks\n\n")
		for _, d := range b.Blocking {
			fmt.Fprintf(&sb, "- %s\n", briefRef(d))
		}
	}

	body := strings.TrimSpace(t.BodyMD)
	switch {
	case body == "":
		sb.WriteString("\n### Description\n\n(no description)\n")
	case len(body) > briefBodyLimit:
		fmt.Fprintf(&sb, "\n### Description (abridged: %d of %d bytes; the full body is one mcp__tend__get_current_task call away)\n\n", briefBodyLimit, len(body))
		sb.WriteString(abridgeBody(body, briefBodyLimit) + "\n")
	default:
		sb.WriteString("\n### Description\n\n" + body + "\n")
	}

	if len(b.Log) > 0 {
		shown := b.Log
		if len(shown) > briefLogLimit {
			shown = shown[:briefLogLimit]
			fmt.Fprintf(&sb, "\n### Log (newest first; the %d most recent of %d entries)\n\n", briefLogLimit, len(b.Log))
		} else {
			sb.WriteString("\n### Log (newest first)\n\n")
		}
		for _, n := range shown {
			fmt.Fprintf(&sb, "- %s: %s\n", n.CreatedAt.Local().Format("2006-01-02 15:04"), strings.TrimSpace(n.Body))
		}
	}

	return sb.String()
}

// abridgeBody cuts a body longer than limit bytes down to its first and
// last halves of the budget with a marker between them saying how much was
// left out. Both cuts land on line boundaries where a newline falls within
// the budget, so no markdown line is split mid-way, and on a rune
// boundary otherwise, so the result is always valid UTF-8. The head keeps
// the description the task was written with; the tail keeps the notes
// appended most recently, which is where an in-progress task's current
// state lives.
func abridgeBody(body string, limit int) string {
	if len(body) <= limit {
		return body
	}
	half := limit / 2

	head := body[:half]
	if i := strings.LastIndexByte(head, '\n'); i > 0 {
		head = head[:i]
	} else {
		for len(head) > 0 && !utf8.RuneStart(body[len(head)]) {
			head = head[:len(head)-1]
		}
	}

	tail := body[len(body)-half:]
	if i := strings.IndexByte(tail, '\n'); i >= 0 {
		tail = tail[i+1:]
	} else {
		for len(tail) > 0 && !utf8.RuneStart(tail[0]) {
			tail = tail[1:]
		}
	}

	omitted := len(body) - len(head) - len(tail)
	return strings.TrimRight(head, "\n") +
		fmt.Sprintf("\n\n[... %d bytes of the description omitted here to keep the system prompt within budget; read the full body with mcp__tend__get_current_task ...]\n\n", omitted) +
		strings.TrimLeft(tail, "\n")
}

// briefRef renders a related task as `#12 "title" (state)`.
func briefRef(t Task) string {
	return fmt.Sprintf("#%d %q (%s)", t.ID, t.Title, t.State.Label())
}
