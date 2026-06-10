package mcp

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/agend-sh/cli/internal/api"
	"github.com/agend-sh/cli/internal/auth"
	agentgrpc "github.com/agend-sh/cli/internal/grpc"
)

// ConnState tracks the lifecycle of a connection to an environment.
type ConnState int

const (
	StateDisconnected ConnState = iota
	StateResolving
	StateConnecting
	StateConnected
	StateRetrying
)

// EnvConn manages the connection to a single environment.
// It handles resolving endpoints, waking sleeping envs, connecting
// via WebSocket/gRPC, retrying transient errors, and reconnecting
// when endpoints go stale.
type EnvConn struct {
	mu           sync.Mutex
	envID        string
	state        ConnState
	client       *agentgrpc.Client
	endpoint     string
	secret       string // one-time secret (cleared after first auth → session token)
	sessionToken string // session token (set after first auth, reused on reconnect)

	apiClient *api.Client
	lastPing  time.Time
}

// ConnPool manages connections to multiple environments.
type ConnPool struct {
	mu        sync.RWMutex
	conns     map[string]*EnvConn
	apiClient *api.Client
}

func NewConnPool(apiClient *api.Client) *ConnPool {
	return &ConnPool{
		conns:     make(map[string]*EnvConn),
		apiClient: apiClient,
	}
}

// Get returns the connection for an environment, creating one if needed.
func (p *ConnPool) Get(envID string) *EnvConn {
	p.mu.Lock()
	defer p.mu.Unlock()

	if conn, ok := p.conns[envID]; ok {
		return conn
	}

	conn := &EnvConn{
		envID:     envID,
		state:     StateDisconnected,
		apiClient: p.apiClient,
	}
	p.conns[envID] = conn
	return conn
}

// CloseAll closes all connections in the pool.
func (p *ConnPool) CloseAll() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, conn := range p.conns {
		conn.close()
	}
}

// Reset closes all connections and replaces the API client.
// Used by reload_config when credentials change.
func (p *ConnPool) Reset(newAPI *api.Client) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, conn := range p.conns {
		conn.close()
	}
	p.conns = make(map[string]*EnvConn)
	p.apiClient = newAPI
}

// EnsureConnected drives the state machine until connected or fatal error.
// Called before every tool call.
func (c *EnvConn) EnsureConnected(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.state == StateConnected && c.client != nil {
		return nil
	}

	return c.resolveAndConnect(ctx)
}

