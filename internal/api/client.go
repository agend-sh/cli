package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	DefaultBaseURL       = "https://api.agend.sh"
	ControlPlaneVersion  = "v2"
	controlPlaneV2Prefix = "/" + ControlPlaneVersion
	defaultHTTPTimeout   = 30 * time.Second
	loopbackHTTPTimeout  = 3 * time.Minute
)

type Client struct {
	baseURL    string
	baseURLErr error
	token      string
	httpClient *http.Client
}

func New(baseURL, token string) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Client{
		baseURL:    baseURL,
		baseURLErr: validateBaseURL(baseURL),
		token:      token,
		httpClient: &http.Client{
			Timeout: clientHTTPTimeout(baseURL),
		},
	}
}

// Nested QEMU lifecycle tests can legitimately take longer than the public
// control-plane deadline while replaying an exact Firecracker restore. Keep
// the production timeout unchanged and widen only explicitly local endpoints.
func clientHTTPTimeout(baseURL string) time.Duration {
	u, err := url.Parse(baseURL)
	if err != nil {
		return defaultHTTPTimeout
	}
	host := u.Hostname()
	if host == "localhost" {
		return loopbackHTTPTimeout
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return loopbackHTTPTimeout
	}
	return defaultHTTPTimeout
}

// validateBaseURL rejects API base URLs that would send the bearer token
// (and login passwords) in cleartext. The api_url config field is meant for
// dev/testing, so plain http is allowed only toward loopback.
func validateBaseURL(baseURL string) error {
	u, err := url.Parse(baseURL)
	if err != nil {
		return fmt.Errorf("invalid api_url %q: %w", baseURL, err)
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		host := u.Hostname()
		if host == "localhost" {
			return nil
		}
		if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
			return nil
		}
		return fmt.Errorf("refusing plain-http api_url %q: credentials would be sent in cleartext — use https (http is allowed for localhost only)", baseURL)
	default:
		return fmt.Errorf("invalid api_url %q: scheme must be https", baseURL)
	}
}

type APIError struct {
	StatusCode int
	Message    string
	// Code is the server's machine-readable error code, when it sends one.
	Code string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("api error %d: %s", e.StatusCode, e.Message)
}

// Auth

type SignupRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type LoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type GitHubAuthRequest struct {
	Code string `json:"code"`
}

type AuthResponse struct {
	UserID  string `json:"user_id"`
	Token   string `json:"token"`
	Message string `json:"message"` // e.g. signup waitlist notice (no token issued)
}

func (c *Client) Signup(email, password string) (*AuthResponse, error) {
	return doJSON[AuthResponse](c, "POST", "/auth/signup", SignupRequest{Email: email, Password: password})
}

func (c *Client) Login(email, password string) (*AuthResponse, error) {
	return doJSON[AuthResponse](c, "POST", "/auth/login", LoginRequest{Email: email, Password: password})
}

func (c *Client) GitHubAuth(code string) (*AuthResponse, error) {
	return doJSON[AuthResponse](c, "POST", "/auth/github", GitHubAuthRequest{Code: code})
}

// Environments

type CreateEnvResponse struct {
	EnvID    string `json:"env_id"`
	Alias    string `json:"alias"`
	Banner   string `json:"banner"`
	Endpoint string `json:"endpoint"`
	Secret   string `json:"secret"`
	State    string `json:"state"`
}

type CreateEnvRequest struct {
	Alias  string `json:"alias,omitempty"`
	Banner string `json:"banner,omitempty"`
	// ProfileID selects a machine profile the account has an allowance for;
	// empty means the plan's default profile.
	ProfileID string `json:"profile_id,omitempty"`
}

type EnvStatusResponse struct {
	EnvID      string `json:"env_id"`
	Alias      string `json:"alias"`
	Banner     string `json:"banner"`
	PinnedNote string `json:"pinned_note"`
	State      string `json:"state"`
	Endpoint   string `json:"endpoint"`
	Tier       string `json:"tier"`
	Profile    string `json:"profile_id"`
	Secret     string `json:"secret,omitempty"`
	CreatedAt  string `json:"created_at"`
	LastActive string `json:"last_active"`
	// ColdReset reports the cold reset recorded on the current lifecycle
	// operation, if any.
	ColdReset *ColdResetProgress `json:"cold_reset,omitempty"`
}

// ColdResetProgress is the phase of one cold reset: "stopping", "stopped",
// "booting", or "complete".
type ColdResetProgress struct {
	OperationID string `json:"operation_id"`
	Phase       string `json:"phase"`
}

