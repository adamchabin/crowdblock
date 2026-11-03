package geoip

import (
	"net/netip"
	"testing"
)

// MockGeoDBReader is a test fake of GeoDBReader with canned ISO codes and names
// matching the addresses used in the tests below.
type MockGeoDBReader struct{}

// Lookup returns a canned (ISO code, name) for known test addresses and ("", "") for the rest.
func (m *MockGeoDBReader) Lookup(addr netip.Addr) (string, string, error) {
	switch addr.String() {
	case "192.0.2.1":
		return "US", "United States", nil
	case "88.99.10.10":
		return "PL", "Poland", nil
	case "203.0.113.42":
		return "SG", "Singapore", nil
	default:
		return "", "", nil
	}
}

func TestCountryOf_Success(t *testing.T) {
	// Case 1: USA (known address)
	mockReader := &MockGeoDBReader{}
	addrUS, _ := netip.ParseAddr("192.0.2.1")
	code, name := CountryOf(addrUS, mockReader)
	if code != "US" || name != "United States" {
		t.Errorf("Expected ('US', 'United States'), got ('%s', '%s')", code, name)
	}

	// Case 2: Poland (known address)
	addrPL, _ := netip.ParseAddr("88.99.10.10")
	code, name = CountryOf(addrPL, mockReader)
	if code != "PL" || name != "Poland" {
		t.Errorf("Expected ('PL', 'Poland'), got ('%s', '%s')", code, name)
	}

	// Case 3: Singapore (known address)
	addrSG, _ := netip.ParseAddr("203.0.113.42")
	code, name = CountryOf(addrSG, mockReader)
	if code != "SG" || name != "Singapore" {
		t.Errorf("Expected ('SG', 'Singapore'), got ('%s', '%s')", code, name)
	}
}

func TestCountryOf_FailureHandling(t *testing.T) {
	mockReader := &MockGeoDBReader{}

	// Case 1: Unknown IP (mock returns empty code)
	addrUnknown, _ := netip.ParseAddr("99.99.99.99")
	code, name := CountryOf(addrUnknown, mockReader)
	if code != "Unknown" || name != "Unknown" {
		t.Errorf("Expected ('Unknown', 'Unknown') for unknown IP, got: ('%s', '%s')", code, name)
	}
}

func TestCountryOf_NilReader(t *testing.T) {
	// A nil reader (GeoIP not loaded) must not panic.
	addr, _ := netip.ParseAddr("8.8.8.8")
	code, name := CountryOf(addr, nil)
	if code != "Unknown" || name != "Unknown" {
		t.Errorf("Expected ('Unknown', 'Unknown'), got ('%s', '%s')", code, name)
	}
}
