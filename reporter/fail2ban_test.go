package main

import "testing"

func TestParseFail2banLine(t *testing.T) {
	for line, want := range map[string]string{
		"2026-10-03 21:00:01,234 fail2ban.actions        [812]: NOTICE  [sshd] Ban 61.177.172.10":           "sshd 61.177.172.10",
		"2026-10-03 21:00:01,234 fail2ban.actions        [812]: NOTICE  [nginx-botsearch] Ban 2001:4860::1": "nginx-botsearch 2001:4860::1",
		"2026-10-03 21:00:01,234 fail2ban.actions        [812]: NOTICE  [recidive] Ban 45.148.10.5":         "recidive 45.148.10.5",
		"2026-10-03 21:00:01,234 fail2ban.actions [812]: NOTICE [sshd] Ban 45.148.10.6":                     "sshd 45.148.10.6",
		// not fresh bans
		"2026-10-03 21:00:01,234 fail2ban.actions        [812]: NOTICE  [sshd] Restore Ban 61.177.172.10":    "",
		"2026-10-03 21:00:01,234 fail2ban.actions        [812]: NOTICE  [sshd] Unban 61.177.172.10":          "",
		"2026-10-03 21:00:01,234 fail2ban.filter         [812]: INFO    [sshd] Found 61.177.172.10 - 2026":   "",
		"2026-10-03 21:00:01,234 fail2ban.actions        [812]: WARNING [sshd] 61.177.172.10 already banned": "",
		"2026-10-03 21:00:01,234 fail2ban.actions        [812]: NOTICE  [sshd] Ban not-an-ip":                "",
	} {
		jail, ip, ok := ParseFail2banLine(line)
		got := ""
		if ok {
			got = jail + " " + ip.String()
		}
		if got != want {
			t.Errorf("ParseFail2banLine(%q) = %q, want %q", line, got, want)
		}
	}
}
