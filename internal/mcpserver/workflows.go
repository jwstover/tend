package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jwstover/tend/internal/workflow"
)

// The workflow authoring surface: enough of the store's workflow, step
// and edge writes for an agent session to draft a workflow from a
// description, which the user then refines in the TUI's workflows view.
// These tools are registered for every session, not just a workflow
// step's -- drafting a workflow is ordinary task work, and nothing here
// is bound to a task or a run. Deleting a whole workflow is deliberately
// left to the TUI: an agent confusing two ids should at worst lose a
// step it added, not a procedure the user authored.

// workflowOut is a workflow definition as list_workflows shows it.
type workflowOut struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	StepCount   int64  `json:"step_count"`
}

// workflowsOut wraps the list in an object (see subtasksOut).
type workflowsOut struct {
	Workflows []workflowOut `json:"workflows"`
}

// authoredStepOut is a step of a workflow definition -- distinct from
// stepOut (steps.go), which is a step *run* as its own session sees it.
// Position is the step's 1-based place in authoring order; the runner
// starts a run at position 1 and follows edges from there.
type authoredStepOut struct {
	ID             int64             `json:"id"`
	Name           string            `json:"name"`
	Kind           string            `json:"kind"`
	Position       int               `json:"position"`
	PromptMD       string            `json:"prompt_md,omitempty"`
	Model          string            `json:"model,omitempty"`
	PermissionMode string            `json:"permission_mode,omitempty"`
	Edges          []authoredEdgeOut `json:"edges"`
}

// authoredEdgeOut is one edge leaving a step: the outcome it routes and
// the step it leads to, by id and name.
type authoredEdgeOut struct {
	ID            int64  `json:"id"`
	Outcome       string `json:"outcome"`
	ToStepID      int64  `json:"to_step_id"`
	ToStep        string `json:"to_step"`
	MaxIterations *int64 `json:"max_iterations,omitempty"`
}

