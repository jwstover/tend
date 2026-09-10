package runner

import (
	"context"

	"github.com/jwstover/tend/internal/agent"
)

// ClaudeExec is the production Exec: it runs a step as a real headless
// claude session (agent.HeadlessCmd / HeadlessResumeCmd through
// agent.RunHeadless) with the task-bound MCP tools and status hooks an
// interactive session gets, so a step can read and write its task and
// its session row shows starting, idle, ended like any other. DBPath is
// forwarded to both config files for the same reason the TUI forwards
// it: the spawned processes inherit claude's environment, not tend's.
type ClaudeExec struct {
	DBPath string
}

// Check reports whether claude is on $PATH.
func (ClaudeExec) Check() error { return agent.CheckInstalled() }

// Run executes one attempt of an agent step. The MCP and hook config
// files live exactly as long as the process: unlike an interactive
// session, a headless one cannot be backgrounded and reconnected, so
// there is nothing to keep them around for.
func (e ClaudeExec) Run(ctx context.Context, req StepExec) (agent.HeadlessResult, error) {
	// Bound to the step run as well as the task, so `tend mcp` registers
	// get_workflow_step and finish_step for this session.
	mcpPath, mcpCleanup, _ := agent.WriteMCPConfig(req.TaskID, req.StepRun.ID, e.DBPath)
	defer mcpCleanup()
	hooksPath, hooksCleanup, _ := agent.WriteHookSettings(e.DBPath)
	defer hooksCleanup()

	// The system prompt rides along on a resume too: a nudge or a crash
	// resume is a new turn of the same step, with the same contract.
	opts := agent.LaunchOpts{
		Prompt: req.Prompt, Model: req.StepRun.Model, PermissionMode: req.StepRun.PermissionMode,
		AppendSystemPrompt: req.StepRun.SystemPrompt,
	}
	build := agent.HeadlessCmd
	if req.Resume {
		build = agent.HeadlessResumeCmd
	}
	c := build(ctx, req.Run.Cwd, req.StepRun.SessionExternalID, mcpPath, hooksPath, opts)
	return agent.RunHeadless(c, req.StepRun.LogPath)
}
