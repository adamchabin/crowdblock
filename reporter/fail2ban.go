package main

import (
	"net/netip"
	"regexp"
)

func init() {
	register(&Plugin{
		Name:        "fail2ban",
		Description: "addresses banned by fail2ban (any jail)",
		DefaultLog:  "/var/log/fail2ban.log",
		// fail2ban has already applied its own thresholds: a ban is an attack.
		Threshold: 1,
		Parse: func(line string) (netip.Addr, string, bool) {
			jail, ip, ok := ParseFail2banLine(line)
			return ip, "jail " + jail, ok
		},
	})
}

// 2026-10-03 21:00:01,234 fail2ban.actions        [812]: NOTICE  [sshd] Ban 61.177.172.10
//
// Only fresh bans: "Restore Ban" (re-applied after a fail2ban restart) is an
// old attack, "Unban" and "Found" (single failures) are not bans.
var fail2banBan = regexp.MustCompile(`fail2ban\.actions\s*\[\d+\]:\s*NOTICE\s+\[([^\]]+)\]\s+Ban\s+(\S+)\s*$`)

// ParseFail2banLine returns the jail and address of a ban in fail2ban.log,
// or ok=false for any other line.
func ParseFail2banLine(line string) (jail string, ip netip.Addr, ok bool) {
	m := fail2banBan.FindStringSubmatch(line)
	if m == nil {
		return "", netip.Addr{}, false
	}

	addr, err := netip.ParseAddr(m[2])
	if err != nil {
		return "", netip.Addr{}, false
	}
	return m[1], addr.Unmap(), true
}
