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

			tasks, err := s.ListLive(ctx)
			if err != nil {
				return err
			}

			w := tabwriter.NewWriter(cmd.OutOrStdout(), 2, 4, 2, ' ', 0)
			for _, t := range tasks {
				extra := ""
				if t.Project != nil {
					extra += " @" + *t.Project
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
