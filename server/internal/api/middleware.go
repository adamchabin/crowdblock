package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// logNotes are details a handler adds to its request's log line, e.g.
// "list=full enc=gzip from=cache tier=1/1440".
type logNotes struct {
	parts      []string
	records    int  // number of IP records sent in this response
	recordsSet bool // true once records is set for this request
}

type logNotesKey struct{}

func (n *logNotes) String() string {
	if len(n.parts) == 0 && !n.recordsSet {
		return ""
	}
	parts := make([]string, 0, len(n.parts)+1)
	parts = append(parts, n.parts...)
	if n.recordsSet {
		parts = append(parts, fmt.Sprintf("records=%d", n.records))
	}
	return " " + strings.Join(parts, " ")
}

// addLogNote appends "key=value" to the log line of the request.
func addLogNote(r *http.Request, key string, value any) {
	if n, ok := r.Context().Value(logNotesKey{}).(*logNotes); ok {
		n.parts = append(n.parts, fmt.Sprintf("%s=%v", key, value))
	}
}

// setLogRecords records how many IP records were sent in this response,
// so the request log can show "records=N".
func setLogRecords(r *http.Request, n int) {
	if ln, ok := r.Context().Value(logNotesKey{}).(*logNotes); ok {
		ln.records = n
		ln.recordsSet = true
	}
}

// RecoveryMiddleware recovers from panics in any handler or middleware,
// so a single bad request cannot crash the whole server. A 500 is returned
// and the request is still logged by the outer LogRequests middleware.
func RecoveryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("request panic", "path", r.URL.Path, "remote", r.RemoteAddr, "rec", rec)
				http.Error(w, "server error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// TimeoutMiddleware wraps every request in a context with a timeout, so a
// slow or stuck handler cannot hold database connections and other resources
// indefinitely. The timeout is enforced by pgx/redis respecting the context.
func TimeoutMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		r = r.WithContext(ctx)
		next.ServeHTTP(w, r)
	})
}

// LogRequests logs every incoming HTTP request to the console: method, path,
// remote address, response status, bytes received / sent, and duration.
func LogRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		body := &countingReader{ReadCloser: http.MaxBytesReader(w, r.Body, MaxBodyBytes)}
		r.Body = body
		notes := &logNotes{}
		r = r.WithContext(context.WithValue(r.Context(), logNotesKey{}, notes))

		next.ServeHTTP(sw, r)

		msg := fmt.Sprintf("%s %s %s %d in=%s out=%s %s%s", r.RemoteAddr, r.Method, r.URL.Path, sw.status,
			formatBytes(body.n), formatBytes(sw.n), time.Since(start), notes)
		slog.Info(msg)
	})
}

// statusWriter tracks the status code and the number of written bytes.
type statusWriter struct {
	http.ResponseWriter
	status int
	n      int64 // response body bytes
}

func (sw *statusWriter) WriteHeader(status int) {
	sw.status = status
	sw.ResponseWriter.WriteHeader(status)
}

func (sw *statusWriter) Write(b []byte) (int, error) {
	n, err := sw.ResponseWriter.Write(b)
	sw.n += int64(n)
	return n, err
}

// countingReader counts the request body bytes actually read (Content-Length
// is missing for chunked requests).
type countingReader struct {
	io.ReadCloser
	n int64
}

func (cr *countingReader) Read(b []byte) (int, error) {
	n, err := cr.ReadCloser.Read(b)
	cr.n += int64(n)
	return n, err
}

// formatBytes: 512 -> "512B", 363580 -> "355.1KiB", 5242880 -> "5.0MiB"
func formatBytes(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%dB", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1fKiB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1fMiB", float64(n)/(1024*1024))
	}
}

// writeJSON writes a JSON response.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// apiKeyFromRequest returns the API key from the X-API-Key header or, for
// clients that cannot set custom headers, from the Basic auth password
// (the username is ignored).
func apiKeyFromRequest(r *http.Request) string {
	if key := r.Header.Get("X-API-Key"); key != "" {
		return key
	}
	if _, password, ok := r.BasicAuth(); ok {
		return password
	}
	return ""
}
