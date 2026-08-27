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
	envID := ""
	secret := ""
	sessionToken := ""
	if !cmd.Flags().Changed("addr") {
		storedEnvID, endpoint, s, st, err := auth.LoadEnvironment()
		if err == nil && endpoint != "" {
			addr = endpoint
			envID = storedEnvID
			secret = s
			sessionToken = st
		}
	}
	client, err := agentgrpc.Dial(ctx, addr, secret, sessionToken)
	if err != nil {
		return nil, err
	}
	// Persist only if this exact environment credential is still current. An
	// overlapping credential change wins over a late callback from this client.
	if envID != "" {
		client.OnTokenReceived = func(token string) {
			_, _ = auth.SaveSessionTokenForEnvironment(envID, secret, sessionToken, token)
		}
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
	root.AddCommand(newResizeCmd())
	root.AddCommand(newInterruptCmd())
	root.AddCommand(newEnvCmd())
	root.AddCommand(newProfilesCmd())
	root.AddCommand(newTeamCmd())
	root.AddCommand(newDomainCmd())
	root.AddCommand(newMCPCmd(version))
	root.AddCommand(newVersionCmd(version))
	root.AddCommand(newUpdateCmd(version))

	return root
}
