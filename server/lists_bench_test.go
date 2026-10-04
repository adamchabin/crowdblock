package main

import (
	"net/netip"
	"net/url"
	"testing"
)

// Bench fixtures mirror what production actually feeds these functions:
// ipListEntry entries coming out of queryIPList (reporters desc, ip asc) and
// the GeoIP DB-IP file. Keeping sizes close to real lists (thousands) matters
// because the hot path is encodeList → newListPayload called for every
// (min_reporters, minutes) cell of the 5×4 grid per generation.

func benchEntries(n int) []ipListEntry {
	entries := make([]ipListEntry, n)
	for i := range n {
		// Reporters descending so atLeast (leading slice) is exercised like prod.
		entries[i] = ipListEntry{
			IP:                "203.0.113." + itoa(i%254+1),
			DistinctReporters: n - i,
			Country:           "CN",
			CountryName:       "China",
			Sources:           []string{"auth", "fail2ban"},
		}
	}
	return entries
}

// itoa without strconv to avoid the import just for a benchmark helper.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

func BenchmarkEncodeList(b *testing.B) {
	for _, n := range []int{100, 1000, 5000} {
		entries := benchEntries(n)
		b.Run("entries="+itoa(n), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_, _ = encodeList(entries)
			}
		})
	}
}

func BenchmarkNewListPayload(b *testing.B) {
	for _, n := range []int{100, 1000, 5000} {
		entries := benchEntries(n)
		b.Run("entries="+itoa(n), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_, _ = newListPayload(entries)
			}
		})
	}
}

// buildLists exercises all five min_reporters thresholds per window, the full
// per-window work path. Size matches queryIPList(max window) output.
func BenchmarkBuildLists(b *testing.B) {
	entries := benchEntries(5000)
	b.ReportAllocs()
	for b.Loop() {
		_, _ = buildLists(entries)
	}
}

// countryOf against the real DB-IP Lite file: lookup cost with/without name.
func BenchmarkCountryOf(b *testing.B) {
	db, err := openGeoIPForBench("geoip/dbip-country-lite.mmdb")
	if err != nil {
		b.Fatal(err)
	}
	r := &RealGeoDBReader{db: db}
	addr := netip.MustParseAddr("8.8.8.8")

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		_, _ = countryOf(addr, r)
	}
}

// parseListParams validates the query string of GET /api/v1/ips on every
// cached or DB-backed request.
func BenchmarkParseListParams(b *testing.B) {
	q := url.Values{"min_reporters": []string{"10"}, "minutes": []string{"1440"}}
	for b.Loop() {
		_, _, _ = parseListParams(q)
	}
}
