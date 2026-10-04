package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"testing"
)

func TestBuildLists(t *testing.T) {
	// Sorted as queryIPList returns: most reporters first.
	all := []ipListEntry{
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

		var plain []ipListEntry
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
