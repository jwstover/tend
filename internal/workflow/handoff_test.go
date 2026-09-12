package workflow

import (
	"strings"
	"testing"
)

func TestFallbackAllowed(t *testing.T) {
	cases := []struct {
		outcomes []string
		want     bool
	}{
		{nil, true},
		{[]string{}, true},
		{[]string{"done"}, true},
		{[]string{"approve"}, false},
		{[]string{"approve", "reject"}, false},
		{[]string{"done", "retry"}, false},
	}
	for _, tc := range cases {
		if got := FallbackAllowed(tc.outcomes); got != tc.want {
			t.Errorf("FallbackAllowed(%v) = %v, want %v", tc.outcomes, got, tc.want)
		}
	}
}

// The injected block names where the agent is, the exact tool ids, every
// allowed outcome, and that the tool does not end the session.
func TestStepSystemPrompt(t *testing.T) {
	got := StepSystemPrompt(HandoffContext{Workflow: "ship it", Step: "review", Iteration: 2, Outcomes: []string{"approve", "reject"}})
	for _, want := range []string{
		`step "review"`, `workflow "ship it"`, "iteration 2",
		"mcp__tend__finish_step", "mcp__tend__get_workflow_step",
		`"approve", "reject"`, "does not end your session",
		// A bypassPermissions step once edited tend.db directly when no
		// tool could express its change (task #252); the block forbids it.
		"Never open, copy or write tend's SQLite database",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("system prompt missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "%") {
		t.Errorf("system prompt has an unrendered verb:\n%s", got)
	}

	// No edges means the one right answer is done, the same as
	// get_workflow_step reports.
	last := StepSystemPrompt(HandoffContext{Workflow: "w", Step: "ship", Iteration: 1})
	if !strings.Contains(last, `one of: "done".`) {
		t.Errorf("system prompt for an edgeless step should offer done:\n%s", last)
	}
}

func TestNudgePrompt(t *testing.T) {
	got := NudgePrompt("review", []string{"approve", "reject"})
	for _, want := range []string{`step "review"`, "mcp__tend__finish_step", `"approve", "reject"`, "Do not redo the work"} {
		if !strings.Contains(got, want) {
			t.Errorf("nudge missing %q:\n%s", want, got)
		}
	}
	if !strings.Contains(NudgePrompt("ship", nil), `"done"`) {
		t.Error("nudge for an edgeless step should offer done")
	}
}
