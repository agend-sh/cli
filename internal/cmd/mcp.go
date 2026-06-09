package cmd

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/agend-sh/cli/internal/api"
	"github.com/agend-sh/cli/internal/auth"
	"github.com/agend-sh/cli/internal/mcp"
)

func newMCPCmd(version string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Run as MCP server (stdio transport), bridging tool calls to agentd via gRPC",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Redirect logs to stderr — stdout is the MCP transport
			log.SetOutput(os.Stderr)

			token, err := auth.LoadToken()
			if err != nil {
				return fmt.Errorf("not authenticated — run 'agend login' first")
			}

			apiClient := api.New(auth.LoadAPIURL(), token)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			go func() {
				sig := make(chan os.Signal, 1)
				signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
				<-sig
				cancel()
			}()

			server := mcp.NewServer(apiClient, version)
			return server.Run(ctx)
		},
	}

	return cmd
}