// workflowGraphOut is a whole workflow definition: what get_workflow
// returns and what every authoring mutation reports afterwards, so an
// agent sees the effect of each edit -- the preview the TUI shows and
// the problems its validator would flag -- without a second call.
type workflowGraphOut struct {
	ID          int64             `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Steps       []authoredStepOut `json:"steps"`
	// Preview is workflow.PreviewText: numbered steps with every edge
	// that is not the linear default annotated inline.
	Preview string `json:"preview"`
	// Problems is what workflow.Validate finds wrong, empty when the
	// graph is sound and every prompt renders.
	Problems []string `json:"problems"`
}

// permissionModes are the --permission-mode values a step may pin, the
// same set the TUI's picker offers; "" means inherit. Kept strict because
// a value claude rejects fails the run at launch, long after authoring.
var permissionModes = []string{"default", "acceptEdits", "bypassPermissions", "plan"}

// inheritAlias is what an agent may send for model or permission_mode
// to mean "leave the choice to claude", the way the TUI's picker reads
// "inherit"; it is stored as "".
const inheritAlias = "inherit"

// registerWorkflowTools wires the authoring surface onto srv.
func registerWorkflowTools(srv *mcp.Server, store Store) {
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_workflows",
		Description: "List every workflow definition with its step count. Use get_workflow to see one in full.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, workflowsOut, error) {
		wfs, err := store.ListWorkflows(ctx)
		if err != nil {
			return nil, workflowsOut{}, err
		}
		out := make([]workflowOut, len(wfs))
		for i, w := range wfs {
			out[i] = workflowOut{ID: w.ID, Name: w.Name, Description: w.Description, StepCount: w.StepCount}
		}
		return nil, workflowsOut{Workflows: out}, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name: "get_workflow",
		Description: "Get a workflow definition in full, by id or by name: its steps in authoring " +
			"order with their prompts, settings and edges, a text preview of the graph, and the " +
			"problems the authoring validator finds (unreachable steps, dead ends, outcomes a " +
			"prompt names but no edge routes, unbounded loop-backs, templates that fail to render).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in struct {
		WorkflowID *int64 `json:"workflow_id,omitempty" jsonschema:"the workflow id; give this or name"`
		Name       string `json:"name,omitempty" jsonschema:"the workflow name, matched case-insensitively; give this or workflow_id"`
	}) (*mcp.CallToolResult, workflowGraphOut, error) {
		id, err := resolveWorkflow(ctx, store, in.WorkflowID, in.Name)
		if err != nil {
			return nil, workflowGraphOut{}, err
		}
		return fetchGraph(ctx, store, id)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name: "create_workflow",
		Description: "Create an empty workflow definition. Names are unique, case-insensitively. " +
			"Then add_workflow_step for each step, in the order they should run; the first step " +
			"added is where a run starts.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in struct {
		Name        string `json:"name" jsonschema:"the workflow name"`
		Description string `json:"description,omitempty" jsonschema:"optional one-line description of what the workflow is for"`
	}) (*mcp.CallToolResult, workflowGraphOut, error) {
		w, err := store.CreateWorkflow(ctx, in.Name, in.Description)
		if err != nil {
			return nil, workflowGraphOut{}, err
		}
		return fetchGraph(ctx, store, w.ID)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "update_workflow",
		Description: "Rename a workflow and/or replace its description. Fields left out are unchanged.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in struct {
		WorkflowID  int64   `json:"workflow_id" jsonschema:"the workflow id"`
		Name        *string `json:"name,omitempty" jsonschema:"new name; omit to keep the current one"`
		Description *string `json:"description,omitempty" jsonschema:"new description; omit to keep the current one, send empty to clear it"`
	}) (*mcp.CallToolResult, workflowGraphOut, error) {
		if in.Name != nil {
			if err := store.RenameWorkflow(ctx, in.WorkflowID, *in.Name); err != nil {
				return nil, workflowGraphOut{}, err
			}
		}
		if in.Description != nil {
			if err := store.SetWorkflowDescription(ctx, in.WorkflowID, *in.Description); err != nil {
				return nil, workflowGraphOut{}, err
			}
		}
		return fetchGraph(ctx, store, in.WorkflowID)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name: "add_workflow_step",
		Description: "Append a step to a workflow. kind is agent (runs prompt_md as a headless " +
			"Claude Code session) or gate (pauses the run for a human decision; no prompt). " +
			"By default the previous step, if it has no edges yet, is linked to the new one " +
			"with a done edge, so steps added in order form a linear workflow with no edge " +
			"work; send link_from_previous false to add an unlinked step and route it with " +
			"add_workflow_edge. prompt_md is a Go text/template over {{.Task.Title}}, " +
			"{{.Task.Body}}, {{.Task.ID}}, {{.Cwd}}, {{.Input}} (the previous step's " +
			"deliverable), {{.Feedback}} (the deliverable of a step that routed back here), " +
			"{{.Iteration}} and {{.Outcomes}}; it must render or the step is refused. " +
			"model is opus, sonnet, haiku or inherit; permission_mode is default, acceptEdits, " +
			"bypassPermissions, plan or inherit.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in struct {
		WorkflowID       int64  `json:"workflow_id" jsonschema:"the workflow to add the step to"`
		Name             string `json:"name" jsonschema:"the step name, e.g. implement, review, ship"`
		Kind             string `json:"kind,omitempty" jsonschema:"agent (default) or gate"`
		PromptMD         string `json:"prompt_md,omitempty" jsonschema:"the step's prompt template; ignored for a gate"`
		Model            string `json:"model,omitempty" jsonschema:"opus, sonnet, haiku, or inherit (default)"`
		PermissionMode   string `json:"permission_mode,omitempty" jsonschema:"default, acceptEdits, bypassPermissions, plan, or inherit (default)"`
		LinkFromPrevious *bool  `json:"link_from_previous,omitempty" jsonschema:"link the previous step to this one with a done edge when it has no edges yet; defaults to true"`
	}) (*mcp.CallToolResult, workflowGraphOut, error) {
		kind := workflow.StepAgent
		if in.Kind != "" {
			kind = workflow.StepKind(strings.TrimSpace(in.Kind))
			if !kind.Valid() {
				return nil, workflowGraphOut{}, fmt.Errorf("unknown step kind %q; use agent or gate", in.Kind)
			}
		}
		model, err := normalizeModel(in.Model)
		if err != nil {
			return nil, workflowGraphOut{}, err
		}
		mode, err := normalizePermissionMode(in.PermissionMode)
		if err != nil {
			return nil, workflowGraphOut{}, err
		}
		prompt := ""
		if kind == workflow.StepAgent {
			prompt = in.PromptMD
			if err := workflow.ValidatePrompt(prompt); err != nil {
				return nil, workflowGraphOut{}, err
			}
		}

		// The previous step is read before the append so "previous" is
		// unambiguous even if two sessions author at once.
		before, err := store.ListSteps(ctx, in.WorkflowID)
		if err != nil {
			return nil, workflowGraphOut{}, err
		}
		st, err := store.AddStep(ctx, in.WorkflowID, in.Name, kind)
		if err != nil {
			return nil, workflowGraphOut{}, err
		}
		if prompt != "" || model != "" || mode != "" {
			st.PromptMD, st.Model, st.PermissionMode = prompt, model, mode
			if err := store.UpdateStep(ctx, st); err != nil {
				return nil, workflowGraphOut{}, err
			}
		}
		if (in.LinkFromPrevious == nil || *in.LinkFromPrevious) && len(before) > 0 {
			prev := before[len(before)-1]
			edges, err := store.OutgoingEdges(ctx, prev.ID)
			if err != nil {
				return nil, workflowGraphOut{}, err
			}
			if len(edges) == 0 {
				if _, err := store.SetEdge(ctx, prev.ID, workflow.OutcomeDone, st.ID, nil); err != nil {
					return nil, workflowGraphOut{}, err
				}
			}
		}
		return fetchGraph(ctx, store, in.WorkflowID)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name: "update_workflow_step",
		Description: "Change a step's name, kind, model and/or permission mode. Fields left out " +
			"are unchanged; send inherit for model or permission_mode to leave that choice " +
			"to claude. Use set_step_prompt for the prompt.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in struct {
		StepID         int64   `json:"step_id" jsonschema:"the step id"`
		Name           *string `json:"name,omitempty" jsonschema:"new name"`
		Kind           *string `json:"kind,omitempty" jsonschema:"agent or gate"`
		Model          *string `json:"model,omitempty" jsonschema:"opus, sonnet, haiku, or inherit"`
		PermissionMode *string `json:"permission_mode,omitempty" jsonschema:"default, acceptEdits, bypassPermissions, plan, or inherit"`
	}) (*mcp.CallToolResult, workflowGraphOut, error) {
		st, err := store.GetStep(ctx, in.StepID)
		if err != nil {
			return nil, workflowGraphOut{}, err
		}
		if in.Name != nil {
			st.Name = *in.Name
		}
		if in.Kind != nil {
			st.Kind = workflow.StepKind(strings.TrimSpace(*in.Kind))
			if !st.Kind.Valid() {
				return nil, workflowGraphOut{}, fmt.Errorf("unknown step kind %q; use agent or gate", *in.Kind)
			}
		}
		if in.Model != nil {
			if st.Model, err = normalizeModel(*in.Model); err != nil {
				return nil, workflowGraphOut{}, err
			}
		}
		if in.PermissionMode != nil {
			if st.PermissionMode, err = normalizePermissionMode(*in.PermissionMode); err != nil {
				return nil, workflowGraphOut{}, err
			}
		}
		if err := store.UpdateStep(ctx, st); err != nil {
			return nil, workflowGraphOut{}, err
		}
		return fetchGraph(ctx, store, st.WorkflowID)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name: "set_step_prompt",
		Description: "Replace a step's prompt template (see add_workflow_step for the variables). " +
			"The template must render or the write is refused, since a broken template fails " +
			"every run at launch. Every outcome the prompt tells the agent to finish with " +
			"needs an edge (add_workflow_edge), or finish_step will refuse it at run time.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in struct {
		StepID   int64  `json:"step_id" jsonschema:"the step id"`
		PromptMD string `json:"prompt_md" jsonschema:"the new prompt template, replacing the existing one"`
	}) (*mcp.CallToolResult, workflowGraphOut, error) {
		st, err := store.GetStep(ctx, in.StepID)
		if err != nil {
			return nil, workflowGraphOut{}, err
		}
		if err := workflow.ValidatePrompt(in.PromptMD); err != nil {
			return nil, workflowGraphOut{}, err
		}
		if err := store.SetStepPrompt(ctx, in.StepID, in.PromptMD); err != nil {
			return nil, workflowGraphOut{}, err
		}
		return fetchGraph(ctx, store, st.WorkflowID)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name: "reorder_workflow_steps",
		Description: "Rewrite a workflow's authoring order. step_ids must be exactly the " +
			"workflow's steps, in the new order; the first is where a run starts. Edges " +
			"are unaffected -- order only decides the starting step and how the preview reads.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in struct {
		WorkflowID int64   `json:"workflow_id" jsonschema:"the workflow id"`
		StepIDs    []int64 `json:"step_ids" jsonschema:"every step of the workflow, in the new order"`
	}) (*mcp.CallToolResult, workflowGraphOut, error) {
		if err := store.ReorderSteps(ctx, in.WorkflowID, in.StepIDs); err != nil {
			return nil, workflowGraphOut{}, err
		}
		return fetchGraph(ctx, store, in.WorkflowID)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name: "delete_workflow_step",
		Description: "Remove a step and every edge touching it. Refused while a live run has " +
			"executed the step.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in struct {
		StepID int64 `json:"step_id" jsonschema:"the step id"`
	}) (*mcp.CallToolResult, workflowGraphOut, error) {
		st, err := store.GetStep(ctx, in.StepID)
		if err != nil {
			return nil, workflowGraphOut{}, err
		}
		if err := store.DeleteStep(ctx, in.StepID); err != nil {
			return nil, workflowGraphOut{}, err
		}
		return fetchGraph(ctx, store, st.WorkflowID)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name: "add_workflow_edge",
		Description: "Route an outcome of one step to another step, creating the edge or " +
			"re-pointing the one that already routes that outcome. The edges leaving a step " +
			"are its allowed outcomes; an outcome with no edge ends the run. Both steps must " +
			"be in the same workflow. An edge that leads back to an earlier step is a loop " +
			"(a review's reject -> implement); give it max_iterations so the run cannot cycle " +
			"forever -- the validator flags an agent step's unbounded loop-back.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in struct {
		FromStepID    int64  `json:"from_step_id" jsonschema:"the step the outcome leaves"`
		Outcome       string `json:"outcome" jsonschema:"the outcome name, e.g. done, approve, reject; stored trimmed and lower-cased"`
		ToStepID      int64  `json:"to_step_id" jsonschema:"the step the outcome leads to"`
		MaxIterations *int64 `json:"max_iterations,omitempty" jsonschema:"how many times the target step may run within one run when reached this way, at least 1; omit for unbounded"`
	}) (*mcp.CallToolResult, workflowGraphOut, error) {
		e, err := store.SetEdge(ctx, in.FromStepID, in.Outcome, in.ToStepID, in.MaxIterations)
		if err != nil {
			return nil, workflowGraphOut{}, err
		}
		st, err := store.GetStep(ctx, e.FromStepID)
		if err != nil {
			return nil, workflowGraphOut{}, err
		}
		return fetchGraph(ctx, store, st.WorkflowID)
	})

	// Edges are addressed by (from step, outcome), the natural key an agent
	// thinks in ("drop review's reject route") and the one SetEdge upserts
	// on, rather than by an edge id it would have to look up first.
	mcp.AddTool(srv, &mcp.Tool{
		Name: "delete_workflow_edge",
		Description: "Remove the edge that routes an outcome from a step; that outcome then " +
			"ends the run.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in struct {
		FromStepID int64  `json:"from_step_id" jsonschema:"the step the edge leaves"`
		Outcome    string `json:"outcome" jsonschema:"the outcome the edge routes"`
	}) (*mcp.CallToolResult, workflowGraphOut, error) {
		o, err := workflow.NormalizeOutcome(in.Outcome)
		if err != nil {
			return nil, workflowGraphOut{}, err
		}
		st, err := store.GetStep(ctx, in.FromStepID)
		if err != nil {
			return nil, workflowGraphOut{}, err
		}
		edges, err := store.OutgoingEdges(ctx, in.FromStepID)
		if err != nil {
			return nil, workflowGraphOut{}, err
		}
		var found *workflow.Edge
		for i := range edges {
			if edges[i].Outcome == o {
				found = &edges[i]
				break
			}
		}
		if found == nil {
			names := make([]string, 0, len(edges))
			for _, e := range edges {
				names = append(names, e.Outcome)
			}
			if len(names) == 0 {
				return nil, workflowGraphOut{}, fmt.Errorf("step %q has no edges", st.Name)
			}
			return nil, workflowGraphOut{}, fmt.Errorf("step %q has no edge on %q; it routes: %s",
				st.Name, o, strings.Join(names, ", "))
		}
		if err := store.DeleteEdge(ctx, found.ID); err != nil {
			return nil, workflowGraphOut{}, err
		}
		return fetchGraph(ctx, store, st.WorkflowID)
	})
}

// resolveWorkflow turns get_workflow's either/or arguments into an id.
// The id wins when both are given; neither is an error the agent can act
// on rather than a lookup of workflow 0.
func resolveWorkflow(ctx context.Context, store Store, id *int64, name string) (int64, error) {
	if id != nil {
		return *id, nil
	}
	if strings.TrimSpace(name) == "" {
		return 0, errors.New("give workflow_id or name")
	}
	w, err := store.WorkflowByName(ctx, name)
	if err != nil {
		return 0, err
	}
	return w.ID, nil
}

// normalizeModel maps the wire form of a step's model to what the store
// keeps: "" (inherit) or the value as given. Models are not restricted to
// the TUI's three aliases because claude also accepts full model ids.
func normalizeModel(s string) (string, error) {
	m := strings.TrimSpace(s)
	if m == inheritAlias {
		return "", nil
	}
	return m, nil
}

// normalizePermissionMode maps the wire form of a step's permission mode
// to what the store keeps, refusing anything claude would not accept.
func normalizePermissionMode(s string) (string, error) {
	m := strings.TrimSpace(s)
	if m == "" || m == inheritAlias {
		return "", nil
	}
	for _, known := range permissionModes {
		if m == known {
			return m, nil
		}
	}
	return "", fmt.Errorf("unknown permission mode %q; use one of: %s, or inherit",
		s, strings.Join(permissionModes, ", "))
}

// fetchGraph loads and renders a whole workflow definition, the common
// tail of get_workflow and every authoring mutation.
func fetchGraph(ctx context.Context, store Store, workflowID int64) (*mcp.CallToolResult, workflowGraphOut, error) {
	w, err := store.GetWorkflow(ctx, workflowID)
	if err != nil {
		return nil, workflowGraphOut{}, err
	}
	steps, err := store.ListSteps(ctx, workflowID)
	if err != nil {
		return nil, workflowGraphOut{}, err
	}
	edges, err := store.ListEdges(ctx, workflowID)
	if err != nil {
		return nil, workflowGraphOut{}, err
	}
	names := make(map[int64]string, len(steps))
	for _, st := range steps {
		names[st.ID] = st.Name
	}
	out := workflowGraphOut{
		ID: w.ID, Name: w.Name, Description: w.Description,
		Steps:    make([]authoredStepOut, 0, len(steps)),
		Preview:  workflow.PreviewText(steps, edges),
		Problems: []string{},
	}
	for i, st := range steps {
		row := authoredStepOut{
			ID: st.ID, Name: st.Name, Kind: string(st.Kind), Position: i + 1,
			PromptMD: st.PromptMD, Model: st.Model, PermissionMode: st.PermissionMode,
			Edges: []authoredEdgeOut{},
		}
		for _, e := range edges {
			if e.FromStepID != st.ID {
				continue
			}
			row.Edges = append(row.Edges, authoredEdgeOut{
				ID: e.ID, Outcome: e.Outcome, ToStepID: e.ToStepID, ToStep: names[e.ToStepID],
				MaxIterations: e.MaxIterations,
			})
		}
		out.Steps = append(out.Steps, row)
	}
	// A workflow with no steps yet is not a problem, it is a draft; the
	// validator's "no steps" is for the run-time reading.
	if len(steps) > 0 {
		for _, p := range workflow.Validate(steps, edges) {
			out.Problems = append(out.Problems, p.String())
		}
	}
	return nil, out, nil
}
