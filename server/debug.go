package main

import (
	"fmt"
	"log/slog"
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

// debugf logs at debug level; visible when DEBUG enables debug logging.
func debugf(format string, args ...any) {
	if debug {
		slog.Debug(fmt.Sprintf(format, args...))
	}
}
