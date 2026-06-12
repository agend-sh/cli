package cmd

import (
	"context"
	"fmt"
	"time"

	"github.com/agend-sh/cli/internal/api"
	"github.com/agend-sh/cli/internal/auth"
	agentgrpc "github.com/agend-sh/cli/internal/grpc"
	pb "github.com/agend-sh/cli/proto/agentd/v1"
	"github.com/spf13/cobra"
)

// waitTunnelReachable probes the endpoint until a real request routes through
// the Cloudflare tunnel, or the deadline passes. A freshly-created quick tunnel
// reports its URL well before the edge actually routes to it (cloudflared even
// says "it may take some time to be reachable"), so "endpoint is set" is not
// "endpoint works". We probe with an unauthenticated Ping — the daemon always
// allows Ping, so this confirms routing without consuming the one-time secret.
// Returns true once reachable. progress is called between attempts for UX.
func waitTunnelReachable(endpoint string, timeout time.Duration, progress func()) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		client, err := agentgrpc.Dial(ctx, endpoint, "", "")
		if err == nil {
			_, perr := client.Agent.Ping(ctx, &pb.PingRequest{})
			client.Close()
			cancel()
			if perr == nil {
				return true
			}
		} else {
			cancel()
		}
		progress()
		time.Sleep(2 * time.Second)
	}
	return false
}

func apiClient() (*api.Client, error) {
	token, err := auth.LoadToken()
	if err != nil {
		return nil, fmt.Errorf("not authenticated — run 'agend login' first")
	}
	return api.New(auth.LoadAPIURL(), token), nil
}

func newEnvCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "env",
		Short: "Manage environments",
	}

	cmd.AddCommand(newEnvListCmd())
	cmd.AddCommand(newEnvCreateCmd())
	cmd.AddCommand(newEnvStatusCmd())
	cmd.AddCommand(newEnvDeleteCmd())
	cmd.AddCommand(newEnvWakeCmd())

	return cmd
}

func newEnvListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List your environments",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := apiClient()
			if err != nil {
				return err
			}

			resp, err := client.ListEnvironments()
			if err != nil {
				return fmt.Errorf("list failed: %w", err)
			}

			if len(resp.Environments) == 0 {
				fmt.Println("No environments. Run 'agend env create' to start one.")
				return nil
			}

			for _, env := range resp.Environments {
				endpoint := env.Endpoint
				if endpoint == "" {
					endpoint = "-"
				}
				fmt.Printf("%-20s %-10s %-8s %s\n", env.EnvID, env.State, env.Tier, endpoint)
			}
			return nil
		},
	}
}

func newEnvCreateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "create",
		Short: "Create a new environment",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := apiClient()
			if err != nil {
				return err
			}

			fmt.Println("Provisioning environment...")

			resp, err := client.CreateEnvironment()
			if err != nil {
				return fmt.Errorf("create failed: %w", err)
			}

			// Save secret immediately (endpoint comes after tunnel is ready)
			if err := auth.SaveEnvironment(resp.EnvID, resp.Endpoint, resp.Secret); err != nil {
				return fmt.Errorf("save environment: %w", err)
			}

			// Poll until running with endpoint (tunnel takes a few seconds)
			endpoint := resp.Endpoint
			if endpoint == "" {
				fmt.Print("Waiting for tunnel...")
				for i := 0; i < 30; i++ {
					time.Sleep(1 * time.Second)
					fmt.Print(".")
					status, err := client.GetEnvironment(resp.EnvID)
					if err != nil {
						continue
					}
					if status.State == "running" && status.Endpoint != "" {
						endpoint = status.Endpoint
						break
					}
				}
				fmt.Println()
			}

			if endpoint == "" {
				fmt.Printf("Environment %s created but tunnel not ready yet.\n", resp.EnvID)
				fmt.Println("Run 'agend env status' to check when it's ready.")
				return nil
			}

			// Update stored endpoint with the real tunnel URL
			if err := auth.SaveEnvironment(resp.EnvID, endpoint, resp.Secret); err != nil {
				return fmt.Errorf("save environment: %w", err)
			}

			// The endpoint exists, but a fresh Cloudflare quick tunnel can take
			// up to a minute to actually start routing. Probe until it's
			// reachable so we don't tell the user "ready" before it works.
			fmt.Print("Waiting for tunnel to come online (this can take up to a minute)...")
			reachable := waitTunnelReachable(endpoint, 75*time.Second, func() { fmt.Print(".") })
			fmt.Println()

			if !reachable {
				fmt.Printf("Environment %s created — the tunnel is still warming up.\n", resp.EnvID)
				fmt.Printf("  Endpoint: %s\n", endpoint)
				fmt.Println("Give it a few more seconds, then run 'agend exec' or 'agend ping'.")
				return nil
			}

			fmt.Printf("Environment ready!\n")
			fmt.Printf("  ID:       %s\n", resp.EnvID)
			fmt.Printf("  Endpoint: %s\n", endpoint)
			fmt.Println()
			fmt.Println("Run 'agend config' to configure your AI agents.")
			return nil
		},
	}
}

