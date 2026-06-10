package cmd

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/agend-sh/cli/internal/auth"
	agentgrpc "github.com/agend-sh/cli/internal/grpc"
)

// dialDaemon connects to agentd with auth. If addr is the default and a stored
// environment exists, it uses the stored endpoint, secret, and session token.
func dialDaemon(ctx context.Context, cmd *cobra.Command, addr string) (*agentgrpc.Client, error) {
	secret := ""
	sessionToken := ""
	if !cmd.Flags().Changed("addr") {
		_, endpoint, s, st, err := auth.LoadEnvironment()
		if err == nil && endpoint != "" {
			addr = endpoint
		}
		if err == nil {
			secret = s
			sessionToken = st
		}
	}
	client, err := agentgrpc.Dial(ctx, addr, secret, sessionToken)
	if err != nil {
		return nil, err
	}
	// Persist session token so subsequent CLI invocations reuse it
	client.OnTokenReceived = func(token string) {
		_ = auth.SaveSessionToken(token)
	}
	return client, nil
}

func NewRoot(version string) *cobra.Command {
	// Clean up stale .old binary from a previous update (Windows compat)
	cleanupOldBinary()

	root := &cobra.Command{
		Use:   "agend",
		Short: "agend — configure AI agent environments",
		Long:  "CLI for managing agend environments and configuring MCP access for AI agents.",
		CompletionOptions: cobra.CompletionOptions{
			HiddenDefaultCmd: true,
		},
		SilenceUsage: true,
		Version:      version,
	}

	root.AddCommand(newSignupCmd())
	root.AddCommand(newLoginCmd())
	root.AddCommand(newAccountCmd())
	root.AddCommand(newConfigCmd())
	root.AddCommand(newStatusCmd())
	root.AddCommand(newLogoutCmd())
	root.AddCommand(newPingCmd())
	root.AddCommand(newExecCmd())
	root.AddCommand(newFileGetCmd())
	root.AddCommand(newFilePutCmd())
	root.AddCommand(newFileMoveCmd())
	root.AddCommand(newTaskOutputCmd())
	root.AddCommand(newTaskStopCmd())
	root.AddCommand(newInputCmd())
	root.AddCommand(newInterruptCmd())
	root.AddCommand(newEnvCmd())
	root.AddCommand(newDomainCmd())
	root.AddCommand(newMCPCmd(version))
	root.AddCommand(newVersionCmd(version))
	root.AddCommand(newUpdateCmd(version))

	return root
}
