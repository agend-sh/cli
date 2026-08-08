package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/agend-sh/cli/internal/auth"
)

func TestEnvEditUpdatesNameAndDescription(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	received := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch || r.URL.Path != "/v2/environments/env-123" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer metadata-token" {
			http.Error(w, "missing bearer token", http.StatusUnauthorized)
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

	if err := auth.SaveToken("metadata-token"); err != nil {
		t.Fatal(err)
	}
	if err := auth.SaveAPIURL(server.URL); err != nil {
		t.Fatal(err)
	}
	command := newEnvEditCmd()
	command.SetArgs([]string{
		"env-123", "--name", "ular-repo", "--description", "Rocq development",
	})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	body := <-received
	if body["alias"] != "ular-repo" || body["banner"] != "Rocq development" {
		t.Fatalf("unexpected metadata request: %#v", body)
	}
}

func TestEnvEditRejectsConflictingClearFlags(t *testing.T) {
	command := newEnvEditCmd()
	command.SetArgs([]string{"env-123", "--name", "ular-repo", "--clear-name"})
	if err := command.Execute(); err == nil {
		t.Fatal("conflicting name flags were accepted")
	}
}