// resolveEnvID returns the environment to act on: the first positional arg, or
// the stored current environment when none is given.
func resolveEnvID(args []string) (string, error) {
	if len(args) > 0 {
		return args[0], nil
	}
	envID, _, _, _, err := auth.LoadEnvironment()
	if err != nil || envID == "" {
		return "", fmt.Errorf("no environment — run 'agend env create' first")
	}
	return envID, nil
}

func newEnvStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status [env-id]",
		Short: "Show environment status",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := apiClient()
			if err != nil {
				return err
			}

			envID, err := resolveEnvID(args)
			if err != nil {
				return err
			}

			resp, err := client.GetEnvironment(envID)
			if err != nil {
				return fmt.Errorf("status failed: %w", err)
			}

			fmt.Printf("ID:          %s\n", resp.EnvID)
			fmt.Printf("State:       %s\n", resp.State)
			endpoint := resp.Endpoint
			if endpoint == "" {
				endpoint = "-"
			}
			fmt.Printf("Endpoint:    %s\n", endpoint)
			fmt.Printf("Tier:        %s\n", resp.Tier)
			fmt.Printf("Created:     %s\n", resp.CreatedAt)
			fmt.Printf("Last active: %s\n", resp.LastActive)

			// Update stored endpoint if it changed
			if resp.Endpoint != "" && resp.State == "running" {
				storedID, storedEndpoint, _, _, _ := auth.LoadEnvironment()
				if storedID == resp.EnvID && storedEndpoint != resp.Endpoint {
					auth.SaveEnvironment(resp.EnvID, resp.Endpoint, "")
				}
			}
			return nil
		},
	}
}

func newEnvDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete [env-id]",
		Short: "Permanently delete an environment and release all resources",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := apiClient()
			if err != nil {
				return err
			}

			envID, err := resolveEnvID(args)
			if err != nil {
				return err
			}

			resp, err := client.StopEnvironment(envID)
			if err != nil {
				return fmt.Errorf("delete failed: %w", err)
			}

			auth.ClearEnvironment()

			fmt.Printf("Environment %s deleted.\n", resp.EnvID)
			return nil
		},
	}
}

func newEnvWakeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "wake [env-id]",
		Short: "Wake a sleeping environment",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := apiClient()
			if err != nil {
				return err
			}

			envID, err := resolveEnvID(args)
			if err != nil {
				return err
			}

			fmt.Println("Waking environment...")

			resp, err := client.WakeEnvironment(envID)
			if err != nil {
				return fmt.Errorf("wake failed: %w", err)
			}

			if err := auth.SaveEnvironment(resp.EnvID, resp.Endpoint, resp.Secret); err != nil {
				return fmt.Errorf("save environment: %w", err)
			}

			// Poll until running with endpoint
			endpoint := resp.Endpoint
			if endpoint == "" {
				fmt.Print("Waiting for tunnel...")
				for i := 0; i < 30; i++ {
					time.Sleep(1 * time.Second)
					fmt.Print(".")
					status, err := client.GetEnvironment(envID)
					if err != nil {
						continue
					}
					if status.State == "running" && status.Endpoint != "" {
						endpoint = status.Endpoint
						break
					}
				}
				fmt.Println()
			}

			if endpoint != "" {
				auth.SaveEnvironment(resp.EnvID, endpoint, resp.Secret)
				fmt.Printf("Environment awake!\n")
				fmt.Printf("  Endpoint: %s\n", endpoint)
			} else {
				fmt.Printf("Environment waking — run 'agend env status' to check.\n")
			}
			return nil
		},
	}
}
