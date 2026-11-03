package main

import (
	"net/netip"
	"regexp"
)

func init() {
	register(&Plugin{
		Name:        "auth",
		Description: "failed / hostile SSH logins (sshd messages in auth.log)",
		DefaultLog:  "/var/log/auth.log",
		Parse: func(line string) (netip.Addr, string, bool) {
			ip, ok := ParseAuthLine(line)
			return ip, "sshd", ok
		},
	})
}

// sshd messages (OpenSSH, as written to /var/log/auth.log) that mean a failed
// or hostile login attempt. Each pattern captures the remote address in
// group 1. Successful logins and ordinary disconnects don't match.
var authPatterns = []*regexp.Regexp{
	// Failed password for root from 1.2.3.4 port 22 ssh2
	// Failed password for invalid user admin from 1.2.3.4 port 22 ssh2
	// Failed publickey for root from 1.2.3.4 port 22 ssh2: RSA SHA256:...
	regexp.MustCompile(`sshd.*: Failed \S+ for (?:invalid user )?.* from (\S+) port \d+`),
	// Invalid user admin from 1.2.3.4 port 22
	regexp.MustCompile(`sshd.*: Invalid user .* from (\S+) port \d+`),
	// error: maximum authentication attempts exceeded for root from 1.2.3.4 port 22 ssh2 [preauth]
	regexp.MustCompile(`sshd.*: (?:error: )?maximum authentication attempts exceeded for .* from (\S+) port \d+`),
	// Disconnecting authenticating user root 1.2.3.4 port 22: Too many authentication failures [preauth]
	regexp.MustCompile(`sshd.*: Disconnecting (?:authenticating|invalid) user .* (\S+) port \d+: Too many authentication failures`),
	// Did not receive identification string from 1.2.3.4 port 22
	regexp.MustCompile(`sshd.*: Did not receive identification string from (\S+)`),
	// banner exchange: Connection from 1.2.3.4 port 22: invalid format
	regexp.MustCompile(`sshd.*: banner exchange: Connection from (\S+) port \d+: invalid format`),
	// Unable to negotiate with 1.2.3.4 port 22: no matching key exchange method found.
	regexp.MustCompile(`sshd.*: Unable to negotiate with (\S+) port \d+`),
}

// ParseAuthLine returns the attacking address from an auth.log line, or
// ok=false when the line is not a failed/hostile login attempt.
func ParseAuthLine(line string) (ip netip.Addr, ok bool) {
	for _, re := range authPatterns {
		m := re.FindStringSubmatch(line)
		if m == nil {
			continue
		}

		addr, err := netip.ParseAddr(m[1])
		if err != nil {
			return netip.Addr{}, false
		}
		return addr.Unmap(), true
	}
	return netip.Addr{}, false
}

// Special-purpose ranges not covered by netip's Is* methods (same list as
// RESERVED in the OpenWrt client's ip.uc, which would ignore them anyway).
var reservedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"), // CGNAT
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"), // documentation
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"), // documentation
	netip.MustParsePrefix("203.0.113.0/24"),  // documentation
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("2001:db8::/32"), // documentation
}

// IsReportable is false for addresses that must never be reported: loopback,
// private, link-local, multicast, unspecified and other special-purpose
// ranges (i.e. our own networks).
func IsReportable(ip netip.Addr) bool {
	if !ip.IsValid() || !ip.IsGlobalUnicast() || ip.IsPrivate() {
		return false
	}
	for _, p := range reservedPrefixes {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}
