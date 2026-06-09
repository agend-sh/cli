package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	agentgrpc "github.com/agend-sh/cli/internal/grpc"
	pb "github.com/agend-sh/cli/proto/agentd/v1"
)

func newTaskOutputCmd() *cobra.Command {
	var addr string

	cmd := &cobra.Command{
		Use:   "task-output <task-id>",
		Short: "Get output of a background task",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			var resp *pb.TaskOutputResponse
			err := callWithRetry(ctx, cmd, addr, func(client *agentgrpc.Client) error {
				r, err := client.Agent.TaskOutput(ctx, &pb.TaskOutputRequest{
					TaskId: args[0],
				})
				if err != nil {
					return fmt.Errorf("task-output failed: %w", err)
				}
				resp = r
				return nil
			})
			if err != nil {
				return err
			}

			fmt.Printf("status: %s\n", resp.Status)
			if resp.Status == "completed" {
				fmt.Printf("exit_code: %d\n", resp.ExitCode)
			}
			if resp.Stdout != "" {
				fmt.Println(resp.Stdout)
			}
			if resp.Stderr != "" {
				fmt.Fprintln(os.Stderr, resp.Stderr)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&addr, "addr", "localhost:50051", "agentd address")

	return cmd
}

func newTaskStopCmd() *cobra.Command {
	var addr string

	cmd := &cobra.Command{
		Use:   "task-stop <task-id>",
		Short: "Stop a running background task",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			return callWithRetry(ctx, cmd, addr, func(client *agentgrpc.Client) error {
				if _, err := client.Agent.TaskStop(ctx, &pb.TaskStopRequest{
					TaskId: args[0],
				}); err != nil {
					return fmt.Errorf("task-stop failed: %w", err)
				}
				fmt.Println("task stopped")
				return nil
			})
		},
	}

	cmd.Flags().StringVar(&addr, "addr", "localhost:50051", "agentd address")

	return cmd
}
