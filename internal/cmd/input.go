package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	agentgrpc "github.com/agend-sh/cli/internal/grpc"
	pb "github.com/agend-sh/cli/proto/agentd/v1"
)

func newInputCmd() *cobra.Command {
	var addr string

	cmd := &cobra.Command{
		Use:   "input <text>",
		Short: "Send text input to a process waiting for input (newline appended)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			var resp *pb.InputResponse
			err := callWithRetry(ctx, cmd, addr, func(client *agentgrpc.Client) error {
				r, err := client.Agent.Input(ctx, &pb.InputRequest{
					Input: args[0],
				})
				if err != nil {
					return fmt.Errorf("input failed: %w", err)
				}
				resp = r
				return nil
			})
			if err != nil {
				return err
			}

			fmt.Printf("status: %s\n", sanitizeRemote(resp.Status))
			if resp.Stdout != "" {
				fmt.Println(sanitizeForTTY(resp.Stdout, os.Stdout))
			}
			if resp.Stderr != "" {
				fmt.Fprintln(os.Stderr, sanitizeForTTY(resp.Stderr, os.Stderr))
			}
			if resp.Status == "completed" {
				fmt.Printf("exit_code: %d\n", resp.ExitCode)
			}
			if resp.PromptType != "" {
				fmt.Printf("prompt_type: %s\n", sanitizeRemote(resp.PromptType))
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&addr, "addr", "localhost:50051", "agentd address")

	return cmd
}

func newInterruptCmd() *cobra.Command {
	var addr string

	cmd := &cobra.Command{
		Use:   "interrupt",
		Short: "Send SIGINT to the active session",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			return callWithRetry(ctx, cmd, addr, func(client *agentgrpc.Client) error {
				resp, err := client.Agent.Interrupt(ctx, &pb.InterruptRequest{})
				if err != nil {
					return fmt.Errorf("interrupt failed: %w", err)
				}
				fmt.Printf("status: %s\n", sanitizeRemote(resp.Status))
				return nil
			})
		},
	}

	cmd.Flags().StringVar(&addr, "addr", "localhost:50051", "agentd address")

	return cmd
}
