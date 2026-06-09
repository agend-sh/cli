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

			fmt.Fprint(os.Stderr, "Password: ")
			pwBytes, err := term.ReadPassword(int(syscall.Stdin))
			if err != nil {
				return fmt.Errorf("failed to read password: %w", err)
			}
			fmt.Fprintln(os.Stderr)
			password := string(pwBytes)

			if len(password) < 8 {
				return fmt.Errorf("password must be at least 8 characters")
			}

			client := api.New(auth.LoadAPIURL(), "")
			resp, err := client.Signup(email, password)
			if err != nil {
				return fmt.Errorf("signup failed: %w", err)
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
