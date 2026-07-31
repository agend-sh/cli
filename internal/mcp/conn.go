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
		conn.disconnect()
	}
}

// Reset closes all connections and replaces the API client.
// Used by reload_config when credentials change.
func (p *ConnPool) Reset(newAPI *api.Client) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, conn := range p.conns {
		conn.disconnect()
	}
	p.conns = make(map[string]*EnvConn)
	p.apiClient = newAPI
}

// EnsureConnected drives the state machine until connected or fatal error.
// Called before every tool call.
func (c *EnvConn) EnsureConnected(ctx context.Context) error {
	if err := c.acquireRecovery(ctx); err != nil {
		return err
	}
	defer c.releaseRecovery()

	c.mu.Lock()
	// Another caller may have connected while this one waited for the gate.
	if c.state == StateConnected && c.client != nil {
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()

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

	text, isErr, client, generation, authGeneration, err := c.invokeCurrent(ctx, fn)
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
		retryText, retryErr, _, _, _, invokeErr := c.invokeCurrent(ctx, fn)
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
			retryText, retryErr, retryClient, retryGeneration, _, invokeErr := c.invokeCurrent(ctx, fn)
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
			retryText, retryErr, retryClient, retryGeneration, _, invokeErr := c.invokeCurrent(ctx, fn)
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
	ctx context.Context,
	fn func(*agentgrpc.Client) (string, bool),
) (text string, isErr bool, client *agentgrpc.Client, generation, authGeneration uint64, err error) {
	client, generation, authGeneration, err = c.borrowCurrent(ctx)
	if err != nil {
		return "", false, nil, 0, 0, err
	}
	defer c.releaseClient(client)
	text, isErr = fn(client)
	return text, isErr, client, generation, authGeneration, nil
}

func (c *EnvConn) borrowCurrent(ctx context.Context) (client *agentgrpc.Client, generation, authGeneration uint64, err error) {
	// Borrowing is a lifecycle operation: take the context-aware recovery gate
	// before the ordinary state mutex. A recovery may spend tens of seconds in
	// the control plane, and a cancelled MCP request must not get trapped behind
	// its non-selectable sync.Mutex.
	if err := c.acquireRecovery(ctx); err != nil {
		return nil, 0, 0, err
	}
	c.mu.Lock()
	if c.state != StateConnected || c.client == nil {
		c.mu.Unlock()
		c.releaseRecovery()
		return nil, 0, 0, fmt.Errorf("environment is not connected")
	}
	client = c.client
	generation = c.generation
	authGeneration = c.authGeneration
	if c.inflight == nil {
		c.inflight = make(map[*agentgrpc.Client]uint64)
	}
	c.inflight[client]++
	c.mu.Unlock()
	c.releaseRecovery()
	return client, generation, authGeneration, nil
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
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.recoveryGate:
		// Cancellation and the gate can become ready together. Prefer
		// cancellation deterministically instead of relying on select's random
		// ready-case choice.
		if err := ctx.Err(); err != nil {
			c.recoveryGate <- struct{}{}
			return err
		}
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
	if c.client != failedClient || c.generation != failedGeneration {
		if c.state == StateConnected && c.client != nil {
			client, generation := c.client, c.generation
			c.mu.Unlock()
			return client, generation, nil
		}
		c.mu.Unlock()
		return nil, 0, fmt.Errorf("environment connection changed during recovery")
	}
	closeClient := c.close()
	c.mu.Unlock()
	if closeClient != nil {
		_ = closeClient.Close()
	}
	if err := c.resolveAndConnect(ctx); err != nil {
		return nil, 0, err
	}
	c.mu.Lock()
	client, generation := c.client, c.generation
	c.mu.Unlock()
	if client == nil {
		return nil, 0, fmt.Errorf("environment did not publish a connection")
	}
	return client, generation, nil
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
	if c.authGeneration != failedAuthGeneration {
		if c.state == StateConnected && c.client != nil {
			client := c.client
			c.mu.Unlock()
			return client, nil
		}
		c.mu.Unlock()
		if err := c.resolveAndConnect(ctx); err != nil {
			return nil, err
		}
		c.mu.Lock()
		client := c.client
		c.mu.Unlock()
		if client == nil {
			return nil, fmt.Errorf("environment did not publish a connection")
		}
		return client, nil
	}

	// Fence the attempt before issuing the control-plane mutation. A transport
	// error is ambiguous: the control plane may already have committed the new
	// auth revision. Peers that failed with this same credential generation must
	// not issue a second rotation merely because the first response was lost.
	c.authGeneration++
	envID, apiClient, endpoint := c.envID, c.apiClient, c.endpoint
	closeClient := c.close()
	c.mu.Unlock()
	if closeClient != nil {
		_ = closeClient.Close()
	}

	log.Printf("[env:%s] auth error, attempting reauth...", envID)
	reauthResp, reauthErr := apiClient.ReauthEnvironmentContext(ctx, envID)
	if reauthErr == nil {
		if reauthResp.EnvID != envID {
			return nil, fmt.Errorf("reauth returned environment %q, expected %q", reauthResp.EnvID, envID)
		}
		if reauthResp.Secret == "" {
			return nil, fmt.Errorf("reauth returned an empty one-time secret")
		}
		log.Printf("[env:%s] reauth succeeded, reconnecting with new secret", envID)
		c.mu.Lock()
		c.secret = reauthResp.Secret
		c.sessionToken = ""
		c.mu.Unlock()
		// Persist the authoritative control-plane response before attempting the
		// handshake. A process exit in this window must not resurrect the stale
		// secret/session that the new auth revision invalidated.
		if err := auth.SaveEnvironment(envID, endpoint, reauthResp.Secret); err != nil {
			return nil, fmt.Errorf("persist reauthentication secret: %w", err)
		}
	} else {
		// A status refresh may still yield the committed credential/endpoint, but
		// the generation fence above prevents concurrent peers from repeating the
		// ambiguous reauth mutation.
		log.Printf("[env:%s] reauth failed (%v), falling back to re-resolve", envID, reauthErr)
	}

	if err := c.resolveAndConnect(ctx); err != nil {
		return nil, err
	}
	c.mu.Lock()
	client := c.client
	c.mu.Unlock()
	if client == nil {
		return nil, fmt.Errorf("environment did not publish a connection")
	}
	return client, nil
}

// resolveAndConnect queries the API for the environment, wakes it if needed,
// and establishes a gRPC connection. The caller must hold recoveryGate, which
// serializes lifecycle changes. c.mu is held only for short state snapshots;
// control-plane, disk, polling, and dial I/O must remain outside it so another
// MCP call can observe cancellation while it waits on recoveryGate.
func (c *EnvConn) resolveAndConnect(ctx context.Context) error {
	c.mu.Lock()
	c.state = StateResolving
	envID, apiClient := c.envID, c.apiClient
	c.mu.Unlock()

	disconnect := func(err error) error {
		c.mu.Lock()
		if c.client == nil {
			c.state = StateDisconnected
		}
		c.mu.Unlock()
		return err
	}

	env, err := apiClient.GetEnvironmentContext(ctx, envID)
	if err != nil {
		return disconnect(fmt.Errorf("get environment: %w", err))
	}
	if env.EnvID != "" && env.EnvID != envID {
		return disconnect(fmt.Errorf("get environment returned %q, expected %q", env.EnvID, envID))
	}

	switch env.State {
	case "deleted", "stopped":
		return disconnect(fmt.Errorf("environment %s is %s", envID, env.State))

	case "sleeping":
		log.Printf("[env:%s] sleeping, waking...", envID)
		wakeResp, err := apiClient.WakeEnvironmentContext(ctx, envID)
		if err != nil {
			return disconnect(fmt.Errorf("wake: %w", err))
		}
		if wakeResp.EnvID != "" && wakeResp.EnvID != envID {
			return disconnect(fmt.Errorf("wake returned environment %q, expected %q", wakeResp.EnvID, envID))
		}
		if wakeResp.Endpoint != "" {
			env.Endpoint = wakeResp.Endpoint
		}
		if wakeResp.Secret != "" {
			c.mu.Lock()
			c.secret = wakeResp.Secret
			c.sessionToken = "" // new secret invalidates old session
			c.authGeneration++
			c.mu.Unlock()
			// Persist so MCP restarts between now and first auth don't lose the secret
			if err := auth.SaveEnvironment(envID, env.Endpoint, wakeResp.Secret); err != nil {
				return disconnect(fmt.Errorf("persist wake secret: %w", err))
			}
		}
		log.Printf("[env:%s] woke, endpoint=%s", envID, env.Endpoint)

	case "waking", "booting":
		// Poll until running
		log.Printf("[env:%s] %s, waiting...", envID, env.State)
		for i := 0; i < 15; i++ {
			if err := waitForContext(ctx, 2*time.Second); err != nil {
				return disconnect(err)
			}
			status, err := apiClient.GetEnvironmentContext(ctx, envID)
			if err != nil {
				continue
			}
			if status.EnvID != "" && status.EnvID != envID {
				return disconnect(fmt.Errorf("get environment returned %q, expected %q", status.EnvID, envID))
			}
			if status.State == "running" && status.Endpoint != "" {
				env.Endpoint = status.Endpoint
				c.mu.Lock()
				if status.Secret != "" && c.sessionToken == "" && c.secret != status.Secret {
					c.secret = status.Secret
					c.authGeneration++
				}
				c.mu.Unlock()
				break
			}
		}
	}

	if env.Endpoint == "" {
		return disconnect(fmt.Errorf("environment %s has no endpoint (state=%s)", envID, env.State))
	}

	// Load persisted credentials from disk first (survives MCP restarts).
	// Session token takes priority over any secret — it's the proof of completed auth.
	c.mu.Lock()
	credentialsMissing := c.secret == "" && c.sessionToken == ""
	c.mu.Unlock()
	if credentialsMissing {
		if storedEnvID, _, storedSecret, storedToken, err := auth.LoadEnvironment(); err == nil && storedEnvID == envID {
			c.mu.Lock()
			if storedToken != "" {
				if c.secret == "" && c.sessionToken == "" {
					c.sessionToken = storedToken
					log.Printf("[env:%s] loaded session token from disk", envID)
				}
			} else if storedSecret != "" {
				if c.secret == "" && c.sessionToken == "" {
					c.secret = storedSecret
					log.Printf("[env:%s] loaded secret from disk", envID)
				}
			}
			c.mu.Unlock()
		}
	}

	// Last resort: pick up secret from API response (may be stale if already consumed)
	c.mu.Lock()
	if c.secret == "" && c.sessionToken == "" && env.Secret != "" {
		c.secret = env.Secret
		c.authGeneration++
		log.Printf("[env:%s] using secret from API (last resort)", envID)
	}

	secret, sessionToken := c.secret, c.sessionToken
	log.Printf("[env:%s] auth state: secret=%v sessionToken=%v", envID, secret != "", sessionToken != "")

	// Connect via gRPC (WebSocket tunnel for Cloudflare, direct otherwise)
	c.state = StateConnecting
	c.mu.Unlock()
	log.Printf("[env:%s] connecting to %s", envID, env.Endpoint)

	dialCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	client, err := agentgrpc.Dial(dialCtx, env.Endpoint, secret, sessionToken)
	if err != nil {
		return disconnect(fmt.Errorf("connect: %w", err))
	}

	var publishedGeneration uint64
	// Capture session token when the daemon responds to the first authenticated call.
	// The one-time secret is consumed on the first request; subsequent requests use the session token.
	client.OnTokenReceived = func(token string) {
		// Serialize the generation check and durable credential transition with
		// recoveries, but do not hold c.mu across disk I/O. Peers wait on this
		// context-aware gate and can therefore leave immediately when cancelled.
		if err := c.acquireRecovery(context.Background()); err != nil {
			return
		}
		defer c.releaseRecovery()
		c.mu.Lock()
		// A response from a client that was closed during recovery may arrive
		// after its replacement has been published. Do not let that stale
		// generation overwrite the replacement's credentials on disk or in
		// memory.
		if c.client != client || c.generation != publishedGeneration {
			c.mu.Unlock()
			return
		}
		c.sessionToken = token
		c.secret = "" // consumed
		c.authGeneration++
		c.mu.Unlock()
		// recoveryGate remains held, so a recovery cannot publish/persist newer
		// credentials between the check and this write, then be overwritten by a
		// late old-client callback.
		saved, err := auth.SaveSessionTokenForEnvironment(envID, secret, sessionToken, token)
		if err != nil {
			log.Printf("[env:%s] persist session token: %v", envID, err)
		} else if !saved {
			log.Printf("[env:%s] active credential record changed; session token not persisted", envID)
		}
	}

	if err := ctx.Err(); err != nil {
		_ = client.Close()
		return disconnect(err)
	}
	c.mu.Lock()
	c.client = client
	c.generation++
	publishedGeneration = c.generation
	c.endpoint = env.Endpoint
	c.state = StateConnected
	c.lastPing = time.Now()
	authMode := c.authMode()
	c.mu.Unlock()

	log.Printf("[env:%s] connected (auth=%s)", envID, authMode)
	return nil
}

// close unpublishes the current connection and returns an unborrowed client for
// closing after c.mu is released. A borrowed client is instead put on the
// retired set and closed by its final releaseClient call.
// Must be called with c.mu held.
func (c *EnvConn) close() *agentgrpc.Client {
	var closeClient *agentgrpc.Client
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
			closeClient = client
		}
	}
	c.state = StateDisconnected
	return closeClient
}

