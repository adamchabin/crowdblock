package main

import (
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http/httptest"
	"testing"
)

func TestWriteJSONCompressed(t *testing.T) {
	list := make([]ipListEntry, 5000)
	for i := range list {
		list[i] = ipListEntry{IP: "203.0.113.1", DistinctReporters: 3, Country: "CN"}
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
		writeJSONCompressed(rec, req, 200, list)

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
		if rec.Header().Get("Vary") != "Accept-Encoding" {
			t.Errorf("missing Vary header")
		}
		t.Logf("Accept-Encoding %-22q -> %d bytes", tc.accept, size)
	}
}
