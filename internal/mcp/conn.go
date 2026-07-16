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
	"github.com/agend-sh/cli/internal/recovery"
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
	mu               sync.Mutex
	recoveryGateOnce sync.Once
	recoveryGate     chan struct{}
	envID            string
	state            ConnState
	client           *agentgrpc.Client
	generation       uint64
	authGeneration   uint64
	inflight         map[*agentgrpc.Client]uint64
	retired          map[*agentgrpc.Client]struct{}
	endpoint         string
	secret           string // one-time secret (cleared after first auth → session token)
	sessionToken     string // session token (set after first auth, reused on reconnect)

	apiClient *api.Client
	lastPing  time.Time
}

// heartbeatInterval is how often a connected environment is pinged. The ping
// is authenticated, so it doubles as the "user is attached" signal the
// platform uses to keep a workstation awake: while the MCP client holds a
// live connection, heartbeats flow and the env's idle clock never starts.
// Heartbeats stop the moment the connection drops (StartHealthCheck only
// pings in StateConnected), so they can never wake a sleeping env.
const heartbeatInterval = 60 * time.Second

// ConnPool manages connections to multiple environments.
type ConnPool struct {
	mu        sync.RWMutex
	conns     map[string]*EnvConn
	apiClient *api.Client
	ctx       context.Context // bounds heartbeat goroutines to the server's lifetime
}

func NewConnPool(apiClient *api.Client) *ConnPool {
	return &ConnPool{
		conns:     make(map[string]*EnvConn),
		apiClient: apiClient,
		ctx:       context.Background(),
	}
}

// BindContext sets the context that bounds per-connection heartbeat
// goroutines. Call once before serving.
func (p *ConnPool) BindContext(ctx context.Context) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ctx = ctx
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
	conn.StartHealthCheck(p.ctx, heartbeatInterval)
	p.conns[envID] = conn
	return conn
}

// CloseAll closes all connections in the pool.
func (p *ConnPool) CloseAll() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, conn := range p.conns {
		conn.mu.Lock()
		conn.close()
		conn.mu.Unlock()
	}
}

// Reset closes all connections and replaces the API client.
// Used by reload_config when credentials change.
func (p *ConnPool) Reset(newAPI *api.Client) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, conn := range p.conns {
		conn.mu.Lock()
		conn.close()
		conn.mu.Unlock()
	}
	p.conns = make(map[string]*EnvConn)
	p.apiClient = newAPI
}

