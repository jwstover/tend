package workflow

import (
	"strings"
	"testing"
)

// reviewLoop is the acceptance workflow of the edges editor: implement,
// review, a manual gate, ship, with a bounded reject loop from review
// back to implement. Edges are returned the way ListEdges orders them:
// by source step, then outcome.
func reviewLoop() ([]Step, []Edge) {
	steps := []Step{
		{ID: 1, Name: "implement", Kind: StepAgent, SortOrder: 0, PromptMD: "Implement {{.Task.Title}}.{{if .Feedback}} Feedback: {{.Feedback}}{{end}}"},
		{ID: 2, Name: "review", Kind: StepAgent, SortOrder: 1, PromptMD: "Review {{.Input}}; finish with approve or reject."},
		{ID: 3, Name: "gate", Kind: StepGate, SortOrder: 2},
		{ID: 4, Name: "ship", Kind: StepAgent, SortOrder: 3, PromptMD: "Open the PR."},
	}
	three := int64(3)
	edges := []Edge{
		{ID: 10, FromStepID: 1, Outcome: "done", ToStepID: 2},
		{ID: 11, FromStepID: 2, Outcome: "approve", ToStepID: 3},
		{ID: 12, FromStepID: 2, Outcome: "reject", ToStepID: 1, MaxIterations: &three},
		{ID: 13, FromStepID: 3, Outcome: "approve", ToStepID: 4},
		{ID: 14, FromStepID: 3, Outcome: "reject", ToStepID: 1},
	}
	return steps, edges
}

func TestPreviewTextAnnotatesEverythingButTheLinearDefault(t *testing.T) {
	steps, edges := reviewLoop()
	want := strings.Join([]string{
		"1. implement",
		"2. review  [approve -> 3]  [reject -> 1 (max 3)]",
		"3. gate  [approve -> 4]  [reject -> 1]",
		"4. ship  [done -> end]",
	}, "\n")
	if got := PreviewText(steps, edges); got != want {
		t.Errorf("PreviewText =\n%s\nwant\n%s", got, want)
	}
}

func TestPreviewRows(t *testing.T) {
	steps, edges := reviewLoop()
	rows := Preview(steps, edges)
	if len(rows) != 4 {
		t.Fatalf("rows = %d, want 4", len(rows))
	}
	// implement's only edge is done -> next: nothing to annotate, and it
	// is not an end.
	if len(rows[0].Edges) != 0 || rows[0].End || len(rows[0].Annotations()) != 0 {
		t.Errorf("implement row = %+v, want no annotations", rows[0])
	}
	// ship has nothing leaving it: End, annotated as such.
	if !rows[3].End || rows[3].Annotations()[0] != "done -> end" {
		t.Errorf("ship row = %+v, want End with 'done -> end'", rows[3])
	}
	// A done edge that skips a step, or is bounded, is not the default.
	two := int64(2)
	skip := []Edge{{FromStepID: 1, Outcome: "done", ToStepID: 3}, {FromStepID: 3, Outcome: "done", ToStepID: 4, MaxIterations: &two}}
	rows = Preview(steps, skip)
	if got := rows[0].Annotations(); len(got) != 1 || got[0] != "done -> 3" {
		t.Errorf("skipping done edge annotated as %v, want [done -> 3]", got)
	}
	if got := rows[2].Annotations(); len(got) != 1 || got[0] != "done -> 4 (max 2)" {
		t.Errorf("bounded done edge annotated as %v, want [done -> 4 (max 2)]", got)
	}
	// An edge to a step not in the list renders "?" rather than crashing.
	rows = Preview(steps, []Edge{{FromStepID: 1, Outcome: "done", ToStepID: 99}})
	if got := rows[0].Annotations(); len(got) != 1 || got[0] != "done -> ?" {
		t.Errorf("dangling edge annotated as %v, want [done -> ?]", got)
	}
	if PreviewText(nil, nil) != "" {
		t.Error("PreviewText of no steps should be empty")
	}
}

