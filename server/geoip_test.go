package main

import "testing"

func TestCountryOf(t *testing.T) {
	openGeoIP()
	if geoDB == nil {
		t.Skip("GeoIP database not available, run server/scripts/update_geoip.sh")
	}

	for ip, want := range map[string]string{
		"1.80.4.206":   "CN",
		"223.95.98.13": "CN",
		"8.8.8.8":      "US",
		"240e:3a1::1":  "CN",
		"1.80.0.0/13":  "CN",
		"10.0.0.1":     "",
		"bogus":        "",
	} {
		if got := countryOf(ip); got != want {
			t.Errorf("countryOf(%q) = %q, want %q", ip, got, want)
		}
	}
}
