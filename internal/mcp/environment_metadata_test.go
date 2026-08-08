package mcp

import (
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
	want := map[string]bool{"env_create": false, "env_update": false, "env_status": false}
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
