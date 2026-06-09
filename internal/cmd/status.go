package cmd

import (
	"fmt"

	"github.com/agend-sh/cli/internal/auth"
	"github.com/spf13/cobra"
)

func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show current authentication and environment status",
		RunE: func(cmd *cobra.Command, args []string) error {
			token, err := auth.LoadToken()
			if err != nil {
				fmt.Println("Status: not authenticated")
				fmt.Println("Run 'agend login' to get started.")
				return nil
			}

			fmt.Println("Status: authenticated")
			if len(token) >= 12 {
				fmt.Printf("Token:  %s...%s\n", token[:8], token[len(token)-4:])
			} else {
				fmt.Printf("Token:  %s\n", token)
			}

			envID, endpoint, _, _, _ := auth.LoadEnvironment()
			if envID == "" {
				fmt.Println("\nNo active environment. Run 'agend env create' to start one.")
				return nil
			}

			fmt.Printf("\nEnvironment: %s\n", envID)
			fmt.Printf("Endpoint:    %s\n", endpoint)

			client, err := apiClient()
			if err != nil {
				return nil // just show local info if API is unavailable
			}

			resp, err := client.GetEnvironment(envID)
			if err != nil {
				fmt.Printf("State:       unknown (API error)\n")
				return nil
			}

			fmt.Printf("State:       %s\n", resp.State)
			fmt.Printf("Tier:        %s\n", resp.Tier)
			fmt.Printf("Last active: %s\n", resp.LastActive)
			return nil
		},
	}
}