type EnvStopResponse struct {
	EnvID string `json:"env_id"`
	State string `json:"state"`
}

type EnvWakeResponse struct {
	EnvID          string `json:"env_id"`
	Endpoint       string `json:"endpoint"`
	Secret         string `json:"secret"`
	State          string `json:"state"`
	Phase          string `json:"phase,omitempty"`
	ResetPending   bool   `json:"cold_reset_pending,omitempty"`
	ReauthRequired bool   `json:"reauth_required,omitempty"`
	// OperationID echoes the cold reset this response describes. Servers that
	// run reset phases in the background always set it.
	OperationID string `json:"operation_id,omitempty"`
}

type coldResetRequest struct {
	Reason      string `json:"reason"`
	OperationID string `json:"operation_id"`
}

type ListEnvsResponse struct {
	Environments []EnvSummary `json:"environments"`
}

type EnvSummary struct {
	EnvID      string `json:"env_id"`
	Alias      string `json:"alias"`
	Banner     string `json:"banner"`
	PinnedNote string `json:"pinned_note"`
	State      string `json:"state"`
	Endpoint   string `json:"endpoint"`
	Tier       string `json:"tier"`
	Profile    string `json:"profile_id"`
	CreatedAt  string `json:"created_at"`
	LastActive string `json:"last_active"`
}

func (c *Client) ListEnvironments() (*ListEnvsResponse, error) {
	return doControlPlaneJSON[ListEnvsResponse](c, "GET", "/environments", nil)
}

// CreateEnvironment creates a personal environment. profileID selects a
// machine profile the account has an allowance for; empty means the plan's
// default profile (and stays wire-compatible with older control planes).
func (c *Client) CreateEnvironment(profileID string) (*CreateEnvResponse, error) {
	var body any
	if profileID != "" {
		body = map[string]string{"profile_id": profileID}
	}
	return doControlPlaneJSON[CreateEnvResponse](c, "POST", "/environments", body)
}

// ProfileSummary is one machine profile the account may create envs with.
type ProfileSummary struct {
	ProfileID   string `json:"profile_id"`
	Name        string `json:"name"`
	Default     bool   `json:"default"`
	VcpuCount   int    `json:"vcpu_count"`
	MemMib      int    `json:"mem_mib"`
	DiskGB      int    `json:"disk_gb"`
	IdleMinutes int    `json:"idle_minutes"`
	MaxEnvs     int    `json:"max_envs"`
	InUse       int    `json:"in_use"`
}

type ListProfilesResponse struct {
	Profiles []ProfileSummary `json:"profiles"`
}

// ListProfiles lists the machine profiles available to this account, or to a
// team when teamID is non-empty.
func (c *Client) ListProfiles(teamID string) (*ListProfilesResponse, error) {
	path := "/profiles"
	if teamID != "" {
		path += "?team_id=" + url.QueryEscape(teamID)
	}
	return doControlPlaneJSON[ListProfilesResponse](c, "GET", path, nil)
}

func (c *Client) CreateEnvironmentWithMetadata(request CreateEnvRequest) (*CreateEnvResponse, error) {
	return doControlPlaneJSON[CreateEnvResponse](c, "POST", "/environments", request)
}

func (c *Client) GetEnvironment(envID string) (*EnvStatusResponse, error) {
	return c.GetEnvironmentContext(context.Background(), envID)
}

// GetEnvironmentContext is GetEnvironment with caller-controlled cancellation.
// Long-lived MCP processes use it so a cancelled tool call does not remain
// stuck behind the control-plane client's 30-second transport timeout.
func (c *Client) GetEnvironmentContext(ctx context.Context, envID string) (*EnvStatusResponse, error) {
	return doControlPlaneJSONContext[EnvStatusResponse](ctx, c, "GET", "/environments/"+envID, nil)
}

type UpdateEnvRequest struct {
	Alias      *string
	ClearAlias bool
	Banner     *string
}

func (c *Client) UpdateEnvironment(envID string, request UpdateEnvRequest) (*EnvStatusResponse, error) {
	if request.Alias != nil && request.ClearAlias {
		return nil, fmt.Errorf("environment name and clear-name are mutually exclusive")
	}
	body := make(map[string]any, 2)
	if request.Alias != nil {
		body["alias"] = *request.Alias
	} else if request.ClearAlias {
		body["alias"] = nil
	}
	if request.Banner != nil {
		body["banner"] = *request.Banner
	}
	if len(body) == 0 {
		return nil, fmt.Errorf("environment update is empty")
	}
	return doControlPlaneJSON[EnvStatusResponse](c, "PATCH", "/environments/"+envID, body)
}

