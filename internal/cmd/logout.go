package cmd

import (
	"fmt"

	"github.com/agend-sh/cli/internal/auth"
	"github.com/spf13/cobra"
)

func newLogoutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Remove stored credentials",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := auth.RemoveToken(); err != nil {
				return fmt.Errorf("failed to remove credentials: %w", err)
			}
			fmt.Println("Logged out.")
			return nil
		},
	}
}
