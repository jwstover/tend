// Command td is the entrypoint: it wires the concrete store into the CLI
// and dispatches. Wiring only — behavior lives in internal packages.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/jwstover/td/internal/cli"
	"github.com/jwstover/td/internal/store"
)

func main() {
	open := func(ctx context.Context, dbPath string) (cli.Store, error) {
		return store.Open(ctx, dbPath)
	}
	if err := cli.Execute(open); err != nil {
		fmt.Fprintln(os.Stderr, "td:", err)
		os.Exit(1)
	}
}
