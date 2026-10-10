package main

import (
	"log"
	"net/netip"
	"os"

	"github.com/oschwald/maxminddb-golang/v2"
)

// Country database (DB-IP Lite, see scripts/update_geoip.sh), relative to the
// working directory unless GEOIP_DB is set. Optional: without it,
// GET /api/v1/ips just leaves out the country.
const defaultGeoIPDB = "geoip/dbip-country-lite.mmdb"

var geoDB *maxminddb.Reader

}

// CountryNameMap maps 2-letter ISO codes to full English names. Populated with common
// countries needing expanded display name. Extend this map as needed.
var CountryNameMap = map[string]string{
	"PL": "Poland",
	"DK": "Denmark",
	"NL": "Netherlands",
	"DE": "Germany",
	"FR": "France",
	// Add other countries needing full name display here
}

type geoRecord struct {
	Country struct {
		ISOCode string `maxminddb:"iso_code"`
		// Potential addition for full name if it existed in the DB schema.
		// For now, we rely on our hardcoded map check first.
	} `maxminddb:"country"`
}

func openGeoIP() {

func openGeoIP() {
	path := os.Getenv("GEOIP_DB")
	if path == "" {
		path = defaultGeoIPDB
	}

	db, err := maxminddb.Open(path)
	if err != nil {
		log.Printf("warning: GeoIP database not loaded, IP lists will have no country: %v", err)
		return
	}

	geoDB = db
	log.Printf("GeoIP database %s loaded (%s)", path, db.Metadata.DatabaseType)
}

// countryOf returns the full English name for an address or network ("Poland"),
// or the 2-letter ISO code if the full name is not mapped, with "" when unknown.
func countryOf(ip string) string {
	if geoDB == nil {
		return ""
	}

	addr, err := netip.ParseAddr(ip)
	if err != nil {
		prefix, err := netip.ParsePrefix(ip)
		if err != nil {
			return ""
		}
		addr = prefix.Addr()
	}

	var rec geoRecord
	if err := geoDB.Lookup(addr).Decode(&rec); err != nil {
		log.Printf("GeoIP lookup failed for %s: %v", ip, err) // Log failure but return ""
		return ""
	}

	isoCode := rec.Country.ISOCode
	if name, ok := CountryNameMap[isoCode]; ok {
		return name // Use the full English name from our map
	}
	return isoCode // Fall back to the standard 2-letter ISO code
}
