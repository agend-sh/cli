package cmd

import (
	"fmt"

	"github.com/agend-sh/cli/internal/auth"
	"github.com/spf13/cobra"
)

func newLogoutCmd() *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "logout",
		Short: "Log out the active account (or --all)",
		Long:  "Removes the active account's stored credentials. With multiple accounts, the next one becomes active. Use --all to remove every account.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if all {
				if err := auth.RemoveAllAccounts(); err != nil {
					return fmt.Errorf("failed to remove credentials: %w", err)
				}
				fmt.Println("Logged out of all accounts.")
				return nil
			}
			who := auth.ActiveEmail()
			if err := auth.RemoveToken(); err != nil {
				return fmt.Errorf("failed to remove credentials: %w", err)
			}
			if who != "" && who != "default" {
				fmt.Printf("Logged out of %s.\n", who)
			} else {
				fmt.Println("Logged out.")
			}
			if active := auth.ActiveEmail(); active != "" && active != "default" {
				fmt.Printf("Active account is now %s.\n", active)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "log out of all stored accounts")
	return cmd
}
