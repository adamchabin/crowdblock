package list

import (
	"net/http/httptest"
	"testing"
)

// The request log's records= must count addresses even when the body is
// gzipped, where counting `"ip":` in the bytes finds nothing.
func TestWriteListReportsRecordsForGzip(t *testing.T) {
	gz, err := gzipBytes([]byte(`[{"ip":"1.2.3.4"},{"ip":"5.6.7.8"}]`))
	if err != nil {
		t.Fatal(err)
	}

	r := httptest.NewRequest("GET", "/api/v1/ips", nil)
	r.Header.Set("Accept-Encoding", "gzip")
	w := httptest.NewRecorder()

	meta := writeList(w, r, `"abc"`, gz, true, 2)
	if meta.Records != 2 {
		t.Fatalf("records = %d, want 2", meta.Records)
	}
}
