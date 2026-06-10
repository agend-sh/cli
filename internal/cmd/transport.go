package cmd

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/spf13/cobra"

	"github.com/agend-sh/cli/internal/auth"
	agentgrpc "github.com/agend-sh/cli/internal/grpc"
	"github.com/agend-sh/cli/internal/recovery"
)

// errorCategory and classifyErr delegate to internal/recovery — the single
// classifier shared with the MCP bridge. classifyErr takes the error so it can
// classify on the gRPC status code when present, falling back to the message.
type errorCategory = recovery.Category

const (
	errTransient     = recovery.Transient
	errStaleEndpoint = recovery.StaleEndpoint
	errAuth          = recovery.Auth
	errFatal         = recovery.Fatal
)

func classifyErr(err error) errorCategory {
	return recovery.Classify(err)
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
// idempotent reports whether fn is safe to re-execute after it may already
// have run on the daemon. Dial failures (fn never ran) are always retried
// regardless; only the post-call error path consults this.
func callWithRetry(ctx context.Context, cmd *cobra.Command, addr string, idempotent bool, fn func(*agentgrpc.Client) error) error {
	// Direct-address mode: no retry, no creds persistence.
	if cmd.Flags().Changed("addr") {
		client, err := dialDaemon(ctx, cmd, addr)
		if err != nil {
			return err
		}
		defer client.Close()
		return fn(client)
	}

	// Recovery (endpoint refresh / reauth) needs a valid control-plane token.
	// If the stored JWT has already expired, those steps can only 401, so
	// retrying just hangs for tens of seconds. Recognise it and fail fast with
	// an actionable message. (The happy path — fresh endpoint + valid session
	// token — never reaches recovery, so a valid session isn't blocked.)
	storedToken, _ := auth.LoadToken()
	errSessionExpired := errors.New("session expired — run 'agend login' to re-authenticate")

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
			cat := classifyErr(err)
			if cat == errFatal {
				return err
			}
			if auth.TokenExpired(storedToken) {
				return errSessionExpired // recovery can't succeed; don't loop
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
		cat := classifyErr(err)

		// A non-idempotent op that reached the daemon may already have run;
		// don't loop back and re-execute it. Auth is the exception (an
		// Unauthenticated reply means it was rejected before running).
		if !idempotent && cat != errFatal && cat != errAuth {
			// Refresh the endpoint so the *next* CLI invocation is healthy,
			// then surface the original error.
			if rerr := refreshEndpoint(); rerr != nil {
				log.Printf("refresh endpoint failed: %v", rerr)
			}
			return err
		}

		switch cat {
		case errFatal:
			return err

		case errAuth:
			if auth.TokenExpired(storedToken) {
				return errSessionExpired
			}
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
			if auth.TokenExpired(storedToken) {
				return errSessionExpired
			}
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