func TestValidateSoundGraph(t *testing.T) {
	steps, edges := reviewLoop()
	if got := Validate(steps, edges); len(got) != 0 {
		t.Errorf("Validate(review loop) = %v, want no problems", got)
	}
	// A single step with no edges is the simplest sound workflow.
	if got := Validate(steps[:1], nil); len(got) != 0 {
		t.Errorf("Validate(one step) = %v, want no problems", got)
	}
}

func TestValidateProblems(t *testing.T) {
	steps, edges := reviewLoop()
	// A review step whose prompt names no outcome, for the cases about
	// edges alone.
	plainReview := Step{ID: 2, Name: "review", Kind: StepAgent, SortOrder: 1, PromptMD: "Review it."}
	cases := []struct {
		name  string
		steps []Step
		edges []Edge
		want  []string // substrings, one per expected problem, in order
	}{
		{
			name: "no steps",
			want: []string{"no steps"},
		},
		{
			name:  "no edges at all: every later step is unreachable and the first ends the run",
			steps: []Step{steps[0], plainReview},
			want: []string{
				"implement: no edge leaves it; the run would end here, before review",
				"review: unreachable: no edge leads here",
			},
		},
		{
			name:  "a step nothing leads to",
			steps: steps,
			edges: append(edges[:1], Edge{FromStepID: 2, Outcome: "approve", ToStepID: 4}, edges[2], edges[3]),
			want:  []string{"gate: unreachable"},
		},
		{
			name:  "prompt names an outcome the step does not route",
			steps: []Step{steps[0], {ID: 2, Name: "review", Kind: StepAgent, PromptMD: "Say APPROVE, or `reject` it."}},
			edges: []Edge{{FromStepID: 1, Outcome: "done", ToStepID: 2}, {FromStepID: 2, Outcome: "approve", ToStepID: 1, MaxIterations: ptr(2)}},
			want:  []string{`review: prompt mentions "reject" but no edge routes it`},
		},
		{
			name:  "an outcome from another step's edges counts as vocabulary too",
			steps: []Step{steps[0], {ID: 2, Name: "review", Kind: StepAgent, PromptMD: "finish with retry if flaky"}},
			edges: []Edge{{FromStepID: 1, Outcome: "retry", ToStepID: 1, MaxIterations: ptr(2)}, {FromStepID: 1, Outcome: "done", ToStepID: 2}},
			want:  []string{`review: prompt mentions "retry" but no edge routes it`},
		},
		{
			name:  "a word containing the outcome is not a mention",
			steps: []Step{steps[0], {ID: 2, Name: "ship", Kind: StepAgent, PromptMD: "The change was approved and rejections are logged."}},
			edges: edges[:1],
			want:  nil,
		},
		{
			// The wave workflow's steps talk about the sub-task done *state*
			// while their own outcomes are "wave ready" and the like. done is
			// never vocabulary: every step may finish with it, so it is not a
			// hand-off the edges have to route.
			name:  "the sub-task done state is not an outcome: done is never vocabulary",
			steps: []Step{steps[0], {ID: 2, Name: "dispatch", Kind: StepAgent, PermissionMode: "acceptEdits", PromptMD: "Set the sub-task to done when its PR merges. Finish with `wave ready` or `stuck`."}},
			edges: []Edge{{FromStepID: 1, Outcome: "done", ToStepID: 2}, {FromStepID: 2, Outcome: "wave ready", ToStepID: 1, MaxIterations: ptr(2)}, {FromStepID: 2, Outcome: "stuck", ToStepID: 1, MaxIterations: ptr(2)}},
			want:  nil,
		},
		{
			// Dispatch's prose uses "ready", "escalate" and "continue" as
			// plain English; the gate and Prepare route them as outcomes.
			// Only a word in a hand-off context is a mention.
			name:  "wave-style prose: an outcome word outside a hand-off context is not a mention",
			steps: []Step{steps[0], {ID: 2, Name: "dispatch", Kind: StepAgent, PermissionMode: "acceptEdits", PromptMD: "Wait until the wave is ready. If a sub-task is stuck, do not escalate on your own; note it and continue.\nFinish with `wave ready`, or `stuck` once nothing can proceed."}, {ID: 3, Name: "gate", Kind: StepGate}},
			edges: []Edge{
				{FromStepID: 1, Outcome: "ready", ToStepID: 2}, {FromStepID: 1, Outcome: "escalate", ToStepID: 3},
				{FromStepID: 2, Outcome: "wave ready", ToStepID: 1, MaxIterations: ptr(2)}, {FromStepID: 2, Outcome: "stuck", ToStepID: 3},
				{FromStepID: 3, Outcome: "continue", ToStepID: 2},
			},
			want: nil,
		},
		{
			// "wave ready" is routed; the "ready" inside it is not a second,
			// unrouted mention.
			name:  "a routed outcome containing a shorter one is not a mention of the shorter",
			steps: []Step{steps[0], {ID: 2, Name: "dispatch", Kind: StepAgent, PermissionMode: "acceptEdits", PromptMD: "finish_step with outcome `wave ready`"}},
			edges: []Edge{{FromStepID: 1, Outcome: "ready", ToStepID: 2}, {FromStepID: 2, Outcome: "wave ready", ToStepID: 1, MaxIterations: ptr(2)}},
			want:  nil,
		},
		{
			name:  "an outcome named after a hand-off cue is a mention even unquoted",
			steps: []Step{steps[0], {ID: 2, Name: "dispatch", Kind: StepAgent, PermissionMode: "acceptEdits", PromptMD: "When nothing can proceed, set the outcome to escalate. Otherwise finish with wave ready."}, {ID: 3, Name: "gate", Kind: StepGate}},
			edges: []Edge{{FromStepID: 1, Outcome: "escalate", ToStepID: 3}, {FromStepID: 2, Outcome: "wave ready", ToStepID: 1, MaxIterations: ptr(2)}, {FromStepID: 3, Outcome: "continue", ToStepID: 2}},
			want:  []string{`dispatch: prompt mentions "escalate" but no edge routes it`},
		},
		{
			name:  "an outcome in quotes is a mention wherever it appears",
			steps: []Step{steps[0], {ID: 2, Name: "review", Kind: StepAgent, PermissionMode: "acceptEdits", PromptMD: `If the tests are red, "reject" the change.`}},
			edges: []Edge{{FromStepID: 1, Outcome: "done", ToStepID: 2}, {FromStepID: 2, Outcome: "approve", ToStepID: 1, MaxIterations: ptr(2)}},
			want:  []string{`review: prompt mentions "reject" but no edge routes it`},
		},
		{
			name:  "agent loop-back with no max iterations",
			steps: []Step{steps[0], plainReview},
			edges: []Edge{{FromStepID: 1, Outcome: "done", ToStepID: 2}, {FromStepID: 2, Outcome: "reject", ToStepID: 1}},
			want:  []string{`review: "reject" loops back to implement with no max iterations`},
		},
		{
			name:  "a gate's unbounded loop-back is fine: a human decides each time",
			steps: []Step{steps[0], {ID: 3, Name: "gate", Kind: StepGate}},
			edges: []Edge{{FromStepID: 1, Outcome: "done", ToStepID: 3}, {FromStepID: 3, Outcome: "reject", ToStepID: 1}},
			want:  nil,
		},
		{
			name:  "template that does not render",
			steps: []Step{{ID: 1, Name: "implement", Kind: StepAgent, PromptMD: "{{.Nope}}"}},
			want:  []string{"implement: invalid prompt template"},
		},
		{
			name:  "gates carry no prompt, so theirs is never checked",
			steps: []Step{{ID: 1, Name: "gate", Kind: StepGate, PromptMD: "{{.Nope}} approve"}},
			want:  nil,
		},
		{
			name:  "edge to a missing step",
			steps: steps[:1],
			edges: []Edge{{FromStepID: 1, Outcome: "done", ToStepID: 42}},
			want:  []string{`implement: edge on "done" leads to a step that no longer exists`},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Validate(tc.steps, tc.edges)
			if len(got) != len(tc.want) {
				t.Fatalf("Validate = %v (%d problems), want %d: %v", got, len(got), len(tc.want), tc.want)
			}
			for i, want := range tc.want {
				if !strings.Contains(got[i].String(), want) {
					t.Errorf("problem %d = %q, want it to contain %q", i, got[i], want)
				}
			}
		})
	}
}

func ptr(n int64) *int64 { return &n }
