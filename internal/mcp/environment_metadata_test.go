package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/agend-sh/cli/internal/api"
)

func TestEnvironmentMetadataToolUpdatesByEnvironmentID(t *testing.T) {
	received := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch || r.URL.Path != "/v2/environments/env-123" {
			http.NotFound(w, r)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		received <- body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"env_id":"env-123","alias":"ular-repo","banner":"Rocq development","state":"running"}`))
	}))
	defer server.Close()

	mcpServer := NewServer(api.New(server.URL, "token"), "test")
	result, isError := mcpServer.envUpdate(map[string]any{
		"environment": "env-123",
		"name":        "ular-repo",
		"description": "Rocq development",
	})
	if isError {
		t.Fatalf("env_update failed: %s", result)
	}
	body := <-received
	if body["alias"] != "ular-repo" || body["banner"] != "Rocq development" {
		t.Fatalf("unexpected metadata request: %#v", body)
	}
}

func TestEnvironmentMetadataToolsAreAdvertised(t *testing.T) {
	definitions := toolDefinitions()
	want := map[string]bool{
		"env_create": false, "env_update": false, "env_status": false, "env_cold_reset": false,
	}
	for _, definition := range definitions {
		name, _ := definition["name"].(string)
		if _, tracked := want[name]; tracked {
			want[name] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Fatalf("MCP tool %s is not advertised", name)
		}
	}
}

func TestColdResetToolRequiresDiagnosticReason(t *testing.T) {
	mcpServer := NewServer(api.New("http://localhost:1", "token"), "test")
	result, isError := mcpServer.envColdReset(context.Background(), "env-123", "   ")
	if !isError || result != "reason is required" {
		t.Fatalf("envColdReset = (%q, %v), want required-reason error", result, isError)
	}

	for _, definition := range toolDefinitions() {
		if definition["name"] != "env_cold_reset" {
			continue
		}
		schema := definition["inputSchema"].(map[string]any)
		required := schema["required"].([]string)
		if len(required) != 2 || required[0] != "environment" || required[1] != "reason" {
			t.Fatalf("cold reset required fields = %#v", required)
		}
		return
	}
	t.Fatal("env_cold_reset tool is not advertised")
}
