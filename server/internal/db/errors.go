package db

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"log/slog"

	"github.com/jackc/pgx/v5/pgconn"
)

// IsUniqueViolation reports whether err is a Postgres unique-constraint
// violation (SQLSTATE 23505).
func IsUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// IsDBError reports whether err is a database (Postgres) error, so handlers
// can return 503 Service Unavailable instead of a generic 500.
func IsDBError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "connection") ||
		strings.Contains(msg, "no connection to the server") ||
		strings.Contains(msg, "canceling statement") ||
		strings.Contains(msg, "context canceled") ||
		strings.Contains(msg, "context deadline exceeded")
}

// IsRedisError reports whether err is a Redis error.
func IsRedisError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "redis")
}

// ServiceUnavailable writes a 503 response naming the downstream service that
// failed, so operators can tell at a glance whether Postgres, Redis, or the
// app itself is at fault.
func ServiceUnavailable(w http.ResponseWriter, service string, err error) {
	slog.Warn("service unavailable", "service", service, "err", err)
	http.Error(w, fmt.Sprintf("service unavailable (%s)", service), http.StatusServiceUnavailable)
}
