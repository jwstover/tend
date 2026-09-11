package workflow

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
)

// The graph a workflow's edges imply, read two ways: as a text preview
// for the authoring view (Preview), and as a list of the things wrong
// with it (Validate). Both are pure functions of the steps in authoring
// order and the edges between them, so the TUI computes them in Update
// and a test can pin their output without a store.

// PreviewRow is one step of the text graph preview: its 1-based position
// in authoring order and the edges worth annotating. An edge routing
// OutcomeDone to the very next step is what a linear workflow has by
// default, so it is left implicit; every other edge is listed. End is set
// when no edge leaves the step, in which case its one outcome (done)
// ends the run and the row is annotated "done -> end".
type PreviewRow struct {
	Number int
	Step   Step
	Edges  []PreviewEdge
	End    bool
}

// PreviewEdge is an edge as the preview shows it: the edge itself and
// the 1-based number of the step it leads to, 0 when that step is not
// among the workflow's steps (which the schema's cascade should make
// impossible, but a preview should never crash over it).
type PreviewEdge struct {
	Edge Edge
	To   int
}

// String renders the annotation: "reject -> 2 (max 3)", or
// "reject -> ?" when the target is unknown.
func (e PreviewEdge) String() string {
	to := "?"
	if e.To > 0 {
		to = fmt.Sprintf("%d", e.To)
	}
	s := fmt.Sprintf("%s -> %s", e.Edge.Outcome, to)
	if e.Edge.MaxIterations != nil {
		s += fmt.Sprintf(" (max %d)", *e.Edge.MaxIterations)
	}
	return s
}

// Annotations is every bracketed note the row carries: one per listed
// edge, or "done -> end" for a step nothing leaves. Empty for a step
// whose only edge is the implicit done -> next.
func (r PreviewRow) Annotations() []string {
	if r.End {
		return []string{OutcomeDone + " -> end"}
	}
	out := make([]string, 0, len(r.Edges))
	for _, e := range r.Edges {
		out = append(out, e.String())
	}
	return out
}

// Preview lays the graph out as numbered rows in authoring order. steps
// must be in sort order (as ListSteps returns them); edges are matched to
// them by id and kept in the order given, which for ListEdges is by
// source step then outcome.
func Preview(steps []Step, edges []Edge) []PreviewRow {
	number := make(map[int64]int, len(steps))
	for i, st := range steps {
		number[st.ID] = i + 1
	}
	rows := make([]PreviewRow, 0, len(steps))
	for i, st := range steps {
		row := PreviewRow{Number: i + 1, Step: st}
		var leaving int
		for _, e := range edges {
			if e.FromStepID != st.ID {
				continue
			}
			leaving++
			to := number[e.ToStepID]
			if e.Outcome == OutcomeDone && to == i+2 && e.MaxIterations == nil {
				continue // the linear default; implied by the numbering
			}
			row.Edges = append(row.Edges, PreviewEdge{Edge: e, To: to})
		}
		row.End = leaving == 0
		rows = append(rows, row)
	}
	return rows
}

// PreviewText is the preview as plain lines, one per step:
//
//	1. implement
//	2. review  [reject -> 1 (max 3)]  [approve -> 3]
//	3. gate
//	4. ship  [done -> end]
func PreviewText(steps []Step, edges []Edge) string {
	var sb strings.Builder
	for i, row := range Preview(steps, edges) {
		if i > 0 {
			sb.WriteByte('\n')
		}
		fmt.Fprintf(&sb, "%d. %s", row.Number, row.Step.Name)
		for _, a := range row.Annotations() {
			sb.WriteString("  [" + a + "]")
		}
	}
	return sb.String()
}

// Problem is one thing Validate found wrong: which step (by id and name;
// zero and "" for the workflow as a whole) and what.
type Problem struct {
	StepID int64
	Step   string
	Msg    string
}

// String reads "review: prompt mentions "approve" but no edge routes it".
func (p Problem) String() string {
	if p.Step == "" {
		return p.Msg
	}
	return p.Step + ": " + p.Msg
}

// conventionalOutcomes are always part of the vocabulary Validate looks
// for in prompts: a review prompt that says "approve or reject" needs
// those edges whether or not any other step in the workflow uses them.
var conventionalOutcomes = []string{OutcomeApprove, OutcomeReject}