// EnsureConnected drives the state machine until connected or fatal error.
// Called before every tool call.
func (c *EnvConn) EnsureConnected(ctx context.Context) error {
	c.mu.Lock()
	if c.state == StateConnected && c.client != nil {
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()

	if err := c.acquireRecovery(ctx); err != nil {
		return err
	}
	defer c.releaseRecovery()

	c.mu.Lock()
	defer c.mu.Unlock()
	// Another caller may have connected while this one waited for the gate.
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

	text, isErr, client, generation, authGeneration, err := c.invokeCurrent(fn)
	if err != nil {
		return fmt.Sprintf("connection failed: %v", err), true
	}
	if !isErr {
		return text, false
	}

	// Classify the error and decide what to do
	cat := classifyError(text)
	safelyRejected := cat == ErrAuth && recovery.IsUnauthenticatedText(text)

	// For a non-idempotent op that already reached the daemon, don't re-run it.
	// Auth errors are the exception: an Unauthenticated reply proves the call
	// was rejected before executing, so reauth + one retry is safe.
	if !idempotent && cat != ErrFatal && !safelyRejected {
		log.Printf("[env:%s] non-idempotent op failed (%v); reconnecting for next call, not retrying", c.envID, cat)
		_, _, _ = c.reconnectAfterFailure(ctx, client, generation)
		return text, true
	}

	switch cat {
	case ErrFatal:
		return text, true

	case ErrAuth:
		if !idempotent && !safelyRejected {
			return text, true
		}
		_, err := c.recoverAuthentication(ctx, authGeneration)
		if err != nil {
			return fmt.Sprintf("reconnect failed: %v (original: %s)", err, text), true
		}
		retryText, retryErr, _, _, _, invokeErr := c.invokeCurrent(fn)
		if invokeErr != nil {
			return fmt.Sprintf("reconnect failed: %v (original: %s)", invokeErr, text), true
		}
		if !retryErr {
			return "[reconnected] " + retryText, false
		}
		return retryText, true

	case ErrTransient:
		// Retry same endpoint with backoff
		log.Printf("[env:%s] transient error, retrying...", c.envID)
		for attempt := 1; attempt <= 3; attempt++ {
			wait := time.Duration(attempt) * time.Second
			if err := waitForContext(ctx, wait); err != nil {
				return err.Error(), true
			}
			retryText, retryErr, retryClient, retryGeneration, _, invokeErr := c.invokeCurrent(fn)
			if invokeErr != nil {
				return fmt.Sprintf("retry failed: %v", invokeErr), true
			}
			client, generation = retryClient, retryGeneration
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
		reconnectedClient, reconnectedGeneration, err := c.reconnectAfterFailure(ctx, client, generation)
		if err != nil {
			return fmt.Sprintf("reconnect failed: %v (original: %s)", err, text), true
		}
		client = reconnectedClient
		generation = reconnectedGeneration

		// Retry with backoff while the tunnel warms up. A freshly-created
		// Cloudflare quick tunnel can take up to ~60s to start routing, so the
		// total window here covers that (capped per-attempt wait keeps the
		// cadence sane): 3+6+9+10+10+10+10 ≈ 58s across 7 attempts.
		const maxAttempts = 7
		for attempt := 1; attempt <= maxAttempts; attempt++ {
			wait := time.Duration(attempt*3) * time.Second
			if wait > 10*time.Second {
				wait = 10 * time.Second
			}
			log.Printf("[env:%s] waiting %s for tunnel (attempt %d/%d)...", c.envID, wait, attempt, maxAttempts)
			if err := waitForContext(ctx, wait); err != nil {
				return err.Error(), true
			}
			retryText, retryErr, retryClient, retryGeneration, _, invokeErr := c.invokeCurrent(fn)
			if invokeErr != nil {
				return fmt.Sprintf("retry failed: %v", invokeErr), true
			}
			client, generation = retryClient, retryGeneration
			if !retryErr {
				return "[reconnected] " + retryText, false
			}
			log.Printf("[env:%s] attempt %d: %s", c.envID, attempt, retryText[:min(len(retryText), 80)])
		}
		return "environment unreachable after reconnect — the tunnel may still be starting (fresh tunnels can take ~60s). Try again in a few seconds.", true
	}

	return text, true
}

// invokeCurrent borrows the currently published client for the duration of a
// tool RPC. Connection recovery retires a borrowed client instead of closing
// it immediately, allowing concurrent RPCs to receive the daemon's definitive
// status rather than an ambiguous local "connection is closing" error.
func (c *EnvConn) invokeCurrent(
	fn func(*agentgrpc.Client) (string, bool),
) (text string, isErr bool, client *agentgrpc.Client, generation, authGeneration uint64, err error) {
	c.mu.Lock()
	if c.state != StateConnected || c.client == nil {
		c.mu.Unlock()
		return "", false, nil, 0, 0, fmt.Errorf("environment is not connected")
	}
	client = c.client
	generation = c.generation
	authGeneration = c.authGeneration
	if c.inflight == nil {
		c.inflight = make(map[*agentgrpc.Client]uint64)
	}
	c.inflight[client]++
	c.mu.Unlock()

	defer c.releaseClient(client)
	text, isErr = fn(client)
	return text, isErr, client, generation, authGeneration, nil
}

func (c *EnvConn) releaseClient(client *agentgrpc.Client) {
	var closeClient bool
	c.mu.Lock()
	count := c.inflight[client]
	if count <= 1 {
		delete(c.inflight, client)
		if _, ok := c.retired[client]; ok {
			delete(c.retired, client)
			closeClient = true
		}
	} else {
		c.inflight[client] = count - 1
	}
	c.mu.Unlock()
	if closeClient {
		_ = client.Close()
	}
}

// acquireRecovery serializes connection replacement while allowing a cancelled
// MCP call to stop waiting immediately. A sync.Mutex cannot be selected with a
// context, which previously left cancelled peers waiting behind a 30-second
// control-plane request.
func (c *EnvConn) acquireRecovery(ctx context.Context) error {
	c.recoveryGateOnce.Do(func() {
		c.recoveryGate = make(chan struct{}, 1)
		c.recoveryGate <- struct{}{}
	})
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.recoveryGate:
		return nil
	}
}

func (c *EnvConn) releaseRecovery() {
	c.recoveryGate <- struct{}{}
}

// reconnectAfterFailure replaces failedClient only if it is still the
// published generation. If another recovery already installed a replacement,
// a late result from the old client reuses it instead of tearing it down.
func (c *EnvConn) reconnectAfterFailure(
	ctx context.Context,
	failedClient *agentgrpc.Client,
	failedGeneration uint64,
) (*agentgrpc.Client, uint64, error) {
	if err := c.acquireRecovery(ctx); err != nil {
		return nil, 0, err
	}
	defer c.releaseRecovery()

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.client != failedClient || c.generation != failedGeneration {
		if c.state == StateConnected && c.client != nil {
			return c.client, c.generation, nil
		}
		return nil, 0, fmt.Errorf("environment connection changed during recovery")
	}
	c.close()
	if err := c.resolveAndConnect(ctx); err != nil {
		return nil, 0, err
	}
	return c.client, c.generation, nil
}

// recoverAuthentication rotates credentials at most once for all callers that
// failed against the same credential generation. The separate auth generation
// matters because an endpoint-only reconnect can change the client generation
// without repairing the invalid credential that triggered this recovery.
func (c *EnvConn) recoverAuthentication(
	ctx context.Context,
	failedAuthGeneration uint64,
) (*agentgrpc.Client, error) {
	if err := c.acquireRecovery(ctx); err != nil {
		return nil, err
	}
	defer c.releaseRecovery()

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.authGeneration != failedAuthGeneration {
		if c.state == StateConnected && c.client != nil {
			return c.client, nil
		}
		if err := c.resolveAndConnect(ctx); err != nil {
			return nil, err
		}
		return c.client, nil
	}

	log.Printf("[env:%s] auth error, attempting reauth...", c.envID)
	reauthResp, reauthErr := c.apiClient.ReauthEnvironmentContext(ctx, c.envID)
	if reauthErr == nil {
		// A successful control-plane response may already have advanced the
		// durable auth revision. Fence this client before validating the payload
		// so malformed success data cannot provoke a second concurrent rotation.
		c.close()
		c.authGeneration++
		if reauthResp.EnvID != c.envID {
			return nil, fmt.Errorf("reauth returned environment %q, expected %q", reauthResp.EnvID, c.envID)
		}
		if reauthResp.Secret == "" {
			return nil, fmt.Errorf("reauth returned an empty one-time secret")
		}
		log.Printf("[env:%s] reauth succeeded, reconnecting with new secret", c.envID)
		c.secret = reauthResp.Secret
		c.sessionToken = ""
		// Persist the authoritative control-plane response before attempting the
		// handshake. A process exit in this window must not resurrect the stale
		// secret/session that the new auth revision invalidated.
		if err := auth.SaveEnvironment(c.envID, c.endpoint, reauthResp.Secret); err != nil {
			return nil, fmt.Errorf("persist reauthentication secret: %w", err)
		}
	} else {
		// Preserve the existing fallback for control-plane errors: a status
		// refresh may still yield a usable endpoint or credential.
		log.Printf("[env:%s] reauth failed (%v), falling back to re-resolve", c.envID, reauthErr)
		c.close()
	}

	if err := c.resolveAndConnect(ctx); err != nil {
		return nil, err
	}
	return c.client, nil
}

// resolveAndConnect queries the API for the environment, wakes it if needed,
// and establishes a gRPC connection. Must be called with c.mu held.
func (c *EnvConn) resolveAndConnect(ctx context.Context) error {
	c.state = StateResolving

	env, err := c.apiClient.GetEnvironmentContext(ctx, c.envID)
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
		wakeResp, err := c.apiClient.WakeEnvironmentContext(ctx, c.envID)
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
			c.authGeneration++
			// Persist so MCP restarts between now and first auth don't lose the secret
			if err := auth.SaveEnvironment(c.envID, env.Endpoint, wakeResp.Secret); err != nil {
				c.state = StateDisconnected
				return fmt.Errorf("persist wake secret: %w", err)
			}
		}
		log.Printf("[env:%s] woke, endpoint=%s", c.envID, env.Endpoint)

	case "waking", "booting":
		// Poll until running
		log.Printf("[env:%s] %s, waiting...", c.envID, env.State)
		for i := 0; i < 15; i++ {
			if err := waitForContext(ctx, 2*time.Second); err != nil {
				c.state = StateDisconnected
				return err
			}
			status, err := c.apiClient.GetEnvironmentContext(ctx, c.envID)
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
		// A response from a client that was closed during recovery may arrive
		// after its replacement has been published. Do not let that stale
		// generation overwrite the replacement's credentials on disk or in
		// memory.
		if c.client != client {
			c.mu.Unlock()
			return
		}
		c.sessionToken = token
		c.secret = "" // consumed
		c.authGeneration++
		c.mu.Unlock()
		// Persist so MCP restarts can reuse the token (also clears stale secret from disk)
		auth.SaveSessionToken(token)
	}

	c.client = client
	c.generation++
	c.endpoint = env.Endpoint
	c.state = StateConnected
	c.lastPing = time.Now()

	log.Printf("[env:%s] connected (auth=%s)", c.envID, c.authMode())
	return nil
}

