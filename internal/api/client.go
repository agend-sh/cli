package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"
)

const DefaultBaseURL = "https://api.agend.sh"

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
			Timeout: 30 * time.Second,
		},
	}
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
	Endpoint string `json:"endpoint"`
	Secret   string `json:"secret"`
	State    string `json:"state"`
}

type EnvStatusResponse struct {
	EnvID      string `json:"env_id"`
	State      string `json:"state"`
	Endpoint   string `json:"endpoint"`
	Tier       string `json:"tier"`
	Secret     string `json:"secret,omitempty"`
	CreatedAt  string `json:"created_at"`
	LastActive string `json:"last_active"`
}

type EnvStopResponse struct {
	EnvID string `json:"env_id"`
	State string `json:"state"`
}

type EnvWakeResponse struct {
	EnvID    string `json:"env_id"`
	Endpoint string `json:"endpoint"`
	Secret   string `json:"secret"`
	State    string `json:"state"`
}

type ListEnvsResponse struct {
	Environments []EnvSummary `json:"environments"`
}

type EnvSummary struct {
	EnvID      string `json:"env_id"`
	Alias      string `json:"alias"`
	State      string `json:"state"`
	Endpoint   string `json:"endpoint"`
	Tier       string `json:"tier"`
	CreatedAt  string `json:"created_at"`
	LastActive string `json:"last_active"`
}

func (c *Client) ListEnvironments() (*ListEnvsResponse, error) {
	return doJSON[ListEnvsResponse](c, "GET", "/environments", nil)
}

func (c *Client) CreateEnvironment() (*CreateEnvResponse, error) {
	return doJSON[CreateEnvResponse](c, "POST", "/environments", nil)
}

func (c *Client) GetEnvironment(envID string) (*EnvStatusResponse, error) {
	return doJSON[EnvStatusResponse](c, "GET", "/environments/"+envID, nil)
}

func (c *Client) StopEnvironment(envID string) (*EnvStopResponse, error) {
	return doJSON[EnvStopResponse](c, "DELETE", "/environments/"+envID, nil)
}

func (c *Client) WakeEnvironment(envID string) (*EnvWakeResponse, error) {
	return doJSON[EnvWakeResponse](c, "POST", "/environments/"+envID+"/wake", nil)
}

type ReauthResponse struct {
	EnvID  string `json:"env_id"`
	Secret string `json:"secret"`
}

func (c *Client) ReauthEnvironment(envID string) (*ReauthResponse, error) {
	return doJSON[ReauthResponse](c, "POST", "/environments/"+envID+"/reauth", nil)
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
	return doJSON[DomainResponse](c, "POST", "/domains", AddDomainRequest{Zone: zone, CFToken: cfToken})
}

func (c *Client) ListDomains() (*ListDomainsResponse, error) {
	return doJSON[ListDomainsResponse](c, "GET", "/domains", nil)
}

func (c *Client) RemoveDomain(domainID string) (*DomainResponse, error) {
	return doJSON[DomainResponse](c, "DELETE", "/domains/"+domainID, nil)
}

func (c *Client) ResolveDomainCredentials(zone string) (*DomainCredentials, error) {
	return doJSON[DomainCredentials](c, "GET", "/domains/resolve?zone="+zone, nil)
}

// HTTP helpers

func doJSON[T any](c *Client, method, path string, body any) (*T, error) {
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

	req, err := http.NewRequest(method, c.baseURL+path, bodyReader)
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
		}
		json.Unmarshal(respBody, &errResp)
		msg := errResp.Error
		if msg == "" {
			msg = string(respBody)
		}
		return nil, &APIError{StatusCode: resp.StatusCode, Message: msg}
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
	return doJSON[CreateTeamResponse](c, "POST", "/teams", map[string]string{"name": name})
}

func (c *Client) ListTeams() (*ListTeamsResponse, error) {
	return doJSON[ListTeamsResponse](c, "GET", "/teams", nil)
}

func (c *Client) InviteMember(teamID, email string) error {
	_, err := doJSON[messageResponse](c, "POST", "/teams/"+teamID+"/invite", map[string]string{"email": email})
	return err
}

func (c *Client) AcceptInvite(teamID string) error {
	_, err := doJSON[messageResponse](c, "POST", "/teams/"+teamID+"/accept", nil)
	return err
}

func (c *Client) ListMembers(teamID string) (*ListMembersResponse, error) {
	return doJSON[ListMembersResponse](c, "GET", "/teams/"+teamID+"/members", nil)
}

func (c *Client) ListTeamEnvironments(teamID string) (*ListTeamEnvsResponse, error) {
	return doJSON[ListTeamEnvsResponse](c, "GET", "/teams/"+teamID+"/environments", nil)
}

func (c *Client) CreateTeamEnvironment(teamID string) (*CreateEnvResponse, error) {
	return doJSON[CreateEnvResponse](c, "POST", "/environments", map[string]string{"team_id": teamID})
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
	return doJSON[AcquireResponse](c, "POST", "/environments/"+envID+"/acquire", nil)
}

func (c *Client) ReleaseEnvironment(envID string) error {
	_, err := doJSON[messageResponse](c, "POST", "/environments/"+envID+"/release", nil)
	return err
}

func (c *Client) HeartbeatEnvironment(envID string) error {
	_, err := doJSON[messageResponse](c, "POST", "/environments/"+envID+"/heartbeat", nil)
	return err
}
