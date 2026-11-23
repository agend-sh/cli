package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	agentgrpc "github.com/agend-sh/cli/internal/grpc"
	pb "github.com/agend-sh/cli/proto/agentd/v1"
)

func newPingCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ping",
		Short: "Ping the agentd daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			return callWithRetry(ctx, cmd, true, func(client *agentgrpc.Client) error {
				resp, err := client.Agent.Ping(ctx, &pb.PingRequest{})
				if err != nil {
					return fmt.Errorf("ping failed: %w", err)
				}
				fmt.Printf("agentd %s (uptime: %ds)\n", sanitizeRemote(resp.Version), resp.UptimeSeconds)
				return nil
			})
		},
	}

	requireEnvironment(cmd)

	return cmd
}