// disconnect serializes an administrative close with connection recovery.
// It is used only by pool teardown/reset and therefore intentionally waits for
// an in-progress recovery instead of abandoning a partially-published client.
func (c *EnvConn) disconnect() {
	if err := c.acquireRecovery(context.Background()); err != nil {
		return
	}
	c.mu.Lock()
	closeClient := c.close()
	c.mu.Unlock()
	if closeClient != nil {
		_ = closeClient.Close()
	}
	c.releaseRecovery()
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
				// Bounded per-ping deadline: a wedged tunnel must not stall
				// the loop for the server's whole lifetime.
				pingCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
				client, _, _, borrowErr := c.borrowCurrent(pingCtx)
				if borrowErr != nil {
					cancel()
					continue
				}
				_, err := client.Agent.Ping(pingCtx, nil)
				cancel()
				c.releaseClient(client)
				if err != nil {
					log.Printf("[env:%s] heartbeat failed, marking disconnected: %v", c.envID, err)
					var closeClient *agentgrpc.Client
					c.mu.Lock()
					// The failed heartbeat may belong to a connection that a
					// concurrent recovery already replaced. Never let an old RPC
					// close the newly published generation.
					if c.client == client {
						closeClient = c.close()
					}
					c.mu.Unlock()
					if closeClient != nil {
						_ = closeClient.Close()
					}
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

// SetSecret durably installs a new endpoint/one-time-secret pair (called after
// create or an explicit wake). recoveryGate keeps the disk transition ordered
// with session-token callbacks and connection replacement.
func (c *EnvConn) SetSecret(endpoint, secret string) error {
	if err := c.acquireRecovery(context.Background()); err != nil {
		return err
	}
	defer c.releaseRecovery()
	if err := auth.SaveEnvironment(c.envID, endpoint, secret); err != nil {
		return err
	}
	c.mu.Lock()
	// A new one-time secret invalidates every session held by the published
	// client. Retire it now so the next call cannot dispatch with stale auth.
	closeClient := c.close()
	c.secret = secret
	c.sessionToken = "" // new secret invalidates old session
	c.authGeneration++
	c.endpoint = endpoint
	c.mu.Unlock()
	if closeClient != nil {
		_ = closeClient.Close()
	}
	return nil
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
