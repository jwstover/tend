package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jwstover/tend/internal/jira"
	"github.com/jwstover/tend/internal/task"
)

func newAddCmd(open func(context.Context) (Store, error)) *cobra.Command {
	var projectName string
	cmd := &cobra.Command{
		Use:     "add <text>...",
		Aliases: []string{"a"},
		Short:   "Capture a task instantly (no TUI)",
		Long: "Capture a task into the inbox and exit. With no arguments, " +
			"reads from stdin: each non-empty line becomes a task. A single " +
			"Jira issue URL argument is expanded: the ticket key and title " +
			"become the task title and the link lands in the body (see " +
			"`tend auth jira login`).\n\nCaptures land in the project new tasks are " +
			"aimed at (see `tend projects use`); --project overrides that for one " +
			"invocation.",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			if len(args) == 1 {
				if iss, ok := jira.ParseIssueURL(args[0]); ok {
					return addJiraTask(cmd, open, iss, projectName)
				}
			}

			titles, err := gatherTitles(cmd, args)
			if err != nil {
				return err
			}

			s, err := open(ctx)
			if err != nil {
				return err
			}
			defer s.Close()

			// Resolved once, before the loop: an unknown name should fail
			// the command outright rather than capture the first line and
			// then error on the second.
			target, err := captureTarget(ctx, s, projectName)
			if err != nil {
				return err
			}

			for _, title := range titles {
				t, err := addOne(ctx, s, target, title, "")
				if err != nil {
					return fmt.Errorf("adding %q: %w", title, err)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "added #%d%s: %s\n",
					t.ID, projectSuffix(ctx, s, t.ProjectID), t.Title)
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&projectName, "project", "p", "",
		"capture into a named project instead of the active one")
	return cmd
}

// captureTarget resolves where a capture lands: an explicit --project, or
// nil meaning "whatever the store's active project is". Returning nil
// rather than resolving the active id here keeps the zero-flag path on
// Store.AddTask, which is the one capture path tuned to stay fast.
func captureTarget(ctx context.Context, s Store, name string) (*int64, error) {
	if name == "" {
		return nil, nil
	}
	p, err := resolveProject(ctx, s, name)
	if err != nil {
		return nil, err
	}
	return &p.ID, nil
}

// addOne captures one task, into an explicit project when the caller has
// one and the active project otherwise.
func addOne(ctx context.Context, s Store, target *int64, title, body string) (task.Task, error) {
	switch {
	case target != nil && body != "":
		return s.AddTaskWithBodyIn(ctx, *target, title, body)
	case target != nil:
		return s.AddTaskIn(ctx, *target, title)
	case body != "":
		return s.AddTaskWithBody(ctx, title, body)
	default:
		return s.AddTask(ctx, title)
	}
}

// addJiraTask captures a task from a pasted Jira issue URL: the expanded
// title (or the bare key when the lookup can't happen) plus the link in
// the body. Lookup failures warn but never block capture.
func addJiraTask(cmd *cobra.Command, open func(context.Context) (Store, error),
	iss jira.Issue, projectName string) error {
	ctx := cmd.Context()

	title, warn := jira.Expand(ctx, iss)
	if warn != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: using bare ticket key: %v\n", warn)
	}

	s, err := open(ctx)
	if err != nil {
		return err
	}
	defer s.Close()

	target, err := captureTarget(ctx, s, projectName)
	if err != nil {
		return err
	}
	t, err := addOne(ctx, s, target, title, iss.URL+"\n")
	if err != nil {
		return fmt.Errorf("adding %q: %w", title, err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "added #%d%s: %s\n",
		t.ID, projectSuffix(ctx, s, t.ProjectID), t.Title)
	return nil
}

// projectSuffix renders " to <project>" for the capture confirmation.
// Capture now targets whichever project the TUI last selected
// (docs/projects-plan.md §3), so a task captured from a bare shell can
// land somewhere the user wasn't picturing — echoing the destination is
// the mitigation, since AGENTS.md §2 forbids making capture ask.
//
// A lookup failure yields "": the task is already saved, and failing the
// command over a cosmetic label would be worse than staying quiet.
func projectSuffix(ctx context.Context, s Store, projectID int64) string {
	p, err := s.GetProject(ctx, projectID)
	if err != nil {
		return ""
	}
	return " to " + p.Name
}

// gatherTitles turns the invocation into task titles: arguments join into
// a single title; with no arguments, piped stdin yields one task per
// non-empty line.
func gatherTitles(cmd *cobra.Command, args []string) ([]string, error) {
	if len(args) > 0 {
		return []string{strings.Join(args, " ")}, nil
	}

	if f, ok := cmd.InOrStdin().(*os.File); ok {
		info, err := f.Stat()
		if err == nil && info.Mode()&os.ModeCharDevice != 0 {
			return nil, fmt.Errorf("nothing to add: pass a title or pipe lines on stdin")
		}
	}

	var titles []string
	scanner := bufio.NewScanner(cmd.InOrStdin())
	for scanner.Scan() {
		if line := strings.TrimSpace(scanner.Text()); line != "" {
			titles = append(titles, line)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading stdin: %w", err)
	}
	if len(titles) == 0 {
		return nil, fmt.Errorf("nothing to add: stdin was empty")
	}
	return titles, nil
}
