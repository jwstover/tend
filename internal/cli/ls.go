package cli

import (
	"context"
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/jwstover/tend/internal/task"
)

func newLsCmd(open func(context.Context) (Store, error)) *cobra.Command {
	var projectName string
	var asJSON, closed bool
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "Dump the live view as plain text",
		Long: "Print the live view across every project. --project narrows it to " +
			"one.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			s, err := open(ctx)
			if err != nil {
				return err
			}
			defer s.Close()

			filter, err := lsScope(ctx, s, projectName)
			if err != nil {
				return err
			}
			list := s.ListLive
			if closed {
				list = s.ListLiveWithCompleted
			}
			tasks, err := list(ctx, filter)
			if err != nil {
				return err
			}

			tags, err := s.TagsByTask(ctx)
			if err != nil {
				return err
			}

			if asJSON {
				return lsJSON(ctx, cmd, s, tasks, tags)
			}

			w := tabwriter.NewWriter(cmd.OutOrStdout(), 2, 4, 2, ' ', 0)
			for _, t := range tasks {
				extra := ""
				for _, tag := range tags[t.ID] {
					extra += " #" + tag
				}
				if t.Due != nil {
					extra += " due:" + *t.Due
				}
				fmt.Fprintf(w, "%d\t%s\t%s%s\n", t.ID, t.State, t.Title, extra)
			}
			return w.Flush()
		},
	}
	cmd.Flags().StringVarP(&projectName, "project", "p", "", "narrow to a named project")
	cmd.Flags().BoolVar(&closed, "closed", false, "include done, canceled and someday tasks, as the TUI's completed section does")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print {tasks, projects} as JSON (tasks without bodies; `tend show` has one in full)")
	return cmd
}

// lsJSON prints the live view as {tasks, projects}: every project, so a
// front end can name and group them, whatever --project narrowed.
func lsJSON(ctx context.Context, cmd *cobra.Command, s Store, tasks []task.Task, tags map[int64][]string) error {
	projects, err := s.ListProjects(ctx)
	if err != nil {
		return err
	}
	counts, err := s.ChildCounts(ctx)
	if err != nil {
		return err
	}
	out := struct {
		Tasks    []taskJSON    `json:"tasks"`
		Projects []projectJSON `json:"projects"`
	}{Tasks: make([]taskJSON, len(tasks)), Projects: make([]projectJSON, len(projects))}
	for i, t := range tasks {
		out.Tasks[i] = toTaskJSON(t, tags[t.ID])
		out.Tasks[i].ChildrenDone, out.Tasks[i].ChildrenTotal = counts[t.ID].Done, counts[t.ID].Total
	}
	for i, p := range projects {
		out.Projects[i] = toProjectJSON(p)
	}
	return writeJSON(cmd.OutOrStdout(), out)
}

// lsScope resolves the project filter. nil means every project: with no
// flag, `tend ls` dumps the whole live view, the way it always has. The
// shell reads no stored project state, so the same command in any
// terminal prints the same thing.
func lsScope(ctx context.Context, s Store, name string) (*int64, error) {
	if name == "" {
		return nil, nil
	}
	p, err := resolveProject(ctx, s, name)
	if err != nil {
		return nil, err
	}
	return &p.ID, nil
}
