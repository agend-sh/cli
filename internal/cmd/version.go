package cmd

import (
	"fmt"
	"runtime"

	"github.com/spf13/cobra"
)

func newVersionCmd(version string) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Show agend CLI version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("agend %s (%s/%s)\n", version, runtime.GOOS, runtime.GOARCH)
		},
	}
}