// Execute runs a tool call with automatic retry and reconnect on failure.
//
// idempotent says whether fn is safe to transparently re-execute. A
// side-effecting op (shell_exec, file_upload, port_expose, …) that fails
// mid-call may already have run on the daemon, so we must NOT re-run it — we
// reconnect so the *next* call is healthy and surface the original error. Only
// read-only ops are retried in place.
func (c *EnvConn) Execute(ctx context.Context, idempotent bool, fn func(*agentgrpc.Client) (string, bool)) (string, bool) {
	if err := c.EnsureConnected(ctx); err != nil {
		return fmt.Sprintf("connection failed: %v", err), true
	}

	c.mu.Lock()
	client := c.client
	c.mu.Unlock()

	text, isErr := fn(client)
	if !isErr {
		return text, false
	}

	// Classify the error and decide what to do
	cat := classifyError(text)

	// For a non-idempotent op that already reached the daemon, don't re-run it.
	// Auth errors are the exception: an Unauthenticated reply proves the call
	// was rejected before executing, so reauth + one retry is safe.
	if !idempotent && cat != ErrFatal && cat != ErrAuth {
		log.Printf("[env:%s] non-idempotent op failed (%v); reconnecting for next call, not retrying", c.envID, cat)
		c.mu.Lock()
		c.close()
		_ = c.resolveAndConnect(ctx)
		c.mu.Unlock()
		return text, true
	}

	switch cat {
	case ErrFatal:
		return text, true

	case ErrAuth:
		// Try reauth API to get a fresh one-time secret
		log.Printf("[env:%s] auth error, attempting reauth...", c.envID)
		if reauthResp, reauthErr := c.apiClient.ReauthEnvironment(c.envID); reauthErr == nil {
			log.Printf("[env:%s] reauth succeeded, reconnecting with new secret", c.envID)
			c.mu.Lock()
			c.secret = reauthResp.Secret
			c.sessionToken = ""
			c.close()
			err := c.resolveAndConnect(ctx)
			c.mu.Unlock()
			if err != nil {
				return fmt.Sprintf("reconnect failed: %v (original: %s)", err, text), true
			}
		} else {
			// Reauth failed — fall back to re-resolve
			log.Printf("[env:%s] reauth failed (%v), falling back to re-resolve", c.envID, reauthErr)
			c.mu.Lock()
			c.close()
			err := c.resolveAndConnect(ctx)
			c.mu.Unlock()
			if err != nil {
				return fmt.Sprintf("reconnect failed: %v (original: %s)", err, text), true
			}
		}
		c.mu.Lock()
		client = c.client
		c.mu.Unlock()
		retryText, retryErr := fn(client)
		if !retryErr {
			return "[reconnected] " + retryText, false
		}
		return retryText, true

	case ErrTransient:
		// Retry same endpoint with backoff
		log.Printf("[env:%s] transient error, retrying...", c.envID)
		for attempt := 1; attempt <= 3; attempt++ {
			wait := time.Duration(attempt) * time.Second
			time.Sleep(wait)
			retryText, retryErr := fn(client)
			if !retryErr {
				return retryText, false
			}
			if classifyError(retryText) != ErrTransient {
				// Escalated to stale — fall through
				text = retryText
				break
			}
		}
		// Transient retries exhausted — try re-resolve
		fallthrough

	case ErrStaleEndpoint:
		log.Printf("[env:%s] stale endpoint, re-resolving and reconnecting...", c.envID)
		c.mu.Lock()
		c.close()
		err := c.resolveAndConnect(ctx)
		c.mu.Unlock()
		if err != nil {
			return fmt.Sprintf("reconnect failed: %v (original: %s)", err, text), true
		}

		// Retry with backoff while the tunnel warms up. A freshly-created
		// Cloudflare quick tunnel can take up to ~60s to start routing, so the
		// total window here covers that (capped per-attempt wait keeps the
		// cadence sane): 3+6+9+10+10+10+10 ≈ 58s across 7 attempts.
		c.mu.Lock()
		client = c.client
		c.mu.Unlock()
		const maxAttempts = 7
		for attempt := 1; attempt <= maxAttempts; attempt++ {
			wait := time.Duration(attempt*3) * time.Second
			if wait > 10*time.Second {
				wait = 10 * time.Second
			}
			log.Printf("[env:%s] waiting %s for tunnel (attempt %d/%d)...", c.envID, wait, attempt, maxAttempts)
			time.Sleep(wait)
			retryText, retryErr := fn(client)
			if !retryErr {
				return "[reconnected] " + retryText, false
			}
			log.Printf("[env:%s] attempt %d: %s", c.envID, attempt, retryText[:min(len(retryText), 80)])
		}
		return "environment unreachable after reconnect — the tunnel may still be starting (fresh tunnels can take ~60s). Try again in a few seconds.", true
	}

	return text, true
}

