package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	agentgrpc "github.com/agend-sh/cli/internal/grpc"
	pb "github.com/agend-sh/cli/proto/agentd/v1"
)

func newFileGetCmd() *cobra.Command {
	var addr string
	var encoding string

	cmd := &cobra.Command{
		Use:   "file-get <path>",
		Short: "Read a file from the remote environment",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			var resp *pb.FileGetResponse
			err := callWithRetry(ctx, cmd, addr, func(client *agentgrpc.Client) error {
				r, err := client.Agent.FileGet(ctx, &pb.FileGetRequest{
					Path:     args[0],
					Encoding: encoding,
				})
				if err != nil {
					return fmt.Errorf("file-get failed: %w", err)
				}
				resp = r
				return nil
			})
			if err != nil {
				return err
			}
			fmt.Print(sanitizeForTTY(resp.Content, os.Stdout))
			fmt.Fprintf(os.Stderr, "\n--- size: %d, mode: %s, sha256: %s ---\n", resp.Size, sanitizeRemote(resp.Mode), sanitizeRemote(resp.Checksum))
			return nil
		},
	}

	cmd.Flags().StringVar(&addr, "addr", "localhost:50051", "agentd address")
	cmd.Flags().StringVar(&encoding, "encoding", "text", "encoding: text or base64")

	return cmd
}

func newFilePutCmd() *cobra.Command {
	var addr string
	var encoding string
	var mode string
	var createDirs bool
	var overwrite bool

	cmd := &cobra.Command{
		Use:   "file-put <path> <content>",
		Short: "Write a file to the remote environment",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			var resp *pb.FilePutResponse
			err := callWithRetry(ctx, cmd, addr, func(client *agentgrpc.Client) error {
				r, err := client.Agent.FilePut(ctx, &pb.FilePutRequest{
					Path:       args[0],
					Content:    args[1],
					Encoding:   encoding,
					Mode:       mode,
					CreateDirs: createDirs,
					Overwrite:  overwrite,
				})
				if err != nil {
					return fmt.Errorf("file-put failed: %w", err)
				}
				resp = r
				return nil
			})
			if err != nil {
				return err
			}
			fmt.Printf("written %d bytes, sha256: %s\n", resp.Size, resp.Checksum)
			return nil
		},
	}

	cmd.Flags().StringVar(&addr, "addr", "localhost:50051", "agentd address")
	cmd.Flags().StringVar(&encoding, "encoding", "text", "encoding: text or base64")
	cmd.Flags().StringVar(&mode, "mode", "0644", "file permissions")
	cmd.Flags().BoolVar(&createDirs, "create-dirs", false, "create parent directories")
	cmd.Flags().BoolVar(&overwrite, "overwrite", false, "overwrite existing file")

	return cmd
}

func newFileMoveCmd() *cobra.Command {
	var addr string

	cmd := &cobra.Command{
		Use:   "file-move <source> <destination>",
		Short: "Move or rename a file in the remote environment",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			return callWithRetry(ctx, cmd, addr, func(client *agentgrpc.Client) error {
				if _, err := client.Agent.FileMove(ctx, &pb.FileMoveRequest{
					Source:      args[0],
					Destination: args[1],
				}); err != nil {
					return fmt.Errorf("file-move failed: %w", err)
				}
				fmt.Printf("moved %s -> %s\n", args[0], args[1])
				return nil
			})
		},
	}

	cmd.Flags().StringVar(&addr, "addr", "localhost:50051", "agentd address")

	return cmd
}
