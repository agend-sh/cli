package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

// newTeamCmd is the `agend team` command group (ADR-020): create teams, manage
// membership, and manage the team's pool of shared environments.
func newTeamCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "team",
		Short: "Manage teams and shared environments",
	}
	cmd.AddCommand(
		newTeamCreateCmd(),
		newTeamListCmd(),
		newTeamInviteCmd(),
		newTeamAcceptCmd(),
		newTeamMembersCmd(),
		newTeamEnvsCmd(),
		newTeamEnvCreateCmd(),
	)
	return cmd
}

func newTeamCreateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "create <name>",
		Short: "Create a team (you become the owner)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := apiClient()
			if err != nil {
				return err
			}
			t, err := client.CreateTeam(args[0])
			if err != nil {
				return err
			}
			fmt.Printf("Team created: %s (%s)\n", t.Name, t.TeamID)
			fmt.Printf("Invite members:  agend team invite %s <email>\n", t.TeamID)
			return nil
		},
	}
}

func newTeamListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List the teams you belong to",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := apiClient()
			if err != nil {
				return err
			}
			r, err := client.ListTeams()
			if err != nil {
				return err
			}
			if len(r.Teams) == 0 {
				fmt.Println("You're not on any teams. Create one:  agend team create <name>")
				return nil
			}
			for _, t := range r.Teams {
				role := t.Role
				if t.IsOwner {
					role = "owner"
				}
				fmt.Printf("%-22s  %-20s  %-7s  %s\n", t.TeamID, t.Name, role, t.Status)
			}
			return nil
		},
	}
}

func newTeamInviteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "invite <team-id> <email>",
		Short: "Invite a user (by email) to a team you own",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := apiClient()
			if err != nil {
				return err
			}
			if err := client.InviteMember(args[0], args[1]); err != nil {
				return err
			}
			fmt.Printf("Invited %s — they accept with:  agend team accept %s\n", args[1], args[0])
			return nil
		},
	}
}

func newTeamAcceptCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "accept <team-id>",
		Short: "Accept a team invite",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := apiClient()
			if err != nil {
				return err
			}
			if err := client.AcceptInvite(args[0]); err != nil {
				return err
			}
			fmt.Printf("Joined the team. List shared envs:  agend team envs %s\n", args[0])
			return nil
		},
	}
}

func newTeamMembersCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "members <team-id>",
		Short: "List team members",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := apiClient()
			if err != nil {
				return err
			}
			r, err := client.ListMembers(args[0])
			if err != nil {
				return err
			}
			for _, m := range r.Members {
				fmt.Printf("%-32s  %-7s  %s\n", m.Email, m.Role, m.Status)
			}
			return nil
		},
	}
}

func newTeamEnvsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "envs <team-id>",
		Short: "List the team's shared environments and who holds each",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := apiClient()
			if err != nil {
				return err
			}
			r, err := client.ListTeamEnvironments(args[0])
			if err != nil {
				return err
			}
			if len(r.Environments) == 0 {
				fmt.Printf("No shared envs yet. Create one:  agend team env-create %s\n", args[0])
				return nil
			}
			for _, e := range r.Environments {
				held := "free"
				if e.LeasedByEmail != "" {
					held = "in use by " + e.LeasedByEmail
				}
				fmt.Printf("%-22s  %-9s  %s\n", e.EnvID, e.State, held)
			}
			return nil
		},
	}
}

func newTeamEnvCreateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "env-create <team-id>",
		Short: "Create a new shared environment for the team",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := apiClient()
			if err != nil {
				return err
			}
			fmt.Println("Provisioning shared environment...")
			resp, err := client.CreateTeamEnvironment(args[0])
			if err != nil {
				return err
			}
			fmt.Printf("Created %s for the team.\n", resp.EnvID)
			fmt.Printf("Members check it out with:  agend env acquire %s\n", resp.EnvID)
			return nil
		},
	}
}
