package main

import (
	"math/rand/v2"
	"testing"

	"server/internal/db"
)

func TestRandomPublicIPs(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	ips := randomPublicIPs(5000, rng)
	if len(ips) != 5000 {
		t.Fatalf("got %d addresses, want 5000", len(ips))
	}
	seen := map[string]bool{}
	for _, a := range ips {
		if !db.IsPublicIP(a) {
			t.Fatalf("%s is not accepted by the server rule", a)
		}
		if seen[a.String()] {
			t.Fatalf("%s is duplicated", a)
		}
		seen[a.String()] = true
	}
}

func TestRandomPublicIPsSeeded(t *testing.T) {
	a := randomPublicIPs(100, rand.New(rand.NewPCG(7, 7)))
	b := randomPublicIPs(100, rand.New(rand.NewPCG(7, 7)))
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("same seed gave different pools at %d", i)
		}
	}
}
