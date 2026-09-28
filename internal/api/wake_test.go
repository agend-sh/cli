package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// wakeServer fakes a control plane whose wake answers wake and whose GET
// answers state(n) on the n-th poll.
func wakeServer(t *testing.T, wake string, wakeCode int, state func(n int) string) (*Client, *int) {
	t.Helper()
	var mu sync.Mutex
	polls, reauths := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v2/environments/env-1/wake":
			w.WriteHeader(wakeCode)
			_, _ = w.Write([]byte(wake))
		case r.Method == http.MethodGet && r.URL.Path == "/v2/environments/env-1":
			polls++
			_, _ = w.Write([]byte(`{"env_id":"env-1","state":"` + state(polls) + `","endpoint":"woken.test"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v2/environments/env-1/reauth":
			reauths++
			_, _ = w.Write([]byte(`{"env_id":"env-1","secret":"fresh-secret"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return New(server.URL, "token"), &reauths
}

func TestWakeReturnsAFastRestoreDirectly(t *testing.T) {
	fastColdResetPacing(t)
	client, reauths := wakeServer(t,
		`{"env_id":"env-1","state":"running","endpoint":"woken.test","secret":"wake-secret"}`, http.StatusOK,
		func(int) string { t.Fatal("a fast wake polled status"); return "" })
	resp, err := client.WakeEnvironmentContext(context.Background(), "env-1")
	if err != nil || resp.Secret != "wake-secret" || resp.Endpoint != "woken.test" || *reauths != 0 {
		t.Fatalf("resp = %+v err = %v reauths = %d", resp, err, *reauths)
	}
}

func TestWakeWaitsForABackgroundBootAndReauths(t *testing.T) {
	fastColdResetPacing(t)
	client, reauths := wakeServer(t,
		`{"env_id":"env-1","state":"waking","wake_pending":true}`, http.StatusAccepted,
		func(n int) string {
			if n < 3 {
				return "waking"
			}
			return "running"
		})
	resp, err := client.WakeEnvironmentContext(context.Background(), "env-1")
	if err != nil {
		t.Fatalf("WakeEnvironmentContext: %v", err)
	}
	if resp.EnvID != "env-1" || resp.State != "running" || resp.Secret != "fresh-secret" ||
		resp.Endpoint != "woken.test" || *reauths != 1 {
		t.Fatalf("resp = %+v reauths = %d", resp, *reauths)
	}
}

func TestWakeReportsABackgroundBootThatFailed(t *testing.T) {
	fastColdResetPacing(t)
	client, _ := wakeServer(t,
		`{"env_id":"env-1","state":"waking","wake_pending":true}`, http.StatusAccepted,
		func(n int) string {
			if n < 2 {
				return "waking"
			}
			return "crashed"
		})
	_, err := client.WakeEnvironmentContext(context.Background(), "env-1")
	if err == nil || !strings.Contains(err.Error(), "environment is crashed") {
		t.Fatalf("err = %v, want the crashed state", err)
	}
}
