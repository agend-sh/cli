package cmd

import (
	"fmt"
	"time"

	"github.com/agend-sh/cli/internal/auth"
	"github.com/spf13/cobra"
)

func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show current authentication status",
		RunE: func(cmd *cobra.Command, args []string) error {
			token, err := auth.LoadToken()
			if err != nil {
				fmt.Println("Status: not authenticated")
				fmt.Println("Run 'agend login' to get started.")
				return nil
			}

			// An existing token file isn't proof of a usable session — the JWT
			// may have expired. Check the exp claim locally so we don't claim
			// "authenticated" while every API call 401s.
			if auth.TokenExpired(token) {
				exp, _ := auth.TokenExpiry(token)
				fmt.Printf("Status: session expired (%s)\n", time.Unix(exp, 0).Format("2006-01-02 15:04 MST"))
				fmt.Println("Run 'agend login' to re-authenticate.")
				return nil
			}

			fmt.Println("Status: authenticated")
			if active := auth.ActiveEmail(); active != "" && active != "default" {
				fmt.Printf("Account: %s\n", active)
			}
			if len(token) >= 12 {
				fmt.Printf("Token:  %s...%s\n", token[:8], token[len(token)-4:])
			} else {
				fmt.Printf("Token:  %s\n", token)
			}
			if accts, err := auth.ListAccounts(); err == nil && len(accts) > 1 {
				others := make([]string, 0, len(accts)-1)
				for _, a := range accts {
					if !a.Active {
						others = append(others, a.Email)
					}
				}
				fmt.Printf("Other accounts: %v (switch with 'agend account switch')\n", others)
			}

			return nil
		},
	}
}
