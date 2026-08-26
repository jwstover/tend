package cli

import (
	"context"
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

func newLsCmd(open func(context.Context) (Store, error)) *cobra.Command {
	var (
		projectName string
		all         bool
	)
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "Dump the live view as plain text",
		Long: "Print the live view for one project. Defaults to the project new " +
			"captures land in (see `tend projects use`), matching what the TUI " +
			"shows on the same database; --all ignores the scoping.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			s, err := open(ctx)
			if err != nil {
				return err
			}
			defer s.Close()

			filter, err := lsScope(ctx, s, projectName, all)
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
	cmd.Flags().StringVarP(&projectName, "project", "p", "",
		"scope to a named project instead of the active one")
	cmd.Flags().BoolVarP(&all, "all", "a", false, "every project")
	return cmd
}

// lsScope resolves the project filter: an explicit --project wins, --all
// clears the scope, and the default is the active project so `tend ls`
// agrees with what the TUI shows on the same database.
func lsScope(ctx context.Context, s Store, name string, all bool) (*int64, error) {
	if all {
		return nil, nil
	}
	if name != "" {
		p, err := resolveProject(ctx, s, name)
		if err != nil {
			return nil, err
		}
		return &p.ID, nil
	}
	id, err := s.ActiveProjectID(ctx)
	if err != nil {
		return nil, err
	}
	return &id, nil
}
