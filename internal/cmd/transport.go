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

// Every retry uses the environment ID supplied to this command.
func callWithRetry(ctx context.Context, cmd *cobra.Command, idempotent bool, fn func(*agentgrpc.Client) error) error {
	id, err := cmd.Flags().GetString("env")
	if err != nil {
		return err
	}
	if err := validateEnvironmentID(id); err != nil {
		return err
	}
	api, err := apiClient()
	if err != nil {
		return err
	}
	target := environmentTarget{id: id, api: api}
	if err := target.resolve(ctx); err != nil {
		return err
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
		client, err := target.dial(ctx)
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
			if err := target.refresh(ctx); err != nil {
				log.Printf("refresh endpoint failed: %v", err)
			}
			staleBudget--
			if err := waitForRetry(ctx, backoff(attempt)); err != nil {
				return err
			}
			continue
		}

		err = fn(client)
		client.Close()
		if err == nil {
			return nil
		}

		lastErr = err
		cat := classifyErr(err)
		retrySafe := mutationRetrySafe(idempotent, err)

		// A non-idempotent op that reached the daemon may already have run;
		// don't loop back and re-execute it. Auth is the exception (an
		// Unauthenticated reply means it was rejected before running).
		if cat != errFatal && !retrySafe {
			return err
		}

		switch cat {
		case errFatal:
			return err

		case errAuth:
			if !retrySafe {
				return err
			}
			if auth.TokenExpired(storedToken) {
				return errSessionExpired
			}
			if authBudget == 0 {
				return err
			}
			authBudget--
			if rerr := target.reauth(ctx); rerr != nil {
				// Reauth failed — try a full re-resolve instead.
				if frerr := target.refresh(ctx); frerr != nil {
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
			if rerr := target.refresh(ctx); rerr != nil {
				log.Printf("refresh endpoint failed: %v", rerr)
			}
			// Cloudflare quick-tunnel DNS propagation can take several
			// seconds; back off progressively.
			if err := waitForRetry(ctx, backoff(attempt)); err != nil {
				return err
			}

		case errTransient:
			if transientBudget == 0 {
				return err
			}
			transientBudget--
			if err := waitForRetry(ctx, backoff(attempt)); err != nil {
				return err
			}
		}
	}

	if lastErr != nil {
		return fmt.Errorf("exhausted reconnect attempts: %w", lastErr)
	}
	return errors.New("exhausted reconnect attempts")
}

// mutationRetrySafe is deliberately stricter than auth classification. An
// idempotent call can always be retried, but a mutation can only be replayed
// when a typed gRPC Unauthenticated status proves agentd rejected it in the
// auth interceptor before invoking the handler.
func mutationRetrySafe(idempotent bool, err error) bool {
	return idempotent || recovery.IsUnauthenticated(err)
}

// backoff returns a simple linear-ish backoff duration for retry attempt n.
// Chosen to cover the typical window for cloudflared respawn + named-tunnel
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

func waitForRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
