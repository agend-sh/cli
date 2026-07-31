package cmd

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	agentgrpc "github.com/agend-sh/cli/internal/grpc"
)

func parsePTYDimension(value, name string) (uint32, error) {
	parsed, err := strconv.ParseUint(value, 10, 32)
	if err != nil || parsed == 0 || parsed > uint64(agentgrpc.MaxPTYDimension) {
		return 0, fmt.Errorf("%s must be an integer between 1 and %d", name, agentgrpc.MaxPTYDimension)
	}
	return uint32(parsed), nil
}

func newResizeCmd() *cobra.Command {
	var addr string

	cmd := &cobra.Command{
		Use:   "resize <columns> <rows>",
		Short: "Resize the active interactive session",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			columns, err := parsePTYDimension(args[0], "columns")
			if err != nil {
				return err
			}
			rows, err := parsePTYDimension(args[1], "rows")
			if err != nil {
				return err
			}

			ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
			defer cancel()
			if err := callWithRetry(ctx, cmd, addr, true, func(client *agentgrpc.Client) error {
				return client.Resize(ctx, columns, rows)
			}); err != nil {
				return fmt.Errorf("resize failed: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "resized: %dx%d\n", columns, rows)
			return nil
		},
	}

	cmd.Flags().StringVar(&addr, "addr", "localhost:50051", "agentd address")
	return cmd
}
