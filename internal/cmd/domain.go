package cmd

import (
	"fmt"
	"os"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
	"golang.org/x/term"
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
	cmd := &cobra.Command{
		Use:   "add <zone>",
		Short: "Register a domain zone for port expose",
		Long: `Register a Cloudflare-managed domain zone. Requires a Cloudflare API token
with DNS and Tunnel permissions.

The token is read from the AGEND_CF_TOKEN environment variable, or prompted
for interactively. It is never accepted as a flag — argv is visible to every
process via ps.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			zone := strings.TrimSpace(args[0])
			if zone == "" {
				return fmt.Errorf("zone is required")
			}

			cfToken := os.Getenv("AGEND_CF_TOKEN")
			if cfToken == "" && term.IsTerminal(int(syscall.Stdin)) {
				fmt.Fprint(os.Stderr, "Cloudflare API token: ")
				tokenBytes, err := term.ReadPassword(int(syscall.Stdin))
				if err != nil {
					return fmt.Errorf("failed to read token: %w", err)
				}
				fmt.Fprintln(os.Stderr)
				cfToken = strings.TrimSpace(string(tokenBytes))
			}
			if cfToken == "" {
				return fmt.Errorf("a Cloudflare API token is required — set AGEND_CF_TOKEN\n\nCreate a token at https://dash.cloudflare.com/profile/api-tokens with:\n  - Zone:DNS:Edit\n  - Zone:Zone:Read\n  - Account:Cloudflare Tunnel:Edit")
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