func (c *Client) StopEnvironment(envID string) (*EnvStopResponse, error) {
	return doControlPlaneJSON[EnvStopResponse](c, "DELETE", "/environments/"+envID, nil)
}

func (c *Client) WakeEnvironment(envID string) (*EnvWakeResponse, error) {
	return c.WakeEnvironmentContext(context.Background(), envID)
}

// WakeEnvironmentContext is WakeEnvironment with caller-controlled cancellation.
func (c *Client) WakeEnvironmentContext(ctx context.Context, envID string) (*EnvWakeResponse, error) {
	return doControlPlaneJSONContext[EnvWakeResponse](ctx, c, "POST", "/environments/"+envID+"/wake", nil)
}

// ColdResetEnvironment discards guest memory, processes, and snapshots while
// preserving the environment's persistent data disk. The bounded context keeps
// a CLI invocation from waiting forever on an unavailable owner node.
func (c *Client) ColdResetEnvironment(envID, reason string) (*EnvWakeResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), ColdResetTimeout)
	defer cancel()
	return c.ColdResetEnvironmentContext(ctx, envID, reason)
}

// ColdResetEnvironmentContext drives a cold reset to completion. Every request
// carries the same operation ID, so a lost HTTP response resumes the fenced
// operation instead of resetting a recovered VM.
func (c *Client) ColdResetEnvironmentContext(ctx context.Context, envID, reason string) (*EnvWakeResponse, error) {
	return c.ColdResetEnvironmentProgress(ctx, envID, reason, nil)
}

// ColdResetTimeout bounds a complete cold reset: a cold stop and a fresh boot,
// each of which can take about a minute on a loaded node.
const ColdResetTimeout = 10 * time.Minute

// Cold-reset pacing. Variables so tests can run the state machine quickly.
var (
	coldResetPollInterval = 2 * time.Second
	// Re-send when the stop finished but no boot started, e.g. its worker died.
	coldResetAdvanceAfter = 20 * time.Second
	// Re-send when a stop or boot shows no progress for this long. It exceeds
	// the control plane's 90s node-agent timeout.
	coldResetResumeAfter = 3 * time.Minute
	// Re-send when the server has not recorded this operation, or while
	// another lifecycle operation owns the environment.
	coldResetUnclaimedAfter = 30 * time.Second
	coldResetMaxBackoff     = 15 * time.Second
)

// ColdResetEnvironmentProgress is ColdResetEnvironmentContext with progress
// reporting: progress, when non-nil, receives each newly observed phase
// ("stopping", "stopped", "booting", "waiting", "complete").
//
// The server accepts the reset and runs its stop and boot phases in the
// background, so the client polls GET /environments/{id} and re-sends the
// reset only when progress stalls. Re-sends of an accepted operation do not
// count against the per-hour reset limit; a 429 for that limit is final.
func (c *Client) ColdResetEnvironmentProgress(ctx context.Context, envID, reason string, progress func(phase string)) (*EnvWakeResponse, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" || len(reason) > 256 {
		return nil, fmt.Errorf("cold reset reason is required (max 256 chars)")
	}
	if c.baseURLErr != nil {
		return nil, c.baseURLErr
	}
	operationBytes := make([]byte, 16)
	if _, err := rand.Read(operationBytes); err != nil {
		return nil, fmt.Errorf("generate cold reset operation id: %w", err)
	}
	request := coldResetRequest{Reason: reason, OperationID: hex.EncodeToString(operationBytes)}

	lastReported := ""
	report := func(phase string) {
		if progress != nil && phase != "" && phase != lastReported {
			lastReported = phase
			progress(phase)
		}
	}

	result, err := c.sendColdReset(ctx, envID, request)
	if err != nil {
		return nil, err
	}
	if result.OperationID == "" {
		// The server predates background resets and only reports completion
		// in the POST response.
		return c.coldResetByReplay(ctx, envID, request, result)
	}
	report(result.Phase)

	lastSent := time.Now()
	phase, phaseSince := result.Phase, time.Now()
	for {
		if result.State == "running" && (result.Phase == "complete" || result.ReauthRequired || result.Secret != "") {
			report("complete")
			return c.coldResetCredentials(ctx, envID, result)
		}
		if err := sleepContext(ctx, coldResetPollInterval); err != nil {
			return nil, fmt.Errorf("cold reset did not complete: %w", err)
		}

		status, err := c.GetEnvironmentContext(ctx, envID)
		if err != nil {
			if !retryableColdResetStatusError(err) {
				return nil, fmt.Errorf("cold reset status: %w", err)
			}
			continue
		}
		ours := status.ColdReset != nil && status.ColdReset.OperationID == request.OperationID
		if ours {
			if status.ColdReset.Phase != phase {
				phase, phaseSince = status.ColdReset.Phase, time.Now()
			}
			report(phase)
			if phase == "complete" && status.State == "running" {
				return c.coldResetCredentials(ctx, envID, &EnvWakeResponse{
					EnvID: envID, State: status.State, Endpoint: status.Endpoint,
				})
			}
		}

		resendAfter := coldResetUnclaimedAfter
		stalledFor := time.Since(lastSent)
		if ours {
			resendAfter = coldResetResumeAfter
			if phase == "stopped" {
				resendAfter = coldResetAdvanceAfter
			}
			if since := time.Since(phaseSince); since < stalledFor {
				stalledFor = since
			}
		}
		if stalledFor < resendAfter {
			continue
		}
		if result, err = c.sendColdReset(ctx, envID, request); err != nil {
			return nil, err
		}
		lastSent = time.Now()
		if result.Phase == "waiting" {
			report("waiting")
		}
	}
}

