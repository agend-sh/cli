package main

import (
	"fmt"
	"os"

	"github.com/agend-sh/cli/internal/cmd"
)

var version = "0.0.0-dev"

func main() {
	root := cmd.NewRoot(version)
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
