package main

import (
	"log/slog"
	"net/netip"

	maxminddb "github.com/oschwald/maxminddb-golang"
)

// GeoDBReader definiuje interfejs potrzebny do wyszukiwania adresów IP,
// co umożliwia testowanie (mockowanie) w modułach biznesowych.
type GeoDBReader interface {
	// Lookup wyszukuje kod ISO i angielską nazwę kraju dla danego IP.
	// Zwraca ("", "") w przypadku niepowodzenia.
	Lookup(addr netip.Addr) (isoCode, name string, err error)
}

// globalny czytnik, który będzie ustawiany po zakończeniu inicjalizacji.
var geoDB GeoDBReader

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

// openGeoIP inicjalizuje globalny czytnik bazy danych przy użyciu podanego pliku.
func openGeoIP(dbPath string) error {
	db, err := maxminddb.Open(dbPath)
	if err != nil {
		return err
	}
	geoDB = &RealGeoDBReader{db: db}
	slog.Info("GeoIP DB loaded", "path", dbPath)
	return nil
}

// countryOf returns the ISO 3166-1 alpha-2 code and the English country name
// for the address, or ("Unknown", "Unknown") when the lookup fails.
func countryOf(addr netip.Addr, reader GeoDBReader) (code, name string) {
	// 1. Lookup bazy danych za pomocą przekazanego czytnika
	isoCode, name, err := reader.Lookup(addr)

	if err != nil || isoCode == "" {
		return "Unknown", "Unknown"
	}

	return isoCode, name
}