// Validate reports everything wrong with the graph, in authoring order,
// empty when it is sound. It is the authoring-time counterpart of the
// runner's rules: the runner picks every step after the first by edge, so
// a step no edge leads to never runs; a step nothing leaves ends the run,
// which is only right for the last one; an outcome a prompt names has to
// have an edge or finish_step will refuse it; a loop-back from an agent
// step with no max_iterations can run forever; and a prompt that does not
// render as a template fails the run at launch.
func Validate(steps []Step, edges []Edge) []Problem {
	if len(steps) == 0 {
		return []Problem{{Msg: "no steps"}}
	}
	var problems []Problem
	add := func(st Step, format string, args ...any) {
		problems = append(problems, Problem{StepID: st.ID, Step: st.Name, Msg: fmt.Sprintf(format, args...)})
	}

	index := make(map[int64]int, len(steps))
	for i, st := range steps {
		index[st.ID] = i
	}
	leaving := make(map[int64][]Edge, len(steps))
	vocabulary := map[string]bool{}
	for _, o := range conventionalOutcomes {
		vocabulary[o] = true
	}
	for _, e := range edges {
		leaving[e.FromStepID] = append(leaving[e.FromStepID], e)
		if e.Outcome != OutcomeDone {
			vocabulary[e.Outcome] = true
		}
	}

	// Reachability from the first step, the only one entered by position.
	reached := map[int64]bool{steps[0].ID: true}
	frontier := []int64{steps[0].ID}
	for len(frontier) > 0 {
		id := frontier[0]
		frontier = frontier[1:]
		for _, e := range leaving[id] {
			if !reached[e.ToStepID] {
				reached[e.ToStepID] = true
				frontier = append(frontier, e.ToStepID)
			}
		}
	}

	for i, st := range steps {
		if !reached[st.ID] {
			add(st, "unreachable: no edge leads here")
		}
		out := leaving[st.ID]
		if len(out) == 0 && i < len(steps)-1 {
			add(st, "no edge leaves it; the run would end here, before %s", steps[i+1].Name)
		}
		routes := make(map[string]bool, len(out))
		for _, e := range out {
			routes[e.Outcome] = true
			to, known := index[e.ToStepID]
			if !known {
				add(st, "edge on %q leads to a step that no longer exists", e.Outcome)
				continue
			}
			if st.Kind == StepAgent && to <= i && e.MaxIterations == nil {
				add(st, "%q loops back to %s with no max iterations", e.Outcome, steps[to].Name)
			}
		}
		if st.Kind == StepAgent {
			if err := ValidatePrompt(st.PromptMD); err != nil {
				add(st, "%v", err)
			}
			prompt := maskOutcomes(st.PromptMD, slices.Sorted(maps.Keys(routes)))
			for _, o := range slices.Sorted(maps.Keys(vocabulary)) {
				if !routes[o] && mentionsOutcome(prompt, o) {
					add(st, "prompt mentions %q but no edge routes it", o)
				}
			}
		}
	}
	return problems
}

// maskOutcomes blanks every whole-word occurrence of the given outcomes
// in prompt, longest first, so that a routed "wave ready" is not also read
// as an unrouted "ready". The replacement keeps the prompt's length and
// breaks word boundaries, so nothing else shifts or matches across it.
func maskOutcomes(prompt string, outcomes []string) string {
	slices.SortStableFunc(outcomes, func(a, b string) int { return len(b) - len(a) })
	for _, o := range outcomes {
		re, err := regexp.Compile(`(?i)\b` + regexp.QuoteMeta(o) + `\b`)
		if err != nil {
			continue
		}
		prompt = re.ReplaceAllStringFunc(prompt, func(m string) string { return strings.Repeat("#", len(m)) })
	}
	return prompt
}

// handoffCue introduces the outcome an agent is to finish with: "finish
// with approve or reject", "finish_step with outcome `stuck`", "set the
// outcome to escalate". A cue reaches to the end of its sentence.
var handoffCue = regexp.MustCompile(`(?i)\b(?:finish(?:_step)?|outcomes?)\b[^.;\n]*`)

// mentionsOutcome reports whether prompt names outcome, case-insensitively
// and as a whole word, in a hand-off context: quoted or backticked
// anywhere ("reject", `reject`), or unquoted in the same sentence as a
// hand-off cue. Prose that happens to use the word -- "wait until the wave
// is ready", "note it and continue" -- is not a mention: the wave-style
// workflows route "ready" and "continue" from other steps while their
// Dispatch prompt uses both as plain English.
func mentionsOutcome(prompt, outcome string) bool {
	word := `(?i)\b` + regexp.QuoteMeta(outcome) + `\b`
	quoted, err := regexp.Compile("(?i)([`\"'])" + regexp.QuoteMeta(outcome) + `([` + "`" + `"'])`)
	if err != nil {
		return false
	}
	if quoted.MatchString(prompt) {
		return true
	}
	bare, err := regexp.Compile(word)
	if err != nil {
		return false
	}
	for _, sentence := range handoffCue.FindAllString(prompt, -1) {
		if bare.MatchString(sentence) {
			return true
		}
	}
	return false
}
