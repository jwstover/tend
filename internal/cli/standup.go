package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/jwstover/tend/internal/task"
)

func newStandupCmd(open func(context.Context) (Store, error)) *cobra.Command {
	var since string
	cmd := &cobra.Command{
		Use:   "standup",
		Short: "Print a standup summary of recent task activity as markdown",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			now := time.Now()

			from := task.LastWorkdayStart(now)
			if since != "" {
				d, err := task.NormalizeDate(since)
				if err != nil {
					return err
				}
				from, err = time.ParseInLocation("2006-01-02", d, now.Location())
				if err != nil {
					return err
				}
			}

			s, err := open(ctx)
			if err != nil {
				return err
			}
			defer s.Close()

			notes, err := s.ListLogEntries(ctx, from, now)
			if err != nil {
				return err
			}
			events, err := s.ListEvents(ctx, from, now)
			if err != nil {
				return err
			}
			live, err := s.ListLive(ctx)
			if err != nil {
				return err
			}

			fmt.Fprint(cmd.OutOrStdout(), task.StandupMarkdown(
				task.WindowLabel(from, now), notes, task.Summarize(events), live))
			return nil
		},
	}
	cmd.Flags().StringVar(&since, "since", "",
		"start of the reporting window (YYYY-MM-DD, default last workday)")
	return cmd
}
