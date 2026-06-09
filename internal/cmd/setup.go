package cmd

import (
	"fmt"
	"strings"

	addmcp "github.com/acolita/go-add-mcp"
	"github.com/spf13/cobra"
)

func newConfigCmd() *cobra.Command {
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "config [agent...]",
		Short: "Configure MCP for AI agents",
		Long: `Configure agend as an MCP server for your AI agents.

Without arguments, auto-detects installed agents and configures all of them:
  agend config

Configure specific agents:
  agend config claude-code cursor

Preview what would be configured:
  agend config --dry-run

Supported agents:
  claude-code, claude-desktop, cursor, windsurf, vscode, zed,
  jetbrains, cline, roo-code, gemini, amazon-q, codex, goose, continue`,
		ValidArgs: agentNames(),
		RunE: func(cmd *cobra.Command, args []string) error {
			server := addmcp.Server{
				Name:    "agend",
				Command: "agend",
				Args:    []string{"mcp"},
			}

			autoDetect := len(args) == 0
			var agents []addmcp.Agent

			if autoDetect {
				agents = addmcp.Detect()
				if len(agents) == 0 {
					fmt.Println("No supported agents detected.")
					fmt.Println("Install an agent or specify one explicitly: agend config claude-code")
					return nil
				}
				fmt.Printf("Detected: %s\n", joinAgents(agents))
			} else {
				for _, name := range args {
					agents = append(agents, addmcp.Agent(name))
				}
			}

			if dryRun {
				results := addmcp.Resolve(agents)
				for _, r := range results {
					if r.OK() {
						fmt.Printf("  %-16s %s\n", r.Agent, r.Path)
					} else if autoDetect && r.Path == "" {
						continue
					} else {
						fmt.Printf("  %-16s (skip) %v\n", r.Agent, r.Err)
					}
				}
				return nil
			}

			results := addmcp.Install(server, agents)

			var configured int
			var errs []string
			for _, r := range results {
				if r.OK() {
					fmt.Printf("  %-16s %s\n", r.Agent, r.Path)
					configured++
				} else if autoDetect && r.Path == "" {
					continue
				} else {
					errs = append(errs, fmt.Sprintf("  %-16s %v", r.Agent, r.Err))
				}
			}

			if len(errs) > 0 {
				fmt.Println()
				for _, e := range errs {
					fmt.Fprintln(cmd.ErrOrStderr(), e)
				}
				return fmt.Errorf("%d agent(s) failed", len(errs))
			}

			if autoDetect && configured == 0 {
				fmt.Println("No agents configured (detected agents may only support project-scoped config).")
			}

			return nil
		},
	}

	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would be configured without writing files")

	return cmd
}

func agentNames() []string {
	agents := addmcp.Agents()
	names := make([]string, len(agents))
	for i, a := range agents {
		names[i] = string(a)
	}
	return names
}

func joinAgents(agents []addmcp.Agent) string {
	names := make([]string, len(agents))
	for i, a := range agents {
		names[i] = string(a)
	}
	return strings.Join(names, ", ")
}
