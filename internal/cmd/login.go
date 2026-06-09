package cmd

import (
	"fmt"
	"os"
	"syscall"

	"github.com/agend-sh/cli/internal/api"
	"github.com/agend-sh/cli/internal/auth"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func newLoginCmd() *cobra.Command {
	var token string
	var email string
	var password string

	cmd := &cobra.Command{
		Use:   "login",
		Short: "Authenticate with agend",
		Long:  "Authenticate with your agend account via email/password, browser OAuth, or direct token.",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Direct token
			if token != "" {
				if err := auth.SaveToken(token); err != nil {
					return fmt.Errorf("failed to save token: %w", err)
				}
				fmt.Println("Authenticated successfully.")
				return nil
			}

			// Email/password login
			if email != "" {
				pw := password
				if pw == "" {
					fmt.Fprint(os.Stderr, "Password: ")
					pwBytes, err := term.ReadPassword(int(syscall.Stdin))
					if err != nil {
						return fmt.Errorf("failed to read password: %w", err)
					}
					fmt.Fprintln(os.Stderr)
					pw = string(pwBytes)
				}

				client := api.New(auth.LoadAPIURL(), "")
				resp, err := client.Login(email, pw)
				if err != nil {
					return fmt.Errorf("login failed: %w", err)
				}

				if err := auth.SaveToken(resp.Token); err != nil {
					return fmt.Errorf("save token: %w", err)
				}

				fmt.Println("Authenticated successfully.")
				return nil
			}

			// Browser OAuth
			fmt.Println("Opening browser for authentication...")
			fmt.Println("If the browser doesn't open, visit: https://agend.sh/auth/cli")
			fmt.Println()

			t, err := auth.BrowserLogin()
			if err != nil {
				return fmt.Errorf("login failed: %w", err)
			}

			if err := auth.SaveToken(t); err != nil {
				return fmt.Errorf("failed to save token: %w", err)
			}

			fmt.Println("Authenticated successfully.")
			return nil
		},
	}

	cmd.Flags().StringVar(&token, "token", "", "API token (skip interactive login)")
	cmd.Flags().StringVar(&email, "email", "", "login with email/password")
	cmd.Flags().StringVar(&password, "password", "", "password (non-interactive, for scripts)")

	return cmd
}
