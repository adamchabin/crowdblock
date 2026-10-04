package main

import "testing"

func TestParseAuthLine(t *testing.T) {
	for line, want := range map[string]string{
		// attacks
		"Oct  3 21:00:01 srv sshd[1234]: Failed password for root from 61.177.172.10 port 52114 ssh2":                                              "61.177.172.10",
		"Oct  3 21:00:01 srv sshd[1234]: Failed password for invalid user admin from 45.148.10.5 port 40512 ssh2":                                  "45.148.10.5",
		"Oct  3 21:00:01 srv sshd[1234]: Failed publickey for git from 2001:4860::8888 port 40512 ssh2: RSA SHA256:abc":                            "2001:4860::8888",
		"Oct  3 21:00:01 srv sshd[1234]: Invalid user oracle from 103.20.1.7 port 33221":                                                           "103.20.1.7",
		"Oct  3 21:00:01 srv sshd[1234]: error: maximum authentication attempts exceeded for root from 92.118.39.1 port 22 ssh2 [preauth]":         "92.118.39.1",
		"Oct  3 21:00:01 srv sshd[1234]: Disconnecting authenticating user root 92.118.39.2 port 6000: Too many authentication failures [preauth]": "92.118.39.2",
		"Oct  3 21:00:01 srv sshd[1234]: Did not receive identification string from 185.1.2.3 port 61000":                                          "185.1.2.3",
		"Oct  3 21:00:01 srv sshd[1234]: banner exchange: Connection from 185.1.2.4 port 61000: invalid format":                                    "185.1.2.4",
		"Oct  3 21:00:01 srv sshd[1234]: Unable to negotiate with 185.1.2.5 port 61000: no matching key exchange method found.":                    "185.1.2.5",
		"2026-10-03T21:00:01.123456+02:00 srv sshd[1234]: Failed password for root from 61.177.172.11 port 52114 ssh2":                             "61.177.172.11",
		"Oct  3 21:00:01 srv sshd[1234]: Failed password for root from ::ffff:61.177.172.12 port 52114 ssh2":                                       "61.177.172.12",
		// not attacks
		"Oct  3 21:00:01 srv sshd[1234]: Accepted publickey for adam from 89.1.2.3 port 50000 ssh2: ED25519 SHA256:abc": "",
		"Oct  3 21:00:01 srv sshd[1234]: Disconnected from user adam 89.1.2.3 port 50000":                               "",
		"Oct  3 21:00:01 srv sshd[1234]: Connection closed by 89.1.2.3 port 50000 [preauth]":                            "",
		"Oct  3 21:00:01 srv sudo:     adam : TTY=pts/0 ; PWD=/home/adam ; USER=root ; COMMAND=/usr/bin/ls":             "",
		"Oct  3 21:00:01 srv CRON[99]: pam_unix(cron:session): session opened for user root(uid=0)":                     "",
		// user names are attacker-controlled: the real address is the last one
		"Oct  3 21:00:01 srv sshd[1234]: Invalid user x from 8.8.8.8 port 1 from 103.20.1.8 port 33221":                          "103.20.1.8",
		"Oct  3 21:00:01 srv sshd[1234]: Failed password for invalid user x from 8.8.8.8 port 1 from 103.20.1.9 port 33221 ssh2": "103.20.1.9",
		"Oct  3 21:00:01 srv sshd-session[1234]: Failed password for root from 61.177.172.13 port 52114 ssh2":                    "61.177.172.13",
		// not sshd, even with a matching text
		"Oct  3 21:00:01 srv myapp[1]: Failed password for root from 1.1.1.1 port 22": "",
	} {
		ip, ok := ParseAuthLine(line)
		got := ""
		if ok {
			got = ip.String()
		}
		if got != want {
			t.Errorf("ParseAuthLine(%q) = %q, want %q", line, got, want)
		}
	}
}

func TestIsReportable(t *testing.T) {
	for ip, want := range map[string]bool{
		"61.177.172.10":   true,
		"2001:4860::8888": true,
		"10.0.0.1":        false,
		"192.168.1.5":     false,
		"172.16.0.1":      false,
		"127.0.0.1":       false,
		"::1":             false,
		"fe80::1":         false,
		"fd00::1":         false,
		"224.0.0.1":       false,
		"100.64.1.1":      false,
		"203.0.113.5":     false,
		"2001:db8::5":     false,
	} {
		ip, ok := ParseAuthLine("sshd[1]: Invalid user x from " + ip + " port 1")
		if !ok {
			t.Fatalf("test line for %s did not parse", ip)
		}
		if got := IsReportable(ip); got != want {
			t.Errorf("IsReportable(%s) = %v, want %v", ip, got, want)
		}
	}
}