// resolveAndConnect queries the API for the environment, wakes it if needed,
// and establishes a gRPC connection. Must be called with c.mu held.
func (c *EnvConn) resolveAndConnect(ctx context.Context) error {
	c.state = StateResolving

	env, err := c.apiClient.GetEnvironment(c.envID)
	if err != nil {
		c.state = StateDisconnected
		return fmt.Errorf("get environment: %w", err)
	}

	switch env.State {
	case "deleted", "stopped":
		c.state = StateDisconnected
		return fmt.Errorf("environment %s is %s", c.envID, env.State)

	case "sleeping":
		log.Printf("[env:%s] sleeping, waking...", c.envID)
		wakeResp, err := c.apiClient.WakeEnvironment(c.envID)
		if err != nil {
			c.state = StateDisconnected
			return fmt.Errorf("wake: %w", err)
		}
		if wakeResp.Endpoint != "" {
			env.Endpoint = wakeResp.Endpoint
		}
		if wakeResp.Secret != "" {
			c.secret = wakeResp.Secret
			c.sessionToken = "" // new secret invalidates old session
			// Persist so MCP restarts between now and first auth don't lose the secret
			auth.SaveEnvironment(c.envID, env.Endpoint, wakeResp.Secret)
		}
		log.Printf("[env:%s] woke, endpoint=%s", c.envID, env.Endpoint)

	case "waking", "booting":
		// Poll until running
		log.Printf("[env:%s] %s, waiting...", c.envID, env.State)
		for i := 0; i < 15; i++ {
			time.Sleep(2 * time.Second)
			status, err := c.apiClient.GetEnvironment(c.envID)
			if err != nil {
				continue
			}
			if status.State == "running" && status.Endpoint != "" {
				env.Endpoint = status.Endpoint
				if status.Secret != "" && c.sessionToken == "" {
					c.secret = status.Secret
				}
				break
			}
		}
	}

	if env.Endpoint == "" {
		c.state = StateDisconnected
		return fmt.Errorf("environment %s has no endpoint (state=%s)", c.envID, env.State)
	}

	// Load persisted credentials from disk first (survives MCP restarts).
	// Session token takes priority over any secret — it's the proof of completed auth.
	if c.secret == "" && c.sessionToken == "" {
		if storedEnvID, _, storedSecret, storedToken, err := auth.LoadEnvironment(); err == nil && storedEnvID == c.envID {
			if storedToken != "" {
				c.sessionToken = storedToken
				log.Printf("[env:%s] loaded session token from disk", c.envID)
			} else if storedSecret != "" {
				c.secret = storedSecret
				log.Printf("[env:%s] loaded secret from disk", c.envID)
			}
		}
	}

	// Last resort: pick up secret from API response (may be stale if already consumed)
	if c.secret == "" && c.sessionToken == "" && env.Secret != "" {
		c.secret = env.Secret
		log.Printf("[env:%s] using secret from API (last resort)", c.envID)
	}

	log.Printf("[env:%s] auth state: secret=%v sessionToken=%v", c.envID, c.secret != "", c.sessionToken != "")

	// Connect via gRPC (WebSocket tunnel for Cloudflare, direct otherwise)
	c.state = StateConnecting
	log.Printf("[env:%s] connecting to %s", c.envID, env.Endpoint)

	dialCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	client, err := agentgrpc.Dial(dialCtx, env.Endpoint, c.secret, c.sessionToken)
	if err != nil {
		c.state = StateDisconnected
		return fmt.Errorf("connect: %w", err)
	}

	// Capture session token when the daemon responds to the first authenticated call.
	// The one-time secret is consumed on the first request; subsequent requests use the session token.
	client.OnTokenReceived = func(token string) {
		c.mu.Lock()
		c.sessionToken = token
		c.secret = "" // consumed
		c.mu.Unlock()
		// Persist so MCP restarts can reuse the token (also clears stale secret from disk)
		auth.SaveSessionToken(token)
	}

	c.client = client
	c.endpoint = env.Endpoint
	c.state = StateConnected
	c.lastPing = time.Now()

	log.Printf("[env:%s] connected (auth=%s)", c.envID, c.authMode())
	return nil
}

// close shuts down the current connection. Must be called with c.mu held.
func (c *EnvConn) close() {
	if c.client != nil {
		c.client.Close()
		c.client = nil
	}
	c.state = StateDisconnected
}

// StartHealthCheck runs a background goroutine that pings the environment
// every interval. If the ping fails, it marks the connection as disconnected
// so the next tool call triggers reconnection proactively.
func (c *EnvConn) StartHealthCheck(ctx context.Context, interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				c.mu.Lock()
				if c.state != StateConnected || c.client == nil {
					c.mu.Unlock()
					continue
				}
				client := c.client
				c.mu.Unlock()

				_, err := client.Agent.Ping(ctx, nil)
				if err != nil {
					log.Printf("[env:%s] health check failed: %v", c.envID, err)
					c.mu.Lock()
					c.close()
					c.mu.Unlock()
				} else {
					c.mu.Lock()
					c.lastPing = time.Now()
					c.mu.Unlock()
				}
			}
		}
	}()
}

// SetSecret stores the one-time secret for this environment (called after create).
func (c *EnvConn) SetSecret(secret string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.secret = secret
	c.sessionToken = "" // new secret invalidates old session
}

func (c *EnvConn) authMode() string {
	if c.sessionToken != "" {
		return "session-token"
	}
	if c.secret != "" {
		return "one-time-secret"
	}
	return "none"
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
