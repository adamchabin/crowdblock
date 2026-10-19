package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestIsDBError(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want bool
	}{
		"nil":                   {err: nil, want: false},
		"connection":            {err: errors.New("connection refused"), want: true},
		"no connection to server": {err: errors.New("no connection to the server"), want: true},
		"canceling statement":   {err: errors.New("canceling statement due to conflict"), want: true},
		"context canceled":      {err: errors.New("context canceled"), want: true},
		"context deadline":      {err: errors.New("context deadline exceeded"), want: true},
		"pgx error":             {err: pgx.ErrNoRows, want: false}, // not a connection error
		"other":                 {err: errors.New("something else"), want: false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := isDBError(tc.err); got != tc.want {
				t.Errorf("isDBError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestIsRedisError(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want bool
	}{
		"nil":   {err: nil, want: false},
		"redis": {err: errors.New("redis: connection pool timeout"), want: true},
		"other": {err: errors.New("something else"), want: false},
		"REDIS": {err: errors.New("REDIS connection failed"), want: true},
	} {
		t.Run(name, func(t *testing.T) {
			if got := isRedisError(tc.err); got != tc.want {
				t.Errorf("isRedisError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestHandleHealth(t *testing.T) {
	// Test healthy - only works if db/rdb are initialized (integration test)
	// Skip if they're not available
	if db == nil || rdb == nil {
		t.Skip("db/rdb not initialized, skipping health check test")
	}
}

func TestHandleHealth_ResponseFormat(t *testing.T) {
	// Skip if db/rdb are not initialized (unit test environment)
	if db == nil || rdb == nil {
		t.Skip("db/rdb not initialized, skipping health check test")
	}

	r := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	w := httptest.NewRecorder()
	handleHealth(w, r)

	var resp struct {
		Status   string `json:"status"`
		Postgres string `json:"postgres"`
		Redis    string `json:"redis"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Status != "ok" && resp.Status != "degraded" {
		t.Errorf("unexpected status: %s", resp.Status)
	}
	if resp.Postgres != "up" && resp.Postgres != "down" {
		t.Errorf("unexpected postgres status: %s", resp.Postgres)
	}
	if resp.Redis != "up" && resp.Redis != "down" {
		t.Errorf("unexpected redis status: %s", resp.Redis)
	}
}

func TestServiceUnavailable(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(io.Discard)

	w := httptest.NewRecorder()
	serviceUnavailable(w, "postgres", errors.New("connection refused"))

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", w.Code, http.StatusServiceUnavailable)
	}
	if !strings.Contains(w.Body.String(), "postgres") {
		t.Errorf("body should mention service: %s", w.Body.String())
	}
}

func TestMiddleware_Recovery(t *testing.T) {
	h := recoveryMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	h := timeoutMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx))
	// Should timeout and return something
}

func TestLogNotesNonMutating(t *testing.T) {
	notes := &logNotes{parts: []string{"list=full"}, records: 5, recordsSet: true}
	s := notes.String()
	count := strings.Count(s, "records=")
	if count != 1 {
		t.Errorf("String() should not append records multiple times, got %d: %s", count, s)
	}
}

func TestIsDBError_PostgresErrors(t *testing.T) {
	// Test pgx-specific errors - they are not connection errors
	for _, err := range []error{
		pgx.ErrNoRows,
		pgx.ErrTxClosed,
	} {
		if isDBError(err) {
			t.Errorf("pgx error %v should not be classified as connection error", err)
		}
	}
}

func TestMiddlewareChain(t *testing.T) {
	h := recoveryMiddleware(timeoutMiddleware(logRequests(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}))))

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Errorf("middleware chain: status = %d, want 200", w.Code)
	}
	if w.Body.String() != "ok" {
		t.Errorf("body = %q, want 'ok'", w.Body.String())
	}
}

// Test that context is passed through middleware chain
func TestMiddleware_ContextPassed(t *testing.T) {
	var called bool
	h := recoveryMiddleware(timeoutMiddleware(logRequests(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := r.Context().Value(logNotesKey{}).(*logNotes); ok {
			called = true
		}
		w.WriteHeader(http.StatusOK)
	}))))

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	h.ServeHTTP(w, r)

	if !called {
		t.Errorf("inner handler should be called through middleware chain")
	}
}