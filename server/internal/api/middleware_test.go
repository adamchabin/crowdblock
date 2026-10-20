package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"server/internal/db"
)

func TestMiddleware_Recovery(t *testing.T) {
	h := RecoveryMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("test panic")
	}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Code != http.StatusInternalServerError {
		t.Errorf("recoveryMiddleware: status = %d, want 500", w.Code)
	}
	if !strings.Contains(w.Body.String(), "server error") {
		t.Errorf("body should contain 'server error': %s", w.Body.String())
	}
}

func TestMiddleware_Timeout(t *testing.T) {
	// The handler sees a context with a deadline.
	var hasDeadline bool
	h := TimeoutMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, hasDeadline = r.Context().Deadline()
		w.WriteHeader(http.StatusOK)
	}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if !hasDeadline {
		t.Error("timeoutMiddleware: handler context has no deadline")
	}
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func TestMiddlewareChain(t *testing.T) {
	h := RecoveryMiddleware(TimeoutMiddleware(LogRequests(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}))))

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Code != http.StatusOK {
		t.Errorf("middleware chain: status = %d, want 200", w.Code)
	}
	if w.Body.String() != "ok" {
		t.Errorf("body = %q, want 'ok'", w.Body.String())
	}
}

func TestMiddleware_ContextPassed(t *testing.T) {
	var called bool
	h := RecoveryMiddleware(TimeoutMiddleware(LogRequests(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := r.Context().Value(logNotesKey{}).(*logNotes); ok {
			called = true
		}
		w.WriteHeader(http.StatusOK)
	}))))

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if !called {
		t.Error("inner handler should be called through middleware chain")
	}
}

func TestAPIKeyFromRequest(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-API-Key", "sfw_header")
	if got := apiKeyFromRequest(r); got != "sfw_header" {
		t.Errorf("apiKeyFromRequest = %q, want the header value", got)
	}

	// Clients that cannot set headers send the key as the Basic auth password.
	r = httptest.NewRequest(http.MethodGet, "/", nil)
	r.SetBasicAuth("user", "sfw_basic")
	if got := apiKeyFromRequest(r); got != "sfw_basic" {
		t.Errorf("apiKeyFromRequest = %q, want the Basic auth password", got)
	}

	r = httptest.NewRequest(http.MethodGet, "/", nil)
	if got := apiKeyFromRequest(r); got != "" {
		t.Errorf("apiKeyFromRequest = %q, want empty", got)
	}
}

func TestHandleHealth(t *testing.T) {
	// The health endpoint needs a database and Redis; without them the
	// handler would panic, so skip when they are not configured.
	if db.DB == nil || db.RDB == nil {
		t.Skip("database not initialized, skipping health check test")
	}

	r := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	w := httptest.NewRecorder()
	HandleHealth(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("health: status = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"status":"ok"`) {
		t.Errorf("health: body = %s, want status ok", w.Body.String())
	}
}

func TestTimeoutContextHonoured(t *testing.T) {
	// A handler that waits for the context to be done must be released.
	done := make(chan struct{})
	h := TimeoutMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
		close(done)
	}))

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx))

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Error("handler was not released by the context")
	}
}
