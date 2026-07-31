package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/agend-sh/cli/internal/auth"
)

func TestEnvUseSelectsRunningRestoredEnvironmentWithFreshAuthority(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	var statusCalls atomic.Int32
	var reauthCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer control-plane-token" {
			http.Error(w, "missing bearer token", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v2/environments/env-restored":
			statusCalls.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"env_id":   "env-restored",
				"state":    "running",
				"endpoint": "https://restored.example.test",
				// Status authority may already have been consumed by another
				// client. The command must use reauth instead.
				"secret": "stale-status-secret",
			})
		case r.Method == http.MethodPost && r.URL.Path == "/v2/environments/env-restored/reauth":
			reauthCalls.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"env_id": "env-restored",
				"secret": "fresh-reauth-secret",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	if err := auth.SaveToken("control-plane-token"); err != nil {
		t.Fatal(err)
	}
	if err := auth.SaveAPIURL(server.URL); err != nil {
		t.Fatal(err)
	}

	command := newEnvUseCmd()
	command.SetArgs([]string{"env-restored"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}

	envID, endpoint, secret, sessionToken, err := auth.LoadEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	if envID != "env-restored" ||
		endpoint != "https://restored.example.test" ||
		secret != "fresh-reauth-secret" ||
		sessionToken != "" {
		t.Fatalf("unexpected selected environment: env=%q endpoint=%q secret=%q session=%q",
			envID, endpoint, secret, sessionToken)
	}
	if statusCalls.Load() != 1 || reauthCalls.Load() != 1 {
		t.Fatalf("unexpected API calls: status=%d reauth=%d",
			statusCalls.Load(), reauthCalls.Load())
	}
}
