package main

import (
	"bytes"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

func TestLogRequestsSizes(t *testing.T) {
	var out bytes.Buffer
	log.SetOutput(&out)
	defer log.SetOutput(io.Discard)

	h := logRequests(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusCreated)
		w.Write(bytes.Repeat([]byte("x"), 2048))
	}))

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/api/v1/reports", strings.NewReader(`{"ip":"1.2.3.4"}`)))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/api/v1/ips", nil))

	for _, want := range []string{
		`POST /api/v1/reports 201 in=16B out=2.0KiB `,
		`GET /api/v1/ips 201 in=0B out=2.0KiB `,
	} {
		if !regexp.MustCompile(regexp.QuoteMeta(want)).MatchString(out.String()) {
			t.Errorf("log %q does not contain %q", out.String(), want)
		}
	}
}

func TestFormatBytes(t *testing.T) {
	for n, want := range map[int64]string{0: "0B", 512: "512B", 363580: "355.1KiB", 5 * 1024 * 1024: "5.0MiB"} {
		if got := formatBytes(n); got != want {
			t.Errorf("formatBytes(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestLogNotesForLists(t *testing.T) {
	var out bytes.Buffer
	log.SetOutput(&out)
	defer log.SetOutput(io.Discard)

	p, _ := newListPayload([]ipListEntry{{IP: "1.2.3.4", DistinctReporters: 3}})
	h := logRequests(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeListPayload(w, r, p)
		addLogNote(r, "from", "cache")
		addLogNote(r, "tier", "1/1440")
	}))

	for _, tc := range []struct {
		header, value, want string
	}{
		{"Accept-Encoding", "gzip", ` 200 in=0B out=\d+B \S+ list=full enc=gzip from=cache tier=1/1440$`},
		{"", "", ` 200 in=0B out=44B \S+ list=full enc=plain from=cache tier=1/1440$`},
		{"If-None-Match", p.etag, ` 304 in=0B out=0B \S+ list=not-modified from=cache tier=1/1440$`},
	} {
		out.Reset()
		req := httptest.NewRequest("GET", "/api/v1/ips", nil)
		if tc.header != "" {
			req.Header.Set(tc.header, tc.value)
		}
		h.ServeHTTP(httptest.NewRecorder(), req)

		line := strings.TrimSpace(out.String())
		if !regexp.MustCompile(tc.want).MatchString(line) {
			t.Errorf("log %q does not match %q", line, tc.want)
		}
	}

	// Requests without notes keep the plain format.
	out.Reset()
	logRequests(http.NotFoundHandler()).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/x", nil))
	if line := strings.TrimSpace(out.String()); !regexp.MustCompile(` 404 in=0B out=19B \S+$`).MatchString(line) {
		t.Errorf("log without notes: %q", line)
	}
}
