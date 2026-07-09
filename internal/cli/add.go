package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jwstover/tend/internal/jira"
)

func newAddCmd(open func(context.Context) (Store, error)) *cobra.Command {
	return &cobra.Command{
		Use:     "add <text>...",
		Aliases: []string{"a"},
		Short:   "Capture a task instantly (no TUI)",
		Long: "Capture a task into the inbox and exit. With no arguments, " +
			"reads from stdin: each non-empty line becomes a task. A single " +
			"Jira issue URL argument is expanded: the ticket key and title " +
			"become the task title and the link lands in the body (see " +
			"`tend auth jira login`).",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			if len(args) == 1 {
				if iss, ok := jira.ParseIssueURL(args[0]); ok {
					return addJiraTask(cmd, open, iss)
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

			for _, title := range titles {
				t, err := s.AddTask(ctx, title)
				if err != nil {
					return fmt.Errorf("adding %q: %w", title, err)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "added #%d: %s\n", t.ID, t.Title)
			}
			return nil
		},
	}
}

// addJiraTask captures a task from a pasted Jira issue URL: the expanded
// title (or the bare key when the lookup can't happen) plus the link in
// the body. Lookup failures warn but never block capture.
func addJiraTask(cmd *cobra.Command, open func(context.Context) (Store, error), iss jira.Issue) error {
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

	t, err := s.AddTaskWithBody(ctx, title, iss.URL+"\n")
	if err != nil {
		return fmt.Errorf("adding %q: %w", title, err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "added #%d: %s\n", t.ID, t.Title)
	return nil
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
