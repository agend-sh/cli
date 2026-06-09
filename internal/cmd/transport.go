package cmd

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/agend-sh/cli/internal/auth"
	agentgrpc "github.com/agend-sh/cli/internal/grpc"
)

// errorCategory mirrors the MCP classifier so direct CLI subcommands get
// the same auto-recovery behaviour the MCP bridge has always had.
type errorCategory int

const (
	errTransient errorCategory = iota
	errStaleEndpoint
	errAuth
	errFatal
)

func classifyErr(msg string) errorCategory {
	t := strings.ToLower(msg)

	// Client-side cancellation — don't retry.
	if strings.Contains(t, "context canceled") || strings.Contains(t, "context deadline exceeded") {
		return errFatal
	}
	// Fatal gRPC statuses.
	if strings.Contains(t, "not found") && strings.Contains(t, "404") {
		return errFatal
	}
	if strings.Contains(t, "forbidden") || strings.Contains(t, "403") {
		return errFatal
	}
	// Port_expose misconfiguration — retries will never fix these. Return
	// immediately so the caller sees a clean, actionable error.
	if strings.Contains(t, "does not belong to cloudflare zone") {
		return errFatal
	}
	if strings.Contains(t, "already bound: cname points to") {
		return errFatal
	}
	// Auth — reauth flow needed.
	if strings.Contains(t, "unauthenticated") {
		return errAuth
	}
	if strings.Contains(t, "invalid session token") {
		return errAuth
	}
	if strings.Contains(t, "401") && !strings.Contains(t, "websocket") {
		return errAuth
	}
	// Stale endpoint — re-resolve via control plane.
	if strings.Contains(t, "status code 530") {
		return errStaleEndpoint
	}
	if strings.Contains(t, "no such host") {
		return errStaleEndpoint
	}
	if strings.Contains(t, "connection refused") {
		return errStaleEndpoint
	}
	if strings.Contains(t, "tunnel connection") && strings.Contains(t, "failed") {
		return errStaleEndpoint
	}
	if strings.Contains(t, "may be stopped or expired") {
		return errStaleEndpoint
	}
	if strings.Contains(t, "environment unreachable") {
		return errStaleEndpoint
	}
	// Transient — same endpoint, wait and retry.
	if strings.Contains(t, "unavailable") {
		return errTransient
	}
	if strings.Contains(t, "status code 502") || strings.Contains(t, "status code 503") {
		return errTransient
	}
	if strings.Contains(t, "i/o timeout") {
		return errTransient
	}
	if strings.Contains(t, "connection reset") {
		return errTransient
	}
	if strings.Contains(t, "deadline exceeded") {
		return errTransient
	}
	if strings.Contains(t, "unreachable") {
		return errTransient
	}
	// Default: assume stale — the safest recovery, since fatal cases are
	// explicitly listed above.
	return errStaleEndpoint
}

// callWithRetry runs fn against a freshly-dialed agentd client, with
// automatic:
//
//   - reauth on expired session token / Unauthenticated errors
//   - endpoint re-resolve on tunnel URL rotation (stale endpoint errors)
//   - backoff retry on transient errors
//
// If --addr is explicitly set by the user, retries are suppressed (we
// assume the user knows what they're pointing at).
//
// Fresh credentials obtained during recovery are persisted to disk so
// subsequent CLI invocations start from a good state.
func callWithRetry(ctx context.Context, cmd *cobra.Command, addr string, fn func(*agentgrpc.Client) error) error {
	// Direct-address mode: no retry, no creds persistence.
	if cmd.Flags().Changed("addr") {
		client, err := dialDaemon(ctx, cmd, addr)
		if err != nil {
			return err
		}
		defer client.Close()
		return fn(client)
	}

	// Bounded retry budgets per category. The outer loop bounds total
	// attempts so a persistently-failing env doesn't hang forever.
	var (
		maxTotal        = 6
		authBudget      = 1
		staleBudget     = 3
		transientBudget = 3
	)

	var lastErr error
	for attempt := 1; attempt <= maxTotal; attempt++ {
		client, err := dialDaemon(ctx, cmd, addr)
		if err != nil {
			// A dial failure before any RPC is almost always a stale
			// endpoint or a transient connection problem.
			lastErr = err
			cat := classifyErr(err.Error())
			if cat == errFatal {
				return err
			}
			if staleBudget == 0 && transientBudget == 0 {
				return err
			}
			if err := refreshEndpoint(); err != nil {
				log.Printf("refresh endpoint failed: %v", err)
			}
			staleBudget--
			time.Sleep(backoff(attempt))
			continue
		}

		err = fn(client)
		client.Close()
		if err == nil {
			return nil
		}

		lastErr = err
		cat := classifyErr(err.Error())
		switch cat {
		case errFatal:
			return err

		case errAuth:
			if authBudget == 0 {
				return err
			}
			authBudget--
			if rerr := reauthEnvironment(); rerr != nil {
				// Reauth failed — try a full re-resolve instead.
				if frerr := refreshEndpoint(); frerr != nil {
					return err
				}
			}
			// Don't sleep on reauth — the new creds are usable immediately.

		case errStaleEndpoint:
			if staleBudget == 0 {
				return err
			}
			staleBudget--
			if rerr := refreshEndpoint(); rerr != nil {
				log.Printf("refresh endpoint failed: %v", rerr)
			}
			// Cloudflare quick-tunnel DNS propagation can take several
			// seconds; back off progressively.
			time.Sleep(backoff(attempt))

		case errTransient:
			if transientBudget == 0 {
				return err
			}
			transientBudget--
			time.Sleep(backoff(attempt))
		}
	}

	if lastErr != nil {
		return fmt.Errorf("exhausted reconnect attempts: %w", lastErr)
	}
	return errors.New("exhausted reconnect attempts")
}

// backoff returns a simple linear-ish backoff duration for retry attempt n.
// Chosen to cover the typical window for cloudflared respawn + trycloudflare
// DNS propagation (~15-20s).
func backoff(attempt int) time.Duration {
	switch attempt {
	case 1:
		return 1 * time.Second
	case 2:
		return 3 * time.Second
	case 3:
		return 6 * time.Second
	default:
		return 9 * time.Second
	}
}

// refreshEndpoint fetches the current endpoint for the stored env from
// the control plane and persists it. Preserves the existing secret
// because endpoint rotation alone does not invalidate the gRPC session.
func refreshEndpoint() error {
	envID, _, secret, _, err := auth.LoadEnvironment()
	if err != nil || envID == "" {
		return fmt.Errorf("no active environment in credentials")
	}
	api, err := apiClient()
	if err != nil {
		return err
	}
	env, err := api.GetEnvironment(envID)
	if err != nil {
		return err
	}
	if env.Endpoint == "" {
		return fmt.Errorf("env %s has no endpoint (state=%s)", envID, env.State)
	}
	return auth.SaveEnvironment(envID, env.Endpoint, secret)
}

// reauthEnvironment rotates the one-time secret via the control plane
// and persists it. Also clears any cached session token, since the new
// secret invalidates all previous sessions.
func reauthEnvironment() error {
	envID, endpoint, _, _, err := auth.LoadEnvironment()
	if err != nil || envID == "" {
		return fmt.Errorf("no active environment in credentials")
	}
	api, err := apiClient()
	if err != nil {
		return err
	}
	resp, err := api.ReauthEnvironment(envID)
	if err != nil {
		return err
	}
	if err := auth.SaveEnvironment(envID, endpoint, resp.Secret); err != nil {
		return err
	}
	return auth.ClearSessionToken()
}
