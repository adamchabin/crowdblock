package list

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"server/internal/db"
)

func TestBuildLists(t *testing.T) {
	// Sorted as db.QueryIPList returns: most reporters first.
	all := []db.IPListEntry{
		{IP: "1.1.1.1", DistinctReporters: 60},
		{IP: "2.2.2.2", DistinctReporters: 20},
		{IP: "3.3.3.3", DistinctReporters: 12, Country: "CN", Sources: []string{"auth"}},
		{IP: "4.4.4.4", DistinctReporters: 5},
		{IP: "5.5.5.5", DistinctReporters: 2},
		{IP: "6.6.6.6", DistinctReporters: 1},
	}

	lists, err := buildLists(all)
	if err != nil {
		t.Fatal(err)
	}

	want := map[int]int{1: 6, 5: 4, 10: 3, 20: 2, 50: 1}
	if len(lists) != len(want) {
		t.Fatalf("%d lists, want %d", len(lists), len(want))
	}
	for min, n := range want {
		p := lists[min]

		var plain []db.IPListEntry
		if err := json.Unmarshal(p.plain, &plain); err != nil || len(plain) != n {
			t.Errorf("min %d: plain has %d entries (err %v), want %d", min, len(plain), err, n)
		}

		zr, err := gzip.NewReader(bytes.NewReader(p.gz))
		if err != nil {
			t.Fatalf("min %d: gzip: %v", min, err)
		}
		unpacked, _ := io.ReadAll(zr)
		if !bytes.Equal(unpacked, p.plain) {
			t.Errorf("min %d: gzip version differs from plain", min)
		}

		if len(p.etag) != 18 || p.etag[0] != '"' {
			t.Errorf("min %d: etag %q", min, p.etag)
		}
	}

	if lists[1].etag == lists[5].etag {
		t.Error("different lists have the same etag")
	}
	again, _ := buildLists(all)
	if again[10].etag != lists[10].etag {
		t.Error("etag of the same content changed")
	}

	// Empty window: valid empty JSON array, not null.
	empty, _ := buildLists(nil)
	if got := string(empty[1].plain); got != "[]\n" {
		t.Errorf("empty list = %q", got)
	}
}

func TestEncodeList(t *testing.T) {
	for _, tc := range []struct {
		entries []db.IPListEntry
		want    string
	}{
		{nil, "[]\n"},
		{[]db.IPListEntry{}, "[]\n"},
		{[]db.IPListEntry{{IP: "1.2.3.4", DistinctReporters: 3}},
			"[\n{\"ip\":\"1.2.3.4\",\"distinct_reporters\":3}\n]\n"},
		{[]db.IPListEntry{{IP: "1.2.3.4", DistinctReporters: 3, Country: "CN", Sources: []string{"auth"}}, {IP: "5.6.7.8", DistinctReporters: 1}},
			"[\n{\"ip\":\"1.2.3.4\",\"distinct_reporters\":3,\"country\":\"CN\",\"sources\":[\"auth\"]},\n{\"ip\":\"5.6.7.8\",\"distinct_reporters\":1}\n]\n"},
	} {
		got, err := encodeList(tc.entries)
		if err != nil || string(got) != tc.want {
			t.Errorf("encodeList = %q (err %v), want %q", got, err, tc.want)
		}
		// Still a plain JSON array for any client.
		var decoded []db.IPListEntry
		if err := json.Unmarshal(got, &decoded); err != nil || len(decoded) != len(tc.entries) {
			t.Errorf("not valid JSON: %q (%v)", got, err)
		}
		// One entry per line.
		if n := strings.Count(string(got), "\n"); len(tc.entries) > 0 && n != len(tc.entries)+2 {
			t.Errorf("%d lines for %d entries", n, len(tc.entries))
		}
	}
}
