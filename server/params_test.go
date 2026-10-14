package main

import (
	"fmt"
	"net/netip"
	"net/url"
	"testing"
)

func TestParseListParams(t *testing.T) {
	for query, want := range map[string]string{
		"min_reporters=5&minutes=360":    "ok 5 360",
		"min_reporters=1&minutes=60":     "ok 1 60",
		"min_reporters=50&minutes=10080": "ok 50 10080",
		"min_reporters=3&minutes=360":    "invalid data (min_reporters must be one of [1 5 10 20 50])",
		"min_reporters=5&minutes=30":     "invalid data (minutes must be one of [60 360 1440 10080])",
		"min_reporters=x&minutes=360":    "invalid data (min_reporters must be one of [1 5 10 20 50])",
		"minutes=360":                    "invalid data (min_reporters must be one of [1 5 10 20 50])",
	} {
		q, _ := url.ParseQuery(query)
		r, m, err := parseListParams(q)
		got := ""
		if err != nil {
			got = err.Error()
		} else {
			got = fmt.Sprintf("ok %d %d", r, m)
		}
		if got != want {
			t.Errorf("parseListParams(%q) = %q, want %q", query, got, want)
		}
	}
}

func TestIsPublicIP(t *testing.T) {
	for ip, want := range map[string]bool{
		"203.0.113.10": true, "8.8.8.8": true, "2001:4860:4860::8888": true,
		"10.0.0.1": false, "192.168.1.1": false, "127.0.0.1": false, "0.0.0.0": false,
		"169.254.1.1": false, "224.0.0.1": false, "::1": false, "fe80::1": false, "fd00::1": false,
	} {
		if got := isPublicIP(netip.MustParseAddr(ip)); got != want {
			t.Errorf("isPublicIP(%s) = %v, want %v", ip, got, want)
		}
	}
}

func TestParseEmail(t *testing.T) {
	for in, want := range map[string]bool{
		"user@example.com": true, " user@example.com ": true, "a.b+c@example.co.uk": true,
		"": false, "user": false, "Name <user@example.com>": false,
		"a@example.com\r\nBcc: x@example.com": false, "a@b.c, d@e.f": false,
	} {
		if _, ok := parseEmail(in); ok != want {
			t.Errorf("parseEmail(%q) = %v, want %v", in, ok, want)
		}
	}
}

func TestGeneratePassword(t *testing.T) {
	a, _ := generatePassword()
	b, _ := generatePassword()
	if len(a) != 16 || a == b {
		t.Errorf("bad passwords %q %q", a, b)
	}
}
