package cli

import (
	"context"
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

func newLsCmd(open func(context.Context) (Store, error)) *cobra.Command {
	var projectName string
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
			tasks, err := s.ListLive(ctx, filter)
			if err != nil {
				return err
			}

			tags, err := s.TagsByTask(ctx)
			if err != nil {
				return err
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
	return cmd
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
