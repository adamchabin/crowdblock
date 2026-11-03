package main

import (
	"net/netip"
	"sync"
	"time"
)

// Tracker decides when an address gets reported: after `threshold` failed
// attempts within `window` (one mistyped password is not an attack), and then
// at most once per `cooldown` (the server counts distinct reporters, repeated
// reports from us add nothing). The threshold depends on the source, the
// cooldown is shared: an attack seen in several logs is reported once.
type Tracker struct {
	window   time.Duration
	cooldown time.Duration

	mu       sync.Mutex
	attempts map[netip.Addr][]time.Time // within window, oldest first
	reported map[netip.Addr]time.Time
	lastGC   time.Time
}

func NewTracker(window, cooldown time.Duration) *Tracker {
	return &Tracker{
		window:   window,
		cooldown: cooldown,
		attempts: map[netip.Addr][]time.Time{},
		reported: map[netip.Addr]time.Time{},
	}
}

// Hit records an attempt and returns true when the address should be
// reported now, i.e. it reached `threshold` attempts within the window.
func (t *Tracker) Hit(ip netip.Addr, now time.Time, threshold int) bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.gc(now)

	if at, ok := t.reported[ip]; ok && now.Sub(at) < t.cooldown {
		return false
	}

	times := append(recent(t.attempts[ip], now, t.window), now)
	if len(times) < threshold {
		t.attempts[ip] = times
		return false
	}

	delete(t.attempts, ip)
	t.reported[ip] = now
	return true
}

// Forget undoes a report decision (the report could not be sent).
func (t *Tracker) Forget(ip netip.Addr) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.reported, ip)
}

// gc drops stale entries now and then, so a scan from many addresses
// doesn't grow the maps forever.
func (t *Tracker) gc(now time.Time) {
	if now.Sub(t.lastGC) < time.Minute {
		return
	}
	t.lastGC = now

	for ip, times := range t.attempts {
		if times = recent(times, now, t.window); len(times) == 0 {
			delete(t.attempts, ip)
		} else {
			t.attempts[ip] = times
		}
	}
	for ip, at := range t.reported {
		if now.Sub(at) >= t.cooldown {
			delete(t.reported, ip)
		}
	}
}

func recent(times []time.Time, now time.Time, window time.Duration) []time.Time {
	i := 0
	for i < len(times) && now.Sub(times[i]) >= window {
		i++
	}
	return times[i:]
}
