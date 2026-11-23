package cmd

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/agend-sh/cli/internal/api"
	"github.com/agend-sh/cli/internal/auth"
)

func TestCommandsRequireExplicitEnvironment(t *testing.T) {
	commands := []string{
		"exec pwd", "ping", "connect", "watch", "resize 80 24", "input text", "interrupt",
		"file-get x", "file-put x text", "file-move x y", "task-output task", "task-stop task",
		"env status", "env edit", "env wake", "env delete", "env cold-reset --reason test",
		"env acquire", "env release", "env heartbeat",
	}
	for _, args := range commands {
		t.Run(args, func(t *testing.T) {
			cmd := NewRoot("dev")
			cmd.SetArgs(strings.Fields(args))
			err := cmd.Execute()
			want := "required flag(s) \"env\""
			if strings.HasPrefix(args, "env ") {
				want = "accepts 1 arg(s)"
			}
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("expected %q, got %v", want, err)
			}
		})
	}
}

func TestDirectWorkerUsesOnlyExplicitCredentialWhenControlPlaneOffline(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if err := auth.SaveToken("test-token"); err != nil {
		t.Fatal(err)
	}
	if err := auth.SaveEnvironment("env-first", "localhost:1234", "first-secret"); err != nil {
		t.Fatal(err)
	}
	if err := auth.SaveEnvironment("env-second", "localhost:5678", "second-secret"); err != nil {
		t.Fatal(err)
	}

	control := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer control.Close()
	target := environmentTarget{id: "env-first", api: api.New(control.URL, "test-token")}
	if err := target.resolve(context.Background()); err == nil {
		t.Fatal("ignored control-plane rejection")
	}

	control.Close()
	if err := target.resolve(context.Background()); err != nil {
		t.Fatal(err)
	}
	if target.endpoint != "localhost:1234" || target.secret != "first-secret" {
		t.Fatal("used wrong cached target")
	}
	missing := environmentTarget{id: "env-missing", api: target.api}
	if err := missing.resolve(context.Background()); err == nil {
		t.Fatal("resolved unknown environment while offline")
	}
}
