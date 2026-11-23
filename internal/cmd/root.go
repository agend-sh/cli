package cmd

import "github.com/spf13/cobra"

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
	root.AddCommand(newConnectCmd())
	root.AddCommand(newWatchCmd())
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
