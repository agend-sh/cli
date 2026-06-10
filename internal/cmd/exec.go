package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	agentgrpc "github.com/agend-sh/cli/internal/grpc"
	pb "github.com/agend-sh/cli/proto/agentd/v1"
)

func newExecCmd() *cobra.Command {
	var addr string
	var timeoutMs uint32
	var tailLines uint32
	var headLines uint32
	var background bool
	var interactive bool

	cmd := &cobra.Command{
		Use:   "exec [command...]",
		Short: "Execute a command in the remote environment",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			var resp *pb.ExecResponse
			err := callWithRetry(ctx, cmd, addr, false, func(client *agentgrpc.Client) error {
				r, err := client.Agent.Exec(ctx, &pb.ExecRequest{
					Command:         strings.Join(args, " "),
					TimeoutMs:       timeoutMs,
					TailLines:       tailLines,
					HeadLines:       headLines,
					RunInBackground: background,
					Interactive:     interactive,
				})
				if err != nil {
					return fmt.Errorf("exec failed: %w", err)
				}
				resp = r
				return nil
			})
			if err != nil {
				return err
			}

			if background {
				fmt.Printf("task_id: %s\n", resp.TaskId)
				return nil
			}

			if interactive {
				fmt.Printf("status: %s\n", sanitizeRemote(resp.Status))
				if resp.Screen != "" {
					fmt.Println(sanitizeRemote(resp.Screen))
				}
				if resp.PromptType != "" {
					fmt.Printf("prompt_type: %s\n", sanitizeRemote(resp.PromptType))
				}
				return nil
			}

			if resp.Stdout != "" {
				fmt.Println(sanitizeForTTY(resp.Stdout, os.Stdout))
			}
			if resp.Stderr != "" {
				fmt.Fprintln(os.Stderr, sanitizeForTTY(resp.Stderr, os.Stderr))
			}

			if resp.Status == "timeout" {
				fmt.Fprintln(os.Stderr, "(timed out)")
			}

			if resp.ExitCode != 0 {
				os.Exit(int(resp.ExitCode))
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&addr, "addr", "localhost:50051", "agentd address")
	cmd.Flags().Uint32Var(&timeoutMs, "timeout", 30000, "timeout in milliseconds")
	cmd.Flags().Uint32Var(&tailLines, "tail", 0, "return only last N lines")
	cmd.Flags().Uint32Var(&headLines, "head", 0, "return only first N lines")
	cmd.Flags().BoolVar(&background, "background", false, "run in background, return task_id")
	cmd.Flags().BoolVar(&interactive, "interactive", false, "interactive/PTY mode")

	return cmd
}
