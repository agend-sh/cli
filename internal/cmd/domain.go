package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

func newDomainCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "domain",
		Short: "Manage custom domains for port expose",
	}

	cmd.AddCommand(newDomainAddCmd())
	cmd.AddCommand(newDomainListCmd())
	cmd.AddCommand(newDomainRemoveCmd())

	return cmd
}

func newDomainAddCmd() *cobra.Command {
	var cfToken string

	cmd := &cobra.Command{
		Use:   "add <zone>",
		Short: "Register a domain zone for port expose",
		Long:  "Register a Cloudflare-managed domain zone. Requires a Cloudflare API token with DNS and Tunnel permissions.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			zone := strings.TrimSpace(args[0])
			if zone == "" {
				return fmt.Errorf("zone is required")
			}

			if cfToken == "" {
				return fmt.Errorf("--cf-token is required\n\nCreate a token at https://dash.cloudflare.com/profile/api-tokens with:\n  - Zone:DNS:Edit\n  - Zone:Zone:Read\n  - Account:Cloudflare Tunnel:Edit")
			}

			client, err := apiClient()
			if err != nil {
				return err
			}

			resp, err := client.AddDomain(zone, cfToken)
			if err != nil {
				return err
			}

			fmt.Printf("Domain registered!\n")
			fmt.Printf("  ID:      %s\n", resp.DomainID)
			fmt.Printf("  Zone:    %s\n", resp.Zone)
			fmt.Printf("  Zone ID: %s\n", resp.CFZoneID)
			fmt.Printf("  State:   %s\n", resp.State)
			fmt.Printf("\nYou can now use: port_expose(port=8080, domain=\"app.%s\")\n", resp.Zone)

			return nil
		},
	}

	cmd.Flags().StringVar(&cfToken, "cf-token", "", "Cloudflare API token (DNS + Tunnel permissions)")
	cmd.MarkFlagRequired("cf-token")

	return cmd
}

func newDomainListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List registered domains",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := apiClient()
			if err != nil {
				return err
			}

			resp, err := client.ListDomains()
			if err != nil {
				return err
			}

			if len(resp.Domains) == 0 {
				fmt.Println("No domains registered. Use 'agend domain add' to register one.")
				return nil
			}

			for _, d := range resp.Domains {
				fmt.Printf("%s  zone=%s  state=%s  created=%s\n", d.DomainID, d.Zone, d.State, d.CreatedAt)
			}

			return nil
		},
	}
}

func newDomainRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <domain-id>",
		Short: "Remove a registered domain",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := apiClient()
			if err != nil {
				return err
			}

			_, err = client.RemoveDomain(args[0])
			if err != nil {
				return err
			}

			fmt.Printf("Domain %s removed.\n", args[0])
			return nil
		},
	}
}
