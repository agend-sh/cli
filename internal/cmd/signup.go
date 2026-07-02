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

func newSignupCmd() *cobra.Command {
	var email string

	cmd := &cobra.Command{
		Use:   "signup",
		Short: "Create a new agend account",
		RunE: func(cmd *cobra.Command, args []string) error {
			if email == "" {
				return fmt.Errorf("--email is required")
			}

			// Never a flag — argv is visible via ps. Non-interactive use
			// (pipes, agents) sets AGEND_PASSWORD, same as `agend login`.
			password := os.Getenv("AGEND_PASSWORD")
			if password == "" {
				if !term.IsTerminal(int(syscall.Stdin)) {
					return fmt.Errorf("stdin is not a terminal — set AGEND_PASSWORD for non-interactive signup")
				}
				fmt.Fprint(os.Stderr, "Password: ")
				pwBytes, err := term.ReadPassword(int(syscall.Stdin))
				if err != nil {
					return fmt.Errorf("failed to read password: %w", err)
				}
				fmt.Fprintln(os.Stderr)
				password = string(pwBytes)
			}

			if len(password) < 8 {
				return fmt.Errorf("password must be at least 8 characters")
			}

			client := api.New(auth.LoadAPIURL(), "")
			resp, err := client.Signup(email, password)
			if err != nil {
				return fmt.Errorf("signup failed: %w", err)
			}

			// Signup may put the account on a waitlist (no token issued) rather
			// than logging in immediately. Don't claim "account created" or try
			// to store an empty token in that case.
			if resp.Token == "" {
				if resp.Message != "" {
					fmt.Println(resp.Message)
				} else {
					fmt.Println("Account created — pending approval.")
				}
				return nil
			}

			if err := auth.SaveToken(resp.Token); err != nil {
				return fmt.Errorf("save token: %w", err)
			}

			fmt.Println("Account created. Welcome to agend!")
			fmt.Println("Run 'agend env create' to start your environment.")
			return nil
		},
	}

	cmd.Flags().StringVar(&email, "email", "", "email address")
	cmd.MarkFlagRequired("email")

	return cmd
}
