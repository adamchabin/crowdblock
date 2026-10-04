package main

import (
	"log"
	"os"
	"strings"
)

// DEBUG=true (or 1) also logs what happens behind the requests: list
// generation rounds, why a delta falls back to the full list, cache hits,
// accepted reports. Verbose – development and troubleshooting only.
var debug = isTrue(os.Getenv("DEBUG"))

func isTrue(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// debugf logs with a "DEBUG " prefix when DEBUG is enabled.
func debugf(format string, args ...any) {
	if debug {
		log.Printf("DEBUG "+format, args...)
	}
}
