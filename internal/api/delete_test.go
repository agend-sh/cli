package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func fastDeletePacing(t *testing.T) {
	t.Helper()
	poll, resend, backoff := deletePollInterval, deleteResendAfter, coldResetMaxBackoff
	deletePollInterval = time.Millisecond
	deleteResendAfter = 30 * time.Millisecond
	coldResetMaxBackoff = 5 * time.Millisecond
	t.Cleanup(func() { deletePollInterval, deleteResendAfter, coldResetMaxBackoff = poll, resend, backoff })
}

// deleteServer fakes a control plane that stops a deleted VM in the
// background. del answers each DELETE; state answers each GET.
type deleteServer struct {
	mu       sync.Mutex
	deletes  int
	statuses int
	del      func(n int) (int, string)
	state    func(n int) string
}

func (f *deleteServer) start(t *testing.T) *Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodDelete && r.URL.Path == "/v2/environments/env-1":
			f.deletes++
			code, body := f.del(f.deletes)
			w.WriteHeader(code)
			_, _ = w.Write([]byte(body))
		case r.Method == http.MethodGet && r.URL.Path == "/v2/environments/env-1":
			f.statuses++
			_, _ = w.Write([]byte(`{"env_id":"env-1","state":"` + f.state(f.statuses) + `"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return New(server.URL, "token")
}

const deletePending = `{"env_id":"env-1","state":"delete_cleanup","cleanup_pending":true}`

func TestDeleteWaitsForTheBackgroundStop(t *testing.T) {
	fastDeletePacing(t)
	fake := &deleteServer{
		del: func(int) (int, string) { return http.StatusAccepted, deletePending },
		state: func(n int) string {
			if n < 3 {
				return "delete_cleanup"
			}
			return "deleted"
		},
	}
	resp, err := fake.start(t).DeleteEnvironmentContext(context.Background(), "env-1")
	if err != nil {
		t.Fatalf("DeleteEnvironmentContext: %v", err)
	}
	if resp.EnvID != "env-1" || resp.State != "deleted" {
		t.Fatalf("resp = %+v", resp)
	}
	if fake.deletes != 1 || fake.statuses != 3 {
		t.Fatalf("deletes = %d statuses = %d, want one delete then polling", fake.deletes, fake.statuses)
	}
}

func TestDeleteReturnsAtOnceWhenAlreadyDeleted(t *testing.T) {
	fastDeletePacing(t)
	fake := &deleteServer{
		del:   func(int) (int, string) { return http.StatusOK, `{"env_id":"env-1","state":"deleted"}` },
		state: func(int) string { return "deleted" },
	}
	resp, err := fake.start(t).DeleteEnvironmentContext(context.Background(), "env-1")
	if err != nil || resp.State != "deleted" || fake.statuses != 0 {
		t.Fatalf("resp = %+v err = %v statuses = %d", resp, err, fake.statuses)
	}
}

func TestDeleteRetriesServerErrorsAndRepeatsAStalledDelete(t *testing.T) {
	fastDeletePacing(t)
	fake := &deleteServer{}
	fake.del = func(n int) (int, string) {
		if n == 1 {
			return http.StatusServiceUnavailable, `{"message":"Service Unavailable"}`
		}
		return http.StatusAccepted, deletePending
	}
	// The stop only completes after the stalled delete is repeated.
	fake.state = func(int) string {
		if fake.deletes < 3 {
			return "delete_cleanup"
		}
		return "deleted"
	}
	resp, err := fake.start(t).DeleteEnvironmentContext(context.Background(), "env-1")
	if err != nil || resp.State != "deleted" {
		t.Fatalf("resp = %+v err = %v", resp, err)
	}
	if fake.deletes != 3 {
		t.Fatalf("deletes = %d, want the 503 retried and the stalled delete repeated once", fake.deletes)
	}
}

func TestDeleteReportsClientErrors(t *testing.T) {
	fastDeletePacing(t)
	fake := &deleteServer{
		del:   func(int) (int, string) { return http.StatusForbidden, `{"error":"forbidden"}` },
		state: func(int) string { return "running" },
	}
	_, err := fake.start(t).DeleteEnvironmentContext(context.Background(), "env-1")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusForbidden || fake.deletes != 1 {
		t.Fatalf("err = %v deletes = %d, want one forbidden delete", err, fake.deletes)
	}
}
