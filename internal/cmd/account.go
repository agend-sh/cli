package cmd

import (
	"fmt"

	"github.com/agend-sh/cli/internal/auth"
	"github.com/spf13/cobra"
)

func newAccountCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "account",
		Short:   "Manage multiple agend accounts",
		Long:    "List, switch between, and remove the agend accounts you've logged into. Log in to a new account with 'agend login --email <email>'.",
		Aliases: []string{"accounts"},
	}
	cmd.AddCommand(newAccountListCmd())
	cmd.AddCommand(newAccountSwitchCmd())
	cmd.AddCommand(newAccountRemoveCmd())
	return cmd
}

func newAccountListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Short:   "List logged-in accounts",
		Aliases: []string{"ls"},
		RunE: func(cmd *cobra.Command, args []string) error {
			accts, err := auth.ListAccounts()
			if err != nil || len(accts) == 0 {
				fmt.Println("No accounts. Run 'agend login' to get started.")
				return nil
			}
			for _, a := range accts {
				marker := "  "
				if a.Active {
					marker = "* "
				}
				status := ""
				if a.Expired {
					status = "  (session expired — run 'agend login')"
				}
				env := a.EnvID
				if env == "" {
					env = "(no environment)"
				}
				fmt.Printf("%s%-40s %s%s\n", marker, a.Email, env, status)
			}
			return nil
		},
	}
}

func newAccountSwitchCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "switch <email>",
		Short:   "Switch the active account",
		Aliases: []string{"use"},
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := auth.SwitchAccount(args[0]); err != nil {
				return err
			}
			fmt.Printf("Switched to %s.\n", args[0])
			return nil
		},
	}
}

func newAccountRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "remove <email>",
		Short:   "Remove a stored account",
		Aliases: []string{"rm"},
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := auth.RemoveAccount(args[0]); err != nil {
				return err
			}
			fmt.Printf("Removed %s.\n", args[0])
			if active := auth.ActiveEmail(); active != "" {
				fmt.Printf("Active account is now %s.\n", active)
			}
			return nil
		},
	}
}
