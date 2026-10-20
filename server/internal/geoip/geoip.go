// Package geoip provides IP-to-country lookup using the MaxMind DB-IP database.
package geoip

import (
	"log/slog"
	"net/netip"

	maxminddb "github.com/oschwald/maxminddb-golang"
)

// GeoDBReader defines the interface needed for IP address lookups,
// enabling testing (mocking) in business logic modules.
type GeoDBReader interface {
	// Lookup finds the ISO code and English country name for the given IP.
	// Returns ("", "", error) on failure.
	Lookup(addr netip.Addr) (isoCode, name string, err error)
}

// GeoDB is the global reader, set after initialization completes.
var GeoDB GeoDBReader

// RealGeoDBReader wraps maxminddb.Reader.
type RealGeoDBReader struct {
	db *maxminddb.Reader
}

func (r *RealGeoDBReader) Lookup(addr netip.Addr) (isoCode, name string, err error) {
	var result struct {
		Country struct {
			ISOCode string            `maxminddb:"iso_code"`
			Names   map[string]string `maxminddb:"names"`
		} `maxminddb:"country"`
	}
	if err := r.db.Lookup(addr.AsSlice(), &result); err != nil {
		return "", "", err
	}
	return result.Country.ISOCode, result.Country.Names["en"], nil
}

// OpenGeoIP initializes the global database reader using the given file.
func OpenGeoIP(dbPath string) error {
	db, err := maxminddb.Open(dbPath)
	if err != nil {
		return err
	}
	GeoDB = &RealGeoDBReader{db: db}
	slog.Info("GeoIP DB loaded", "path", dbPath)
	return nil
}

// CountryOf returns the ISO 3166-1 alpha-2 code and the English country name
// for the address, or ("Unknown", "Unknown") when the lookup fails.
func CountryOf(addr netip.Addr, reader GeoDBReader) (code, name string) {
	if reader == nil {
		return "Unknown", "Unknown"
	}

	isoCode, name, err := reader.Lookup(addr)
	if err != nil || isoCode == "" {
		return "Unknown", "Unknown"
	}

	return isoCode, name
}
