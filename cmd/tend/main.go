// Command tend is the entrypoint: it wires the concrete store into the CLI
// and dispatches. Wiring only — behavior lives in internal packages.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/jwstover/tend/internal/cli"
	"github.com/jwstover/tend/internal/store"
	"github.com/jwstover/tend/internal/tui"
)

func main() {
	open := func(ctx context.Context, dbPath string) (cli.Store, error) {
		return store.Open(ctx, dbPath)
	}
	runTUI := func(ctx context.Context, dbPath string) error {
		s, err := store.Open(ctx, dbPath)
		if err != nil {
			return err
		}
		defer s.Close()
		return tui.Run(ctx, s, dbPath)
	}
	if err := cli.Execute(open, runTUI); err != nil {
		fmt.Fprintln(os.Stderr, "tend:", err)
		os.Exit(1)
	}
}