// close shuts down the current connection. Must be called with c.mu held.
func (c *EnvConn) close() {
	if c.client != nil {
		client := c.client
		c.client = nil
		c.generation++
		if c.inflight[client] > 0 {
			if c.retired == nil {
				c.retired = make(map[*agentgrpc.Client]struct{})
			}
			c.retired[client] = struct{}{}
		} else {
			_ = client.Close()
		}
	}
	c.state = StateDisconnected
}

// StartHealthCheck runs a background goroutine that pings the environment
// every interval while connected. The ping carries the session token, so it
// is also the liveness heartbeat that keeps the workstation awake server-side.
// If the ping fails, it marks the connection as disconnected so heartbeats
// stop (never waking a sleeping env) and the next tool call reconnects.
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

				// Bounded per-ping deadline: a wedged tunnel must not stall
				// the loop for the server's whole lifetime.
				pingCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
				_, err := client.Agent.Ping(pingCtx, nil)
				cancel()
				if err != nil {
					log.Printf("[env:%s] heartbeat failed, marking disconnected: %v", c.envID, err)
					c.mu.Lock()
					// The failed heartbeat may belong to a connection that a
					// concurrent recovery already replaced. Never let an old RPC
					// close the newly published generation.
					if c.client == client {
						c.close()
					}
					c.mu.Unlock()
				} else {
					c.mu.Lock()
					if c.client == client {
						c.lastPing = time.Now()
					}
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
	c.authGeneration++
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

func waitForContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
