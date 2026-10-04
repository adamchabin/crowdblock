package main

import (
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWriteListPayload(t *testing.T) {
	list := make([]ipListEntry, 5000)
	for i := range list {
		list[i] = ipListEntry{IP: "203.0.113.1", DistinctReporters: 3, Country: "CN"}
	}
	p, err := newListPayload(list)
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		accept string
		gzip   bool
	}{
		{"", false},
		{"gzip", true},
		{"gzip, deflate, br", true},
		{"br;q=1.0, GZIP;q=0.5", true},
		{"gzip;q=0", false},
		{"deflate", false},
	} {
		req := httptest.NewRequest("GET", "/api/v1/ips", nil)
		req.Header.Set("Accept-Encoding", tc.accept)
		rec := httptest.NewRecorder()
		writeListPayload(rec, req, p)

		var body io.Reader = rec.Body
		size := rec.Body.Len()
		if got := rec.Header().Get("Content-Encoding") == "gzip"; got != tc.gzip {
			t.Fatalf("Accept-Encoding %q: gzip = %v, want %v", tc.accept, got, tc.gzip)
		}
		if tc.gzip {
			gz, err := gzip.NewReader(rec.Body)
			if err != nil {
				t.Fatal(err)
			}
			body = gz
		}

		var decoded []ipListEntry
		if err := json.NewDecoder(body).Decode(&decoded); err != nil || len(decoded) != len(list) {
			t.Fatalf("Accept-Encoding %q: decoded %d entries, err %v", tc.accept, len(decoded), err)
		}
		if rec.Header().Get("Vary") != "Accept-Encoding" || rec.Header().Get("ETag") != p.etag {
			t.Errorf("headers: %v", rec.Header())
		}
		t.Logf("Accept-Encoding %-22q -> %d bytes", tc.accept, size)
	}

	// The client already has this version.
	req := httptest.NewRequest("GET", "/api/v1/ips", nil)
	req.Header.Set("If-None-Match", p.etag)
	rec := httptest.NewRecorder()
	writeListPayload(rec, req, p)
	if rec.Code != 304 || rec.Body.Len() != 0 {
		t.Errorf("If-None-Match: %d, %d bytes", rec.Code, rec.Body.Len())
	}
}

func TestEncodeList(t *testing.T) {
	for _, tc := range []struct {
		entries []ipListEntry
		want    string
	}{
		{nil, "[]\n"},
		{[]ipListEntry{}, "[]\n"},
		{[]ipListEntry{{IP: "1.2.3.4", DistinctReporters: 3}},
			"[\n{\"ip\":\"1.2.3.4\",\"distinct_reporters\":3}\n]\n"},
		{[]ipListEntry{{IP: "1.2.3.4", DistinctReporters: 3, Country: "CN", Sources: []string{"auth"}}, {IP: "5.6.7.8", DistinctReporters: 1}},
			"[\n{\"ip\":\"1.2.3.4\",\"distinct_reporters\":3,\"country\":\"CN\",\"sources\":[\"auth\"]},\n{\"ip\":\"5.6.7.8\",\"distinct_reporters\":1}\n]\n"},
	} {
		got, err := encodeList(tc.entries)
		if err != nil || string(got) != tc.want {
			t.Errorf("encodeList = %q (err %v), want %q", got, err, tc.want)
		}
		// Still a plain JSON array for any client.
		var decoded []ipListEntry
		if err := json.Unmarshal(got, &decoded); err != nil || len(decoded) != len(tc.entries) {
			t.Errorf("not valid JSON: %q (%v)", got, err)
		}
		// One entry per line.
		if n := strings.Count(string(got), "\n"); len(tc.entries) > 0 && n != len(tc.entries)+2 {
			t.Errorf("%d lines for %d entries", n, len(tc.entries))
		}
	}
}
