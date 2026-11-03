package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestTracker(t *testing.T) {
	tr := NewTracker(10*time.Minute, time.Hour)
	ip := netip.MustParseAddr("61.177.172.10")
	t0 := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)

	steps := []struct {
		at   time.Duration
		want bool
	}{
		{0, false},
		{2 * time.Minute, false},
		{11 * time.Minute, false},               // first attempt fell out of the window
		{11*time.Minute + 30*time.Second, true}, // 3 attempts within 10 minutes
		{13 * time.Minute, false},               // cooldown
		{72 * time.Minute, false},               // cooldown over, but the count starts again
		{73 * time.Minute, false},
		{74 * time.Minute, true},
	}
	for _, s := range steps {
		if got := tr.Hit(ip, t0.Add(s.at), 3); got != s.want {
			t.Errorf("Hit at +%s = %v, want %v", s.at, got, s.want)
		}
	}

	// A failed report is retried on the next attempt.
	tr = NewTracker(time.Minute, time.Hour)
	if !tr.Hit(ip, t0, 1) || tr.Hit(ip, t0.Add(time.Second), 1) {
		t.Fatal("threshold 1: expected report, then cooldown")
	}
	tr.Forget(ip)
	if !tr.Hit(ip, t0.Add(2*time.Second), 1) {
		t.Error("after Forget the address should be reported again")
	}

	// The cooldown is shared between sources: a fail2ban ban (threshold 1)
	// right after the auth.log report is not sent again.
	tr = NewTracker(10*time.Minute, time.Hour)
	tr.Hit(ip, t0, 3)
	tr.Hit(ip, t0.Add(time.Second), 3)
	if !tr.Hit(ip, t0.Add(2*time.Second), 3) {
		t.Fatal("auth.log: expected report after 3 attempts")
	}
	if tr.Hit(ip, t0.Add(3*time.Second), 1) {
		t.Error("fail2ban ban within the cooldown should not be reported")
	}
}

func TestFollow(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.log")
	write := func(flag int, s string) {
		f, err := os.OpenFile(path, flag|os.O_WRONLY, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		f.WriteString(s)
		f.Close()
	}

	write(os.O_CREATE, "old line, must be skipped\n")

	var mu sync.Mutex
	var lines []string
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		Follow(ctx, path, func(l string) {
			mu.Lock()
			lines = append(lines, l)
			mu.Unlock()
		})
		close(done)
	}()

	waitFor := func(n int) {
		t.Helper()
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
			mu.Lock()
			got := len(lines)
			mu.Unlock()
			if got >= n {
				return
			}
		}
		t.Fatalf("timeout waiting for %d lines, got %q", n, lines)
	}

	time.Sleep(200 * time.Millisecond) // let Follow open the file
	write(os.O_APPEND, "one\ntw")
	time.Sleep(300 * time.Millisecond)
	write(os.O_APPEND, "o\n") // a line written in two parts
	waitFor(2)

	// logrotate (create): rename, then a new file. A line written to the old
	// file right before the rename must not be lost.
	write(os.O_APPEND, "three\n")
	os.Rename(path, path+".1")
	write(os.O_CREATE, "four\n")
	waitFor(4)

	// copytruncate
	os.Truncate(path, 0)
	time.Sleep(1500 * time.Millisecond)
	write(os.O_APPEND, "five\n")
	waitFor(5)

	cancel()
	<-done

	want := []string{"one", "two", "three", "four", "five"}
	if len(lines) != len(want) {
		t.Fatalf("lines = %q, want %q", lines, want)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Fatalf("lines = %q, want %q", lines, want)
		}
	}
}

func TestClientReport(t *testing.T) {
	var got struct {
		path, key, ip, source string
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct{ IP, Source string }
		json.NewDecoder(r.Body).Decode(&body)
		got.path, got.key, got.ip, got.source = r.URL.Path, r.Header.Get("X-API-Key"), body.IP, body.Source

		if got.key != "sfw_test" {
			http.Error(w, "invalid API key", http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	ip := netip.MustParseAddr("2001:4860::8888")
	if err := NewClient(srv.URL+"/", "sfw_test").Report(context.Background(), ip, "fail2ban"); err != nil {
		t.Fatal(err)
	}
	if got.path != "/api/v1/reports" || got.ip != "2001:4860::8888" || got.source != "fail2ban" {
		t.Errorf("request = %+v", got)
	}

	err := NewClient(srv.URL, "wrong").Report(context.Background(), ip, "auth")
	if err == nil || err.Error() != "server returned 401 Unauthorized: invalid API key" {
		t.Errorf("bad key: err = %v", err)
	}
}

func TestSelectPlugins(t *testing.T) {
	for list, want := range map[string]string{
		"auth":                "auth",
		"fail2ban, auth":      "fail2ban auth",
		"auth,auth,,fail2ban": "auth fail2ban",
		"auth,nope":           `error: unknown plugin "nope" (available: auth, fail2ban)`,
		"":                    "error: no plugins selected (available: auth, fail2ban)",
	} {
		ps, err := selectPlugins(list)
		var got []string
		for _, p := range ps {
			got = append(got, p.Name)
		}
		s := strings.Join(got, " ")
		if err != nil {
			s = "error: " + err.Error()
		}
		if s != want {
			t.Errorf("selectPlugins(%q) = %q, want %q", list, s, want)
		}
	}
}

func TestValidateServerURL(t *testing.T) {
	for server, ok := range map[string]bool{
		"http://100.64.0.2:8080":     true,
		"https://crowdblock.example": true,
		"http://[2001:db8::1]:8080/": true,
		"100.64.0.2:8000":            false,
		"crowdblock.example":         false,
		"ftp://crowdblock.example":   false,
		"http://":                    false,
	} {
		if err := ValidateServerURL(server); (err == nil) != ok {
			t.Errorf("ValidateServerURL(%q) = %v, want ok=%v", server, err, ok)
		}
	}
}
