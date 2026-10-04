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
