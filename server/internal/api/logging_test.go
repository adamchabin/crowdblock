package api

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

	h := LogRequests(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

	h := LogRequests(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		addLogNote(r, "list", "full")
		addLogNote(r, "enc", "gzip")
		addLogNote(r, "from", "cache")
		addLogNote(r, "tier", "1/1440")
		setLogRecords(r, 42)
		w.Write([]byte("x"))
	}))

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/api/v1/ips", nil))

	line := strings.TrimSpace(out.String())
	want := ` 200 in=0B out=1B \S+ list=full enc=gzip from=cache tier=1/1440 records=42$`
	if !regexp.MustCompile(want).MatchString(line) {
		t.Errorf("log %q does not match %q", line, want)
	}
}

func TestLogNotesNoDuplicateRecords(t *testing.T) {
	// String() must not append the records note twice when it is called again.
	notes := &logNotes{parts: []string{"list=full"}, records: 5, recordsSet: true}
	first := notes.String()
	second := notes.String()
	if first != second {
		t.Errorf("String() is not stable: %q vs %q", first, second)
	}
	if n := strings.Count(second, "records="); n != 1 {
		t.Errorf("records= appears %d times: %q", n, second)
	}
}

func TestLogRequestsWithoutNotes(t *testing.T) {
	var out bytes.Buffer
	log.SetOutput(&out)
	defer log.SetOutput(io.Discard)

	LogRequests(http.NotFoundHandler()).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/x", nil))
	if line := strings.TrimSpace(out.String()); !regexp.MustCompile(` 404 in=0B out=19B \S+$`).MatchString(line) {
		t.Errorf("log without notes: %q", line)
	}
}
