package workflow

import (
	"fmt"
	"strconv"
	"strings"
)

// The exact tool ids a step's session sees for the step tools registered
// by `tend mcp --step-run-id`: the MCP server is named "tend" in the
// per-session --mcp-config, and Claude Code exposes a server's tools as
// mcp__<server>__<tool>. Spelled out in full so a session whose tool list
// is deferred can load them by name rather than guess.
const (
	FinishStepTool      = "mcp__tend__finish_step"
	GetWorkflowStepTool = "mcp__tend__get_workflow_step"
)

// FallbackAllowed reports whether a step that exits without calling
// finish_step may still be settled from its final text as a "done"
// deliverable. That is safe only when done is the one outcome the step
// can have: no edges at all (the end of a linear workflow) or a single
// done edge. A step that routes anything else -- approve/reject, a
// verdict, a choice -- must hand off through finish_step, because a
// guessed "done" would silently take an edge the agent never chose, or
// end the run with the verdict lost.
func FallbackAllowed(outcomes []string) bool {
	switch len(outcomes) {
	case 0:
		return true
	case 1:
		return outcomes[0] == OutcomeDone
	}
	return false
}

// HandoffContext is what StepSystemPrompt needs to tell an agent where it
// is and how it must finish: the workflow and step names, the iteration,
// and the outcomes finish_step accepts (the step's edge outcomes, or
// just done when it has none -- the same set get_workflow_step reports).
type HandoffContext struct {
	Workflow  string
	Step      string
	Iteration int64
	Outcomes  []string
}

// StepSystemPrompt is the block the runner appends to claude's system
// prompt (--append-system-prompt) for every agent step, so the hand-off
// contract does not depend on each prompt_md author remembering to spell
// it out. It names the step, the run's dependence on finish_step, the
// exact tool ids, and the allowed outcomes; and it says the tool does not
// end the session, since a model that expects it to may stall waiting.
// Pure text assembly, no I/O, so it is testable and the block that ran
// can be recorded on the step run verbatim.
func StepSystemPrompt(h HandoffContext) string {
	outcomes := h.Outcomes
	if len(outcomes) == 0 {
		outcomes = []string{OutcomeDone}
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "You are executing step %q of the tend workflow %q (iteration %s) as a headless session driven by tend's workflow runner.\n\n",
		h.Step, h.Workflow, strconv.FormatInt(h.Iteration, 10))
	fmt.Fprintf(&sb, "The run cannot continue until you call the MCP tool %s with `outcome` set to one of: %s. ",
		FinishStepTool, quoteAll(outcomes))
	sb.WriteString("Pass what the next step should receive as `deliverable` (a PR link, a summary, a review verdict with its reasons). ")
	fmt.Fprintf(&sb, "Call %s first if you need this step's input, feedback, or outcomes.\n\n", GetWorkflowStepTool)
	sb.WriteString(deferredToolsHint)
	sb.WriteString("\n\n")
	fmt.Fprintf(&sb, "%s does not end your session; call it once, when the work is done, then finish your turn normally. ", FinishStepTool)
	sb.WriteString("Do not skip it, and do not substitute a shell command or a message for it: a step that exits without calling it is treated as not having done its job.\n\n")
	sb.WriteString(dbWriteRule)
	return sb.String()
}

// dbWriteRule closes the way a step with bypassPermissions was seen to
// route around the MCP surface (tend task #252, the Complete Breakdown dry
// run): needing to edit one table inside a large task body, the Ship step
// found no tool for it, read tend.db's schema with sqlite3, and ran an
// UPDATE on the tasks table itself. The write happened to be correct, but
// nothing checked it and no event recorded it. The tools are the one
// write path; a step that cannot express an edit through them stops and
// says so in its deliverable instead.
const dbWriteRule = "tend's MCP tools are the only way to read or change tend's tasks and workflows from this session. " +
	"Never open, copy or write tend's SQLite database (tend.db) with sqlite3, a script or any other means, " +
	"even to make an edit the tools cannot express: put what you could not record into your deliverable instead."

// deferredToolsHint covers the one way a session with many MCP servers
// has been seen to miss the hand-off (tend task #197, haiku): tend's
// tools are deferred behind a tool search, the model loads them, and
// then searches again and again instead of calling what it loaded. The
// tool ids are exact, so one load is enough, and the hint says so.
const deferredToolsHint = "These tool ids are exact. If they are not in your tool list yet, load them once by name " +
	"(a deferred-tool search for `select:" + FinishStepTool + "," + GetWorkflowStepTool + "`); " +
	"a search result that lists a tool means it is now callable, so call it directly. Never search for the same tool twice."

// NudgePrompt is the one follow-up turn the runner sends to a step's
// session when it exited without calling finish_step. Short, because the
// session already holds the step and its work; it only has to name what
// is missing and what is allowed.
func NudgePrompt(step string, outcomes []string) string {
	if len(outcomes) == 0 {
		outcomes = []string{OutcomeDone}
	}
	return fmt.Sprintf("Your previous turn ended without handing off step %q: the workflow run is stopped until you call the MCP tool %s "+
		"with `outcome` set to one of %s and the step's result as `deliverable`. Call it now with the verdict you already reached, "+
		"then finish your turn. Do not redo the work. %s",
		step, FinishStepTool, quoteAll(outcomes), deferredToolsHint)
}

// quoteAll renders outcomes as `"approve", "reject"`.
func quoteAll(outcomes []string) string {
	q := make([]string, 0, len(outcomes))
	for _, o := range outcomes {
		q = append(q, strconv.Quote(o))
	}
	return strings.Join(q, ", ")
}
