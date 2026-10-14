package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func e(ip string, reporters int, sources ...string) ipListEntry {
	return ipListEntry{IP: ip, DistinctReporters: reporters, Sources: sources}
}

func TestComputeDiff(t *testing.T) {
	prev := []ipListEntry{e("1.1.1.1", 5), e("2.2.2.2", 3, "auth"), e("3.3.3.3", 1)}
	cur := []ipListEntry{e("1.1.1.1", 5), e("2.2.2.2", 4, "auth"), e("4.4.4.4", 2)}

	d := computeDiff(prev, cur)
	if got := []string{d.Upsert[0].IP, d.Upsert[1].IP}; !reflect.DeepEqual(got, []string{"2.2.2.2", "4.4.4.4"}) || len(d.Upsert) != 2 {
		t.Errorf("upsert = %+v", d.Upsert)
	}
	if !reflect.DeepEqual(d.Remove, []string{"3.3.3.3"}) {
		t.Errorf("remove = %v", d.Remove)
	}

	// Same content, also after a JSON round trip (nil vs empty sources).
	var decoded []ipListEntry
	data, _ := json.Marshal(cur)
	json.Unmarshal(data, &decoded)
	if d := computeDiff(decoded, cur); len(d.Upsert) != 0 || len(d.Remove) != 0 {
		t.Errorf("no change, got %+v", d)
	}
}

func TestMergeDiffs(t *testing.T) {
	// v0 {1, 2, 3} -> v1 {1, 2*, 4} -> v2 {1, 3, 4, 5} -> v3 {1, 3, 5}
	v0 := []ipListEntry{e("1.1.1.1", 9), e("2.2.2.2", 3), e("3.3.3.3", 1)}
	v1 := []ipListEntry{e("1.1.1.1", 9), e("2.2.2.2", 4), e("4.4.4.4", 2)}
	v2 := []ipListEntry{e("1.1.1.1", 9), e("3.3.3.3", 2), e("4.4.4.4", 2), e("5.5.5.5", 7)}
	v3 := []ipListEntry{e("1.1.1.1", 9), e("3.3.3.3", 2), e("5.5.5.5", 7)}

	m := mergeDiffs([]listDiff{computeDiff(v0, v1), computeDiff(v1, v2), computeDiff(v2, v3)})

	// Applying the merged diff to v0 must give v3.
	set := map[string]ipListEntry{}
	for _, x := range v0 {
		set[x.IP] = x
	}
	for _, ip := range m.Remove {
		delete(set, ip)
	}
	for _, x := range m.Upsert {
		set[x.IP] = x
	}
	want := map[string]ipListEntry{}
	for _, x := range v3 {
		want[x.IP] = x
	}
	if !reflect.DeepEqual(set, want) {
		t.Errorf("v0 + merged diff = %v, want %v", set, want)
	}
	// 4.4.4.4 came and went: not in the merged diff at all... except as a
	// removal, which is harmless for a client that never had it.
	for _, x := range m.Upsert {
		if x.IP == "4.4.4.4" {
			t.Error("4.4.4.4 should not be upserted")
		}
	}
	// Most reported first.
	if m.Upsert[0].IP != "5.5.5.5" {
		t.Errorf("upsert order %+v", m.Upsert)
	}
}

func TestSetHash(t *testing.T) {
	a := setHash([]ipListEntry{e("1.1.1.1", 1), e("2.2.2.2", 9)})
	b := setHash([]ipListEntry{e("2.2.2.2", 3), e("1.1.1.1", 5, "auth")})
	if a != b || len(a) != 16 {
		t.Errorf("order / other fields must not matter: %q %q", a, b)
	}
	if a == setHash([]ipListEntry{e("1.1.1.1", 1)}) {
		t.Error("different sets, same hash")
	}
	// Clients compute it with `printf '1.1.1.1\n2.2.2.2\n' | sha256sum | cut -c1-16`.
	if a != "5b1199467bc59137" {
		t.Errorf("set hash = %s, want the sha256sum of the sorted lines", a)
	}
}

func TestEncodeDelta(t *testing.T) {
	body, err := encodeDelta(`"abc"`, listMeta{Count: 3, SetHash: "0123456789abcdef"},
		listDiff{Upsert: []ipListEntry{e("1.1.1.1", 2), e("2.2.2.2", 1, "auth")}, Remove: []string{"3.3.3.3"}})
	if err != nil {
		t.Fatal(err)
	}

	var d struct {
		ETag    string        `json:"etag"`
		Count   int           `json:"count"`
		SetHash string        `json:"set_hash"`
		Remove  []string      `json:"remove"`
		Upsert  []ipListEntry `json:"upsert"`
	}
	if err := json.Unmarshal(body, &d); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, body)
	}
	if d.ETag != `"abc"` || d.Count != 3 || len(d.Upsert) != 2 || d.Remove[0] != "3.3.3.3" {
		t.Errorf("decoded %+v", d)
	}
	// Starts with "{" (the client tells a delta from a full list by it) and
	// has one upsert entry per line.
	if body[0] != '{' || strings.Count(string(body), "\n") != 4 {
		t.Errorf("format:\n%s", body)
	}

	empty, _ := encodeDelta(`"x"`, listMeta{}, listDiff{Upsert: []ipListEntry{}, Remove: []string{}})
	if err := json.Unmarshal(empty, &d); err != nil {
		t.Errorf("empty delta not valid JSON: %s", empty)
	}
}
