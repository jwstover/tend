package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/jwstover/tend/internal/agent"
)

// newAgentHookCmd wires the hidden `tend agent-hook <event>` command:
// one short-lived process per Claude Code hook firing, spawned by
// `claude` itself via the generated --settings file (see
// agent.WriteHookSettings), reading the hook's JSON payload from stdin
// and recording what it implies about the session's status
// (docs/agent-sessions-plan.md §8.2).
//
// No socket server and no daemon — tend already owns a WAL-mode sqlite
// database every instance can write to, which is the whole reason this
// can be a plain CLI command.
//
// Hidden because it is plumbing a launched session's --settings points
// at, not something a user runs by hand.
func newAgentHookCmd(open func(ctx context.Context) (Store, error)) *cobra.Command {
	return &cobra.Command{
		Use:    "agent-hook <event>",
		Short:  "Record a Claude Code hook event against its session",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Never surface a failure as a non-zero exit. This runs
			// synchronously between a session's turns, so the worst
			// thing it could do is get in the user's way — and a status
			// update is a convenience indicator that costs nothing when
			// lost. Genuine failures go to stderr for debugging and the
			// command still succeeds.
			if err := runAgentHook(cmd.Context(), open, args[0], cmd.InOrStdin()); err != nil {
				fmt.Fprintln(cmd.ErrOrStderr(), "tend agent-hook:", err)
			}
			return nil
		},
	}
}

// runAgentHook is the testable half: decode the payload, map the event
// to a status, and write it against the session's external id.
//
// The event comes from argv rather than the payload's own
// hook_event_name because tend generates both sides of this contract —
// argv is the explicit statement of which hook fired, and it keeps the
// command drivable in a test without synthesizing a full payload.
//
// The database is opened only after the payload validates, so a
// malformed or irrelevant firing costs no file I/O at all.
func runAgentHook(ctx context.Context, open func(ctx context.Context) (Store, error), event string, stdin io.Reader) error {
	status, ok := agent.StatusForEvent(event)
	if !ok {
		return fmt.Errorf("unsubscribed hook event %q", event)
	}
	payload, err := agent.ParseHookPayload(stdin)
	if err != nil {
		return err
	}
	if payload.SessionID == "" {
		return fmt.Errorf("hook event %s carried no session_id", event)
	}

	s, err := open(ctx)
	if err != nil {
		return err
	}
	defer s.Close()

	// A session id matching no row is the normal case for a brand-new
	// session's first run — agent_sessions rows are written when the
	// terminal handoff returns, not at launch — so SetSessionStatus
	// treats zero rows updated as success, not an error.
	return s.SetSessionStatus(ctx, payload.SessionID, status)
}
