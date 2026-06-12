package main

import (
	"fmt"
	"os"

	"github.com/agend-sh/cli/internal/cmd"
)

var version = "0.0.0-dev"

func main() {
	// Keep the public CLI current: at most once a day this checks for a newer
	// release and, if found, installs it and re-execs into it before the
	// command runs (preserving stdio for `agend mcp`). Fails open. Opt out with
	// AGEND_NO_AUTOUPDATE=1.
	cmd.MaybeAutoUpdate(version, os.Args)

	root := cmd.NewRoot(version)
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
