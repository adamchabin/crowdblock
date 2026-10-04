package main

import (
	"fmt"
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