// sendColdReset posts one cold-reset request, retrying transport failures and
// 5xx responses. The per-hour reset limit is final; the looser request limit
// backs off and retries.
func (c *Client) sendColdReset(ctx context.Context, envID string, request coldResetRequest) (*EnvWakeResponse, error) {
	backoff := coldResetPollInterval
	for {
		result, err := c.coldResetStepContext(ctx, envID, request)
		if err == nil {
			if result.EnvID != "" && result.EnvID != envID {
				return nil, fmt.Errorf("cold reset returned environment %q, expected %q", result.EnvID, envID)
			}
			return result, nil
		}
		if !retryableColdResetError(err) {
			return nil, err
		}
		wait := backoff
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusTooManyRequests {
			wait = coldResetMaxBackoff
		}
		if err := sleepContext(ctx, wait); err != nil {
			return nil, fmt.Errorf("cold reset did not complete: %w", err)
		}
		backoff = min(backoff*2, coldResetMaxBackoff)
	}
}

// coldResetCredentials returns the fresh incarnation's endpoint and secret.
// A completed reset only reports that the environment is running; the secret
// comes from an idempotent reauth.
func (c *Client) coldResetCredentials(ctx context.Context, envID string, result *EnvWakeResponse) (*EnvWakeResponse, error) {
	for {
		if result.Secret == "" {
			reauth, err := c.ReauthEnvironmentContext(ctx, envID)
			if err != nil {
				if !retryableColdResetError(err) {
					return nil, fmt.Errorf("cold reset reauth: %w", err)
				}
			} else if reauth.EnvID != "" && reauth.EnvID != envID {
				return nil, fmt.Errorf("cold reset reauth returned environment %q, expected %q", reauth.EnvID, envID)
			} else {
				result.Secret = reauth.Secret
			}
		}
		if result.Endpoint == "" {
			status, err := c.GetEnvironmentContext(ctx, envID)
			if err == nil && status.State == "running" {
				result.Endpoint = status.Endpoint
			} else if err != nil && !retryableColdResetError(err) {
				return nil, fmt.Errorf("cold reset status: %w", err)
			}
		}
		if result.Secret != "" && result.Endpoint != "" {
			result.EnvID = envID
			result.State = "running"
			return result, nil
		}
		if err := sleepContext(ctx, coldResetPollInterval); err != nil {
			return nil, fmt.Errorf("cold reset did not complete: %w", err)
		}
	}
}

// coldResetByReplay drives servers that run both reset phases inside the POST:
// it re-sends the same operation until the response reports the fresh VM.
func (c *Client) coldResetByReplay(ctx context.Context, envID string, request coldResetRequest, result *EnvWakeResponse) (*EnvWakeResponse, error) {
	for {
		if result.State == "running" {
			return c.coldResetCredentials(ctx, envID, result)
		}
		if result.Phase != "cold_stopped" {
			if err := sleepContext(ctx, coldResetPollInterval); err != nil {
				return nil, fmt.Errorf("cold reset did not complete: %w", err)
			}
		}
		var err error
		if result, err = c.sendColdReset(ctx, envID, request); err != nil {
			return nil, err
		}
	}
}

