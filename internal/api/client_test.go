package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

func TestRecordFunnelEventSendsOnlyActivationMetadata(t *testing.T) {
	var got FunnelEventRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/events" {
			t.Fatalf("path = %q, want /v2/events", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"accepted":true,"first":true}`))
	}))
	defer server.Close()

	resp, err := New(server.URL, "token").RecordFunnelEventContext(
		context.Background(),
		FunnelEventRequest{
			Stage: "mcp_first_success", Tool: "shell_exec", ClientVersion: "v2.0.0",
		},
	)
	if err != nil {
		t.Fatalf("RecordFunnelEventContext: %v", err)
	}
	if !resp.Accepted || !resp.First {
		t.Fatalf("response = %+v", resp)
	}
	want := FunnelEventRequest{
		Stage: "mcp_first_success", Tool: "shell_exec", ClientVersion: "v2.0.0",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("request = %+v, want %+v", got, want)
	}
}

func TestValidateBaseURL(t *testing.T) {
	valid := []string{
		"https://api.agend.sh",
		"https://api.example.com:8443",
		"http://localhost:8080",
		"http://127.0.0.1:3000",
		"http://[::1]:3000",
	}
	for _, u := range valid {
		if err := validateBaseURL(u); err != nil {
			t.Errorf("validateBaseURL(%q) = %v, want nil", u, err)
		}
	}

	invalid := []string{
		"http://api.agend.sh",
		"http://evil.example.com",
		"http://10.0.0.5:8080", // private but not loopback — still cleartext on a network
		"ftp://api.agend.sh",
		"api.agend.sh", // no scheme
	}
	for _, u := range invalid {
		if err := validateBaseURL(u); err == nil {
			t.Errorf("validateBaseURL(%q) = nil, want error", u)
		}
	}
}

func TestClientHTTPTimeoutWidensOnlyLoopbackDevelopmentEndpoints(t *testing.T) {
	for _, test := range []struct {
		baseURL string
		want    time.Duration
	}{
		{baseURL: "https://api.agend.sh", want: defaultHTTPTimeout},
		{baseURL: "https://api.example.com:8443", want: defaultHTTPTimeout},
		{baseURL: "http://127.0.0.1:18000", want: loopbackHTTPTimeout},
		{baseURL: "http://[::1]:18000", want: loopbackHTTPTimeout},
		{baseURL: "http://localhost:18000", want: loopbackHTTPTimeout},
		{baseURL: "not a URL", want: defaultHTTPTimeout},
	} {
		t.Run(test.baseURL, func(t *testing.T) {
			if got := clientHTTPTimeout(test.baseURL); got != test.want {
				t.Fatalf("clientHTTPTimeout(%q) = %s, want %s", test.baseURL, got, test.want)
			}
			if got := New(test.baseURL, "token").httpClient.Timeout; got != test.want {
				t.Fatalf("New(%q) timeout = %s, want %s", test.baseURL, got, test.want)
			}
		})
	}
}

func responseError[T any](_ *T, err error) error {
	return err
}

func TestAuthenticationRoutesRemainUnversioned(t *testing.T) {
	requests := make(chan string, 3)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r.Method + " " + r.URL.RequestURI()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	client := New(server.URL, "")
	tests := []struct {
		name string
		want string
		call func() error
	}{
		{"signup", "POST /auth/signup", func() error { return responseError(client.Signup("dev@example.com", "secret")) }},
		{"login", "POST /auth/login", func() error { return responseError(client.Login("dev@example.com", "secret")) }},
		{"github", "POST /auth/github", func() error { return responseError(client.GitHubAuth("code")) }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.call(); err != nil {
				t.Fatalf("request failed: %v", err)
			}
			if got := <-requests; got != tt.want {
				t.Fatalf("request = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestManagementRoutesUseControlPlaneV2(t *testing.T) {
	requests := make(chan string, 22)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r.Method + " " + r.URL.RequestURI()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	client := New(server.URL, "token")
	tests := []struct {
		name string
		want string
		call func() error
	}{
		{"list environments", "GET /v2/environments", func() error { return responseError(client.ListEnvironments()) }},
		{"create environment", "POST /v2/environments", func() error { return responseError(client.CreateEnvironment("")) }},
		{"list profiles", "GET /v2/profiles", func() error { return responseError(client.ListProfiles("")) }},
		{"list team profiles", "GET /v2/profiles?team_id=team-1", func() error { return responseError(client.ListProfiles("team-1")) }},
		{"get environment", "GET /v2/environments/env-1", func() error { return responseError(client.GetEnvironment("env-1")) }},
		{"stop environment", "DELETE /v2/environments/env-1", func() error { return responseError(client.StopEnvironment("env-1")) }},
		{"wake environment", "POST /v2/environments/env-1/wake", func() error { return responseError(client.WakeEnvironment("env-1")) }},
		{"reauth environment", "POST /v2/environments/env-1/reauth", func() error { return responseError(client.ReauthEnvironment("env-1")) }},
		{"record funnel event", "POST /v2/events", func() error {
			return responseError(client.RecordFunnelEventContext(context.Background(), FunnelEventRequest{
				Stage: "mcp_first_success", Tool: "shell_exec", ClientVersion: "v2.0.0",
			}))
		}},
		{"add domain", "POST /v2/domains", func() error { return responseError(client.AddDomain("example.com", "token")) }},
		{"list domains", "GET /v2/domains", func() error { return responseError(client.ListDomains()) }},
		{"remove domain", "DELETE /v2/domains/domain-1", func() error { return responseError(client.RemoveDomain("domain-1")) }},
		{"resolve domain", "GET /v2/domains/resolve?zone=dev.example.com", func() error { return responseError(client.ResolveDomainCredentials("dev.example.com")) }},
		{"create team", "POST /v2/teams", func() error { return responseError(client.CreateTeam("team")) }},
		{"list teams", "GET /v2/teams", func() error { return responseError(client.ListTeams()) }},
		{"invite member", "POST /v2/teams/team-1/invite", func() error { return client.InviteMember("team-1", "dev@example.com") }},
		{"accept invite", "POST /v2/teams/team-1/accept", func() error { return client.AcceptInvite("team-1") }},
		{"list members", "GET /v2/teams/team-1/members", func() error { return responseError(client.ListMembers("team-1")) }},
		{"list team environments", "GET /v2/teams/team-1/environments", func() error { return responseError(client.ListTeamEnvironments("team-1")) }},
		{"create team environment", "POST /v2/environments", func() error { return responseError(client.CreateTeamEnvironment("team-1", "")) }},
		{"acquire environment", "POST /v2/environments/env-1/acquire", func() error { return responseError(client.AcquireEnvironment("env-1")) }},
		{"release environment", "POST /v2/environments/env-1/release", func() error { return client.ReleaseEnvironment("env-1") }},
		{"heartbeat environment", "POST /v2/environments/env-1/heartbeat", func() error { return client.HeartbeatEnvironment("env-1") }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.call(); err != nil {
				t.Fatalf("request failed: %v", err)
			}
			if got := <-requests; got != tt.want {
				t.Fatalf("request = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestControlPlaneV2DoesNotFallBack(t *testing.T) {
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"no v2 worker available"}`))
	}))
	defer server.Close()

	_, err := New(server.URL, "token").CreateEnvironment("")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusNotFound {
		t.Fatalf("CreateEnvironment error = %v, want APIError 404", err)
	}
	if want := []string{"/v2/environments"}; !reflect.DeepEqual(requests, want) {
		t.Fatalf("requests = %v, want exactly %v", requests, want)
	}
}

func TestReauthEnvironmentContextCancelsInFlightRequest(t *testing.T) {
	entered := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := New(server.URL, "token").ReauthEnvironmentContext(ctx, "env-1")
		result <- err
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("reauth request did not reach control plane")
	}
	started := time.Now()
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("reauth cancellation error = %v, want context.Canceled", err)
		}
		if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
			t.Fatalf("reauth cancellation took %s", elapsed)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reauth request ignored context cancellation")
	}
}
