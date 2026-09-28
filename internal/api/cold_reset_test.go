package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// fastColdResetPacing shrinks the cold-reset timers for one test.
func fastColdResetPacing(t *testing.T) {
	t.Helper()
	poll, advance, resume, unclaimed, backoff :=
		coldResetPollInterval, coldResetAdvanceAfter, coldResetResumeAfter, coldResetUnclaimedAfter, coldResetMaxBackoff
	coldResetPollInterval = time.Millisecond
	coldResetAdvanceAfter = 20 * time.Millisecond
	coldResetResumeAfter = time.Second
	coldResetUnclaimedAfter = 50 * time.Millisecond
	coldResetMaxBackoff = 5 * time.Millisecond
	t.Cleanup(func() {
		coldResetPollInterval, coldResetAdvanceAfter, coldResetResumeAfter, coldResetUnclaimedAfter, coldResetMaxBackoff =
			poll, advance, resume, unclaimed, backoff
	})
}

// coldResetServer fakes a control plane that runs reset phases in the
// background. post answers each POST /cold-reset; status answers each GET.
type coldResetServer struct {
	mu       sync.Mutex
	posts    []coldResetRequest
	statuses int
	reauths  int
	post     func(n int, request coldResetRequest) (int, string)
	status   func(n int, operationID string) string
}