func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (c *Client) coldResetStepContext(ctx context.Context, envID string, request coldResetRequest) (*EnvWakeResponse, error) {
	return doControlPlaneJSONContext[EnvWakeResponse](
		ctx, c, "POST", "/environments/"+envID+"/cold-reset", request,
	)
}

// coldResetLimitCode marks the per-hour cold-reset limit, which retrying
// cannot satisfy.
const coldResetLimitCode = "cold_reset_limit"

func retryableColdResetError(err error) bool {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		if apiErr.Code == coldResetLimitCode {
			return false
		}
		return apiErr.StatusCode == http.StatusTooManyRequests || apiErr.StatusCode >= 500
	}
	return true
}

// retryableColdResetStatusError also tolerates 409: the status route reports a
// lifecycle identity conflict while a phase transition is being committed.
func retryableColdResetStatusError(err error) bool {
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusConflict {
		return true
	}
	return retryableColdResetError(err)
}

type ReauthResponse struct {
	EnvID  string `json:"env_id"`
	Secret string `json:"secret"`
}

func (c *Client) ReauthEnvironment(envID string) (*ReauthResponse, error) {
	return c.ReauthEnvironmentContext(context.Background(), envID)
}

// ReauthEnvironmentContext is ReauthEnvironment with caller-controlled cancellation.
func (c *Client) ReauthEnvironmentContext(ctx context.Context, envID string) (*ReauthResponse, error) {
	return doControlPlaneJSONContext[ReauthResponse](ctx, c, "POST", "/environments/"+envID+"/reauth", nil)
}

// Funnel events contain activation metadata only. MCP callers must never put
// tool arguments or output in this request.
type FunnelEventRequest struct {
	Stage         string `json:"stage"`
	Tool          string `json:"tool,omitempty"`
	ClientVersion string `json:"client_version,omitempty"`
}

type FunnelEventResponse struct {
	Accepted bool `json:"accepted"`
	First    bool `json:"first"`
}

func (c *Client) RecordFunnelEventContext(ctx context.Context, event FunnelEventRequest) (*FunnelEventResponse, error) {
	return doControlPlaneJSONContext[FunnelEventResponse](ctx, c, "POST", "/events", event)
}

// Domains

type AddDomainRequest struct {
	Zone    string `json:"zone"`
	CFToken string `json:"cf_token"`
}

type DomainResponse struct {
	DomainID    string `json:"domain_id"`
	Zone        string `json:"zone"`
	CFZoneID    string `json:"cf_zone_id"`
	CFAccountID string `json:"cf_account_id"`
	State       string `json:"state"`
	CreatedAt   string `json:"created_at"`
}

type ListDomainsResponse struct {
	Domains []DomainResponse `json:"domains"`
}

type DomainCredentials struct {
	CFToken     string `json:"cf_token"`
	CFZoneID    string `json:"cf_zone_id"`
	CFAccountID string `json:"cf_account_id"`
}

func (c *Client) AddDomain(zone, cfToken string) (*DomainResponse, error) {
	return doControlPlaneJSON[DomainResponse](c, "POST", "/domains", AddDomainRequest{Zone: zone, CFToken: cfToken})
}

func (c *Client) ListDomains() (*ListDomainsResponse, error) {
	return doControlPlaneJSON[ListDomainsResponse](c, "GET", "/domains", nil)
}

func (c *Client) RemoveDomain(domainID string) (*DomainResponse, error) {
	return doControlPlaneJSON[DomainResponse](c, "DELETE", "/domains/"+domainID, nil)
}

func (c *Client) ResolveDomainCredentials(zone string) (*DomainCredentials, error) {
	return doControlPlaneJSON[DomainCredentials](c, "GET", "/domains/resolve?zone="+url.QueryEscape(zone), nil)
}

// HTTP helpers

// doControlPlaneJSON sends a request to the current host-shim control-plane
// API. A failed request is returned directly; there is no unversioned fallback.
func doControlPlaneJSON[T any](c *Client, method, path string, body any) (*T, error) {
	return doControlPlaneJSONContext[T](context.Background(), c, method, path, body)
}

func doControlPlaneJSONContext[T any](ctx context.Context, c *Client, method, path string, body any) (*T, error) {
	return doJSONContext[T](ctx, c, method, controlPlaneV2Prefix+path, body)
}

func doJSON[T any](c *Client, method, path string, body any) (*T, error) {
	return doJSONContext[T](context.Background(), c, method, path, body)
}

