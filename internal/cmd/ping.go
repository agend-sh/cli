package cmd

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	agentgrpc "github.com/agend-sh/cli/internal/grpc"
	pb "github.com/agend-sh/cli/proto/agentd/v1"
)

func newPingCmd() *cobra.Command {
	var addr string

	cmd := &cobra.Command{
		Use:   "ping",
		Short: "Ping the agentd daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			return callWithRetry(ctx, cmd, addr, func(client *agentgrpc.Client) error {
				resp, err := client.Agent.Ping(ctx, &pb.PingRequest{})
				if err != nil {
					return fmt.Errorf("ping failed: %w", err)
				}
				fmt.Printf("agentd %s (uptime: %ds)\n", resp.Version, resp.UptimeSeconds)
				return nil
			})
		},
	}

	cmd.Flags().StringVar(&addr, "addr", "localhost:50051", "agentd address")

	return cmd
}
