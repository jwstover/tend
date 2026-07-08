package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

func newLogCmd(open func(context.Context) (Store, error)) *cobra.Command {
	return &cobra.Command{
		Use:   "log <text>...",
		Short: "Capture a standup note instantly (no TUI)",
		Long: "Capture a quick note about what you're working on and exit. " +
			"Notes surface in the standup view and `tend standup`.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			s, err := open(ctx)
			if err != nil {
				return err
			}
			defer s.Close()

			n, err := s.AddLogEntry(ctx, nil, strings.Join(args, " "))
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "logged: %s\n", n.Body)
			return nil
		},
	}
}