func doJSONContext[T any](ctx context.Context, c *Client, method, path string, body any) (*T, error) {
	if c.baseURLErr != nil {
		return nil, c.baseURLErr
	}

	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		bodyReader = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bodyReader)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		var errResp struct {
			Error string `json:"error"`
			Code  string `json:"code"`
		}
		json.Unmarshal(respBody, &errResp)
		msg := errResp.Error
		if msg == "" {
			msg = string(respBody)
		}
		return nil, &APIError{StatusCode: resp.StatusCode, Message: msg, Code: errResp.Code}
	}

	var result T
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return &result, nil
}

// ---- Teams & shared environments (ADR-020) ----

type Team struct {
	TeamID  string `json:"team_id"`
	Name    string `json:"name"`
	Role    string `json:"role"`
	Status  string `json:"status"`
	IsOwner bool   `json:"is_owner"`
	EnvCap  int    `json:"env_cap"`
}

type CreateTeamResponse struct {
	TeamID string `json:"team_id"`
	Name   string `json:"name"`
	Role   string `json:"role"`
}

type ListTeamsResponse struct {
	Teams []Team `json:"teams"`
}

type TeamMember struct {
	UserID string `json:"user_id"`
	Email  string `json:"email"`
	Role   string `json:"role"`
	Status string `json:"status"`
}

type ListMembersResponse struct {
	TeamID  string       `json:"team_id"`
	Members []TeamMember `json:"members"`
}

type TeamEnv struct {
	EnvID         string `json:"env_id"`
	State         string `json:"state"`
	Endpoint      string `json:"endpoint"`
	Alias         string `json:"alias"`
	LeasedByEmail string `json:"leased_by_email"`
	LeaseExpiry   string `json:"lease_expiry"`
}

type ListTeamEnvsResponse struct {
	TeamID       string    `json:"team_id"`
	Environments []TeamEnv `json:"environments"`
}

type messageResponse struct {
	Status string `json:"status"`
}

func (c *Client) CreateTeam(name string) (*CreateTeamResponse, error) {
	return doControlPlaneJSON[CreateTeamResponse](c, "POST", "/teams", map[string]string{"name": name})
}

func (c *Client) ListTeams() (*ListTeamsResponse, error) {
	return doControlPlaneJSON[ListTeamsResponse](c, "GET", "/teams", nil)
}

func (c *Client) InviteMember(teamID, email string) error {
	_, err := doControlPlaneJSON[messageResponse](c, "POST", "/teams/"+teamID+"/invite", map[string]string{"email": email})
	return err
}

func (c *Client) AcceptInvite(teamID string) error {
	_, err := doControlPlaneJSON[messageResponse](c, "POST", "/teams/"+teamID+"/accept", nil)
	return err
}

func (c *Client) ListMembers(teamID string) (*ListMembersResponse, error) {
	return doControlPlaneJSON[ListMembersResponse](c, "GET", "/teams/"+teamID+"/members", nil)
}

func (c *Client) ListTeamEnvironments(teamID string) (*ListTeamEnvsResponse, error) {
	return doControlPlaneJSON[ListTeamEnvsResponse](c, "GET", "/teams/"+teamID+"/environments", nil)
}

func (c *Client) CreateTeamEnvironment(teamID, profileID string) (*CreateEnvResponse, error) {
	body := map[string]string{"team_id": teamID}
	if profileID != "" {
		body["profile_id"] = profileID
	}
	return doControlPlaneJSON[CreateEnvResponse](c, "POST", "/environments", body)
}

// AcquireResponse is the result of leasing a team env: a fresh one-time secret
// to authenticate with, the endpoint, and when the lease expires.
type AcquireResponse struct {
	EnvID       string `json:"env_id"`
	Secret      string `json:"secret"`
	Endpoint    string `json:"endpoint"`
	LeaseExpiry string `json:"lease_expiry"`
}

func (c *Client) AcquireEnvironment(envID string) (*AcquireResponse, error) {
	return doControlPlaneJSON[AcquireResponse](c, "POST", "/environments/"+envID+"/acquire", nil)
}

func (c *Client) ReleaseEnvironment(envID string) error {
	_, err := doControlPlaneJSON[messageResponse](c, "POST", "/environments/"+envID+"/release", nil)
	return err
}

func (c *Client) HeartbeatEnvironment(envID string) error {
	_, err := doControlPlaneJSON[messageResponse](c, "POST", "/environments/"+envID+"/heartbeat", nil)
	return err
}
