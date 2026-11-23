package cmd

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/agend-sh/cli/internal/api"
	"github.com/agend-sh/cli/internal/auth"
	"github.com/agend-sh/cli/internal/mcp"
)

// leaseHeartbeatInterval keeps a team-env lease alive well within its TTL
// (5 min server-side) so a long MCP session doesn't lose the env.
const leaseHeartbeatInterval = 2 * time.Minute

func newMCPCmd(version string) *cobra.Command {
	var leaseEnv string
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

			// SIGTERM never fires on Windows (TerminateProcess is not
			// catchable); the shutdown path there is the MCP host closing
			// stdin → server.Run returns on EOF → deferred releaseLease runs.
			// A hard kill leaks the lease until its server-side TTL, same as
			// SIGKILL on unix.
			go func() {
				sig := make(chan os.Signal, 1)
				signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
				<-sig
				cancel()
			}()

			// Lease only a team environment explicitly requested by this MCP host.
			if cmd.Flags().Changed("env") {
				if err := validateEnvironmentID(leaseEnv); err != nil {
					return err
				}
				leased, aerr := acquireLease(ctx, apiClient, leaseEnv)
				if aerr != nil {
					return aerr
				}
				if leased {
					defer releaseLease(apiClient, leaseEnv)
				}
			}

			server := mcp.NewServer(apiClient, version)
			return server.Run(ctx)
		},
	}

	cmd.Flags().StringVar(&leaseEnv, "env", "", "acquire and maintain a lease for this team environment")
	return cmd
}

// acquireLease leases a team env for this MCP session. Returns (true, nil) and
// starts a background heartbeat if a lease was taken; (false, nil) for a
// personal env (HTTP 400) or a transient error (best-effort: serve unleased);
// and (false, err) when the env is held by another member (HTTP 409), so the
// command aborts with a clear message rather than serving a stale secret.
func acquireLease(ctx context.Context, c *api.Client, envID string) (bool, error) {
	r, err := c.AcquireEnvironment(envID)
	if err != nil {
		var apiErr *api.APIError
		if errors.As(err, &apiErr) {
			if apiErr.StatusCode == 400 {
				return false, nil // not a team env — no lease needed
			}
			if apiErr.StatusCode == 409 {
				return false, fmt.Errorf("%s — wait until it's free or use another team env", apiErr.Message)
			}
		}
		log.Printf("agend mcp: could not acquire lease on %s (%v) — continuing unleased", envID, err)
		return false, nil
	}
	// Save the rotated secret so the MCP→agentd connection authenticates as us.
	if err := auth.SaveEnvironment(r.EnvID, r.Endpoint, r.Secret); err != nil {
		log.Printf("agend mcp: save leased env failed: %v", err)
	}
	log.Printf("agend mcp: leased shared env %s (until %s)", envID, r.LeaseExpiry)
	go heartbeatLease(ctx, c, envID)
	return true, nil
}

func heartbeatLease(ctx context.Context, c *api.Client, envID string) {
	t := time.NewTicker(leaseHeartbeatInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := c.HeartbeatEnvironment(envID); err != nil {
				log.Printf("agend mcp: lease heartbeat failed for %s: %v", envID, err)
			}
		}
	}
}

func releaseLease(c *api.Client, envID string) {
	if err := c.ReleaseEnvironment(envID); err != nil {
		log.Printf("agend mcp: release failed for %s: %v", envID, err)
	} else {
		log.Printf("agend mcp: released shared env %s", envID)
	}
}
