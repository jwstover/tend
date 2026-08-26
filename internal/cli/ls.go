package cli

import (
	"context"
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

func newLsCmd(open func(context.Context) (Store, error)) *cobra.Command {
	return &cobra.Command{
		Use:   "ls",
		Short: "Dump the live view as plain text",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			s, err := open(ctx)
			if err != nil {
				return err
			}
			defer s.Close()

			// nil = every project. Scoping ls to the active project (with
			// an --all escape) lands with the rest of the project CLI in
			// Phase 3; until then it keeps its current global behaviour.
			tasks, err := s.ListLive(ctx, nil)
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
}
