package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const DefaultBaseURL = "https://api.agend.sh"

type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

func New(baseURL, token string) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Client{
		baseURL: baseURL,
		token:   token,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
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
	UserID string `json:"user_id"`
	Token  string `json:"token"`
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
