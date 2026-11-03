package list

import (
	"net/netip"
	"testing"

	"server/internal/db"
)

// Bench fixtures mirror what production actually feeds these functions:
// db.IPListEntry entries coming out of db.QueryIPList (reporters desc, ip asc)
// and the GeoIP DB-IP file. Keeping sizes close to real lists (thousands)
// matters because the hot path is encodeList → NewListPayload called for every
// (min_reporters, minutes) cell of the 5×4 grid per generation.

func benchEntries(n int) []db.IPListEntry {
	entries := make([]db.IPListEntry, n)
	for i := range n {
		// Reporters descending so atLeast (leading slice) is exercised like prod.
		entries[i] = db.IPListEntry{
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
				_, _ = NewListPayload(entries)
			}
		})
	}
}

// buildLists exercises all five min_reporters thresholds per window, the full
// per-window work path. Size matches db.QueryIPList(max window) output.
func BenchmarkBuildLists(b *testing.B) {
	entries := benchEntries(5000)
	b.ReportAllocs()
	for b.Loop() {
		_, _ = buildLists(entries)
	}
}

// atLeast is on the hot path of every generation.
func BenchmarkAtLeast(b *testing.B) {
	entries := benchEntries(5000)
	b.ReportAllocs()
	for b.Loop() {
		_ = atLeast(entries, 5)
	}
}

// The address of a router is parsed and canonicalized on every report.
func BenchmarkNetipParse(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		_, _ = netip.ParseAddr("203.0.113.42")
	}
}