func (f *coldResetServer) start(t *testing.T) *Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v2/environments/env-1/cold-reset":
			var request coldResetRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode request: %v", err)
			}
			f.posts = append(f.posts, request)
			code, body := f.post(len(f.posts), request)
			w.WriteHeader(code)
			_, _ = w.Write([]byte(body))
		case r.Method == http.MethodGet && r.URL.Path == "/v2/environments/env-1":
			f.statuses++
			operationID := ""
			if len(f.posts) > 0 {
				operationID = f.posts[0].OperationID
			}
			_, _ = w.Write([]byte(f.status(f.statuses, operationID)))
		case r.Method == http.MethodPost && r.URL.Path == "/v2/environments/env-1/reauth":
			f.reauths++
			_, _ = w.Write([]byte(`{"env_id":"env-1","secret":"fresh-secret"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return New(server.URL, "token")
}

func accepted(operationID, state, phase string) string {
	return `{"env_id":"env-1","state":"` + state + `","cold_reset_pending":true,"operation_id":"` +
		operationID + `","phase":"` + phase + `"}`
}

func statusBody(state, operationID, phase string) string {
	body := `{"env_id":"env-1","state":"` + state + `","endpoint":"fresh.test"`
	if operationID != "" {
		body += `,"cold_reset":{"operation_id":"` + operationID + `","phase":"` + phase + `"}`
	}
	return body + "}"
}

func TestColdResetPollsStatusInsteadOfResendingTheReset(t *testing.T) {
	fastColdResetPacing(t)
	fake := &coldResetServer{
		post: func(_ int, request coldResetRequest) (int, string) {
			return http.StatusAccepted, accepted(request.OperationID, "stop_cleanup", "stopping")
		},
		status: func(n int, operationID string) string {
			switch {
			case n < 3:
				return statusBody("stop_cleanup", operationID, "stopping")
			case n < 5:
				return statusBody("waking", operationID, "booting")
			default:
				return statusBody("running", operationID, "complete")
			}
		},
	}
	var phases []string
	result, err := fake.start(t).ColdResetEnvironmentProgress(
		context.Background(), "env-1", "guest shim unreachable", func(phase string) { phases = append(phases, phase) },
	)
	if err != nil {
		t.Fatalf("ColdResetEnvironmentProgress: %v", err)
	}
	if result.Secret != "fresh-secret" || result.Endpoint != "fresh.test" || result.State != "running" {
		t.Fatalf("result = %+v", result)
	}
	if len(fake.posts) != 1 {
		t.Fatalf("posts = %d, want 1: an accepted reset is polled, not re-sent", len(fake.posts))
	}
	if want := []string{"stopping", "booting", "complete"}; !reflect.DeepEqual(phases, want) {
		t.Fatalf("phases = %v, want %v", phases, want)
	}
}

func TestColdResetStopsAtTheHourlyResetLimit(t *testing.T) {
	fastColdResetPacing(t)
	fake := &coldResetServer{
		post: func(int, coldResetRequest) (int, string) {
			return http.StatusTooManyRequests,
				`{"error":"cold-reset limit reached (10 per hour); try again later","code":"cold_reset_limit"}`
		},
		status: func(int, string) string { return statusBody("running", "", "") },
	}
	_, err := fake.start(t).ColdResetEnvironmentContext(context.Background(), "env-1", "reason")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != coldResetLimitCode || !strings.Contains(err.Error(), "limit reached") {
		t.Fatalf("error = %v, want the hourly limit", err)
	}
	if len(fake.posts) != 1 {
		t.Fatalf("posts = %d, want 1: the hourly limit is final", len(fake.posts))
	}
}

func TestColdResetRetriesServerErrorsAndTheRequestLimit(t *testing.T) {
	fastColdResetPacing(t)
	fake := &coldResetServer{
		post: func(n int, request coldResetRequest) (int, string) {
			switch n {
			case 1:
				return http.StatusServiceUnavailable, `{"message":"Service Unavailable"}`
			case 2:
				return http.StatusTooManyRequests, `{"error":"too many cold-reset requests — please slow down"}`
			default:
				return http.StatusOK, `{"env_id":"env-1","state":"running","endpoint":"fresh.test","reauth_required":true,"operation_id":"` +
					request.OperationID + `","phase":"complete"}`
			}
		},
		status: func(int, string) string { return statusBody("running", "", "") },
	}
	result, err := fake.start(t).ColdResetEnvironmentContext(context.Background(), "env-1", "reason")
	if err != nil {
		t.Fatalf("ColdResetEnvironmentContext: %v", err)
	}
	if result.Secret != "fresh-secret" || result.Endpoint != "fresh.test" {
		t.Fatalf("result = %+v", result)
	}
	if len(fake.posts) != 3 || fake.posts[0] != fake.posts[1] || fake.posts[1] != fake.posts[2] {
		t.Fatalf("posts are not exact replays: %+v", fake.posts)
	}
}

func TestColdResetResendsWhenTheStoppedPhaseStalls(t *testing.T) {
	fastColdResetPacing(t)
	fake := &coldResetServer{}
	fake.post = func(n int, request coldResetRequest) (int, string) {
		if n == 1 {
			return http.StatusAccepted, accepted(request.OperationID, "stop_cleanup", "stopping")
		}
		return http.StatusAccepted, accepted(request.OperationID, "waking", "booting")
	}
	// The stop finished but nothing started the boot until the re-send.
	fake.status = func(_ int, operationID string) string {
		if len(fake.posts) < 2 {
			return statusBody("crashed", operationID, "stopped")
		}
		return statusBody("running", operationID, "complete")
	}
	result, err := fake.start(t).ColdResetEnvironmentContext(context.Background(), "env-1", "reason")
	if err != nil {
		t.Fatalf("ColdResetEnvironmentContext: %v", err)
	}
	if result.Secret != "fresh-secret" {
		t.Fatalf("result = %+v", result)
	}
	if len(fake.posts) != 2 || fake.posts[0] != fake.posts[1] {
		t.Fatalf("posts = %+v, want one exact re-send to start the boot", fake.posts)
	}
}

func TestColdResetResendsWhenTheServerNeverRecordedTheOperation(t *testing.T) {
	fastColdResetPacing(t)
	fake := &coldResetServer{
		post: func(n int, request coldResetRequest) (int, string) {
			if n == 1 {
				return http.StatusAccepted, accepted(request.OperationID, "stop_cleanup", "stopping")
			}
			return http.StatusOK, `{"env_id":"env-1","state":"running","endpoint":"fresh.test","reauth_required":true,"operation_id":"` +
				request.OperationID + `","phase":"complete"}`
		},
		// Another operation's marker is not progress for this reset.
		status: func(int, string) string {
			return statusBody("running", "ffffffffffffffffffffffffffffffff", "complete")
		},
	}
	result, err := fake.start(t).ColdResetEnvironmentContext(context.Background(), "env-1", "reason")
	if err != nil {
		t.Fatalf("ColdResetEnvironmentContext: %v", err)
	}
	if result.Secret != "fresh-secret" || len(fake.posts) != 2 {
		t.Fatalf("result = %+v posts = %d", result, len(fake.posts))
	}
}

func TestColdResetGivesUpAtItsDeadline(t *testing.T) {
	fastColdResetPacing(t)
	fake := &coldResetServer{
		post: func(_ int, request coldResetRequest) (int, string) {
			return http.StatusAccepted, accepted(request.OperationID, "stop_cleanup", "stopping")
		},
		status: func(_ int, operationID string) string { return statusBody("stop_cleanup", operationID, "stopping") },
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := fake.start(t).ColdResetEnvironmentContext(ctx, "env-1", "reason")
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want deadline", err)
	}
}
