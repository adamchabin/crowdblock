// Package logging provides the debug switch shared by the server packages.
package logging

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
)

// Debug is true when DEBUG=true (or 1): it also logs what happens behind the
// requests - list generation rounds, why a delta falls back to the full list,
// cache hits, accepted reports. Verbose - development and troubleshooting only.
var Debug = IsTrue(os.Getenv("DEBUG"))

// IsTrue reports whether v is a truthy value ("1", "true", "yes", "on").
func IsTrue(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// Debugf logs at debug level; visible when Debug enables debug logging.
func Debugf(format string, args ...any) {
	if Debug {
		slog.Debug(fmt.Sprintf(format, args...))
	}
}
