package db

import (
	"bytes"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestIsDBError(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want bool
	}{
		"nil":                     {err: nil, want: false},
		"connection":              {err: errors.New("connection refused"), want: true},
		"no connection to server": {err: errors.New("no connection to the server"), want: true},
		"canceling statement":     {err: errors.New("canceling statement due to conflict"), want: true},
		"context canceled":        {err: errors.New("context canceled"), want: true},
		"context deadline":        {err: errors.New("context deadline exceeded"), want: true},
		"pgx error":               {err: pgx.ErrNoRows, want: false}, // not a connection error
		"other":                   {err: errors.New("something else"), want: false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := IsDBError(tc.err); got != tc.want {
				t.Errorf("IsDBError(%v) = %v, want %v", tc.err, got, tc.want)
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
			if got := IsRedisError(tc.err); got != tc.want {
				t.Errorf("IsRedisError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestIsDBError_PostgresErrors(t *testing.T) {
	// pgx errors are not connection errors.
	for _, err := range []error{pgx.ErrNoRows, pgx.ErrTxClosed} {
		if IsDBError(err) {
			t.Errorf("pgx error %v should not be classified as connection error", err)
		}
	}
}

func TestServiceUnavailable(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(io.Discard)

	w := httptest.NewRecorder()
	ServiceUnavailable(w, "postgres", errors.New("connection refused"))

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", w.Code, http.StatusServiceUnavailable)
	}
	if !bytes.Contains(w.Body.Bytes(), []byte("postgres")) {
		t.Errorf("body should mention service: %s", w.Body.String())
	}
}
