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

type geoRecord struct {
	Country struct {
		ISOCode string `maxminddb:"iso_code"`
	} `maxminddb:"country"`
}

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

// countryOf returns the ISO 3166-1 alpha-2 code ("CN") for an address or
// network ("1.2.3.0/24"), or "" when unknown.
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
		return ""
	}
	return rec.Country.ISOCode
}
