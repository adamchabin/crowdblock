// Command loadgen sends IP reports to POST /api/v1/reports at a constant rate.
// It builds a pool of unique random public IPv4 addresses once, then every
// request reports a randomly picked address from that pool.
//
// For load tests against a TEST instance only: the reports are stored, count
// in the lists until they age out (7 days at most), and use the API key you
// give. Use a dedicated test account and delete it afterwards.
//
//	CROWD_BLOCK_API_KEY=sfw_... CROWD_BLOCK_BASE_URL=http://host:8080 \
//	    go run ./cmd/loadgen -ips 100000 -rate 50 -duration 10m
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"server/internal/db"
)

type config struct {
	baseURL     string
	apiKey      string
	ips         int
	rate        float64
	duration    time.Duration
	maxInFlight int
	source      string
	reportEvery time.Duration
	seed        uint64
}

// counters are the results of one reporting interval (or of the whole run).
type counters struct {
	sent      int
	skipped   int // ticks dropped because maxInFlight requests were running
	transport int // requests that failed without a response
	codes     map[int]int
	latencies []time.Duration
}

// meter collects counters from the request goroutines and the ticker.
type meter struct {
	mu  sync.Mutex
	c   counters
	all counters // totals for the whole run
}

func newMeter() *meter {
	return &meter{c: counters{codes: map[int]int{}}}
}

func (m *meter) skip() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.c.skipped++
	m.all.skipped++
}

func (m *meter) record(code int, lat time.Duration, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.c.sent++
	m.all.sent++
	if err != nil {
		m.c.transport++
		m.all.transport++
		return
	}
	m.c.codes[code]++
	m.all.codes[code]++
	m.c.latencies = append(m.c.latencies, lat)
	m.all.latencies = append(m.all.latencies, lat)
}

// totals returns the counters of the whole run.
func (m *meter) totals() counters {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.all
}

// take returns the counters collected so far and starts a new interval.
func (m *meter) take() counters {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := m.c
	m.c = counters{codes: map[int]int{}}
	return out
}

// randomPublicIPs returns n distinct public IPv4 addresses, using the same
// rule as the server (db.IsPublicIP), so every report is accepted.
func randomPublicIPs(n int, rng *rand.Rand) []netip.Addr {
	seen := make(map[netip.Addr]struct{}, n)
	ips := make([]netip.Addr, 0, n)
	for len(ips) < n {
		v := rng.Uint32()
		a := netip.AddrFrom4([4]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)})
		if !db.IsPublicIP(a) {
			continue
		}
		if _, dup := seen[a]; dup {
			continue
		}
		seen[a] = struct{}{}
		ips = append(ips, a)
	}
	return ips
}

// envOr returns the value of the environment variable, or def when it is unset.
func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

func main() {
	var cfg config
	flag.StringVar(&cfg.baseURL, "url", envOr("CROWD_BLOCK_BASE_URL", "http://localhost:8080"),
		"server base URL (default from CROWD_BLOCK_BASE_URL)")
	flag.IntVar(&cfg.ips, "ips", 100000, "size of the pool of unique IPs")
	flag.Float64Var(&cfg.rate, "rate", 50, "reports per second")
	flag.DurationVar(&cfg.duration, "duration", 0, "how long to run (0 = until Ctrl-C)")
	flag.IntVar(&cfg.maxInFlight, "inflight", 200, "maximum concurrent requests; excess ticks are counted as skipped")
	flag.StringVar(&cfg.source, "source", "loadgen", "source sent with each report (a-z, 0-9, _ and -, max 32)")
	flag.DurationVar(&cfg.reportEvery, "report", 5*time.Second, "how often to print statistics")
	flag.Uint64Var(&cfg.seed, "seed", 0, "random seed (0 = time based)")
	flag.Parse()

	cfg.apiKey = os.Getenv("CROWD_BLOCK_API_KEY")
	if cfg.apiKey == "" {
		log.Fatal("set CROWD_BLOCK_API_KEY to the API key of a test account")
	}
	if cfg.ips < 1 || cfg.rate <= 0 || cfg.maxInFlight < 1 || cfg.reportEvery <= 0 {
		log.Fatal("-ips, -rate, -inflight and -report must be positive")
	}
	if !db.SourcePattern.MatchString(cfg.source) {
		log.Fatal("invalid -source (a-z, 0-9, _ and -, max 32 characters)")
	}
	if err := run(cfg); err != nil {
		log.Fatal(err)
	}
}

func run(cfg config) error {
	seed := cfg.seed
	if seed == 0 {
		seed = uint64(time.Now().UnixNano())
	}
	rng := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))

	began := time.Now()
	pool := randomPublicIPs(cfg.ips, rng)
	log.Printf("pool of %d unique public IPs built in %s (seed %d)", len(pool), time.Since(began).Round(time.Millisecond), seed)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if cfg.duration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, cfg.duration)
		defer cancel()
	}

	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        cfg.maxInFlight,
			MaxIdleConnsPerHost: cfg.maxInFlight,
		},
	}
	url := strings.TrimRight(cfg.baseURL, "/") + "/api/v1/reports"

	m := newMeter()
	m.all.codes = map[int]int{}
	var wg sync.WaitGroup
	sem := make(chan struct{}, cfg.maxInFlight)

	// Statistics: one line every reportEvery.
	reportDone := make(chan struct{})
	go func() {
		defer close(reportDone)
		t := time.NewTicker(cfg.reportEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				printInterval(m.take(), cfg.reportEvery)
			}
		}
	}()

	ticker := time.NewTicker(time.Duration(float64(time.Second) / cfg.rate))
	defer ticker.Stop()
	began = time.Now()

	for done := false; !done; {
		select {
		case <-ctx.Done():
			done = true
		case <-ticker.C:
			select {
			case sem <- struct{}{}:
			default:
				m.skip()
				continue
			}
			ip := pool[rng.IntN(len(pool))]
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				code, lat, err := send(client, url, cfg.apiKey, ip, cfg.source)
				m.record(code, lat, err)
			}()
		}
	}

	// Let the requests in flight finish, then print the totals.
	wg.Wait()
	<-reportDone
	elapsed := time.Since(began).Seconds()
	total := m.totals()

	fmt.Println()
	log.Printf("done: %d requests in %.0fs (%.1f/s), %d ticks skipped", total.sent, elapsed,
		float64(total.sent)/elapsed, total.skipped)
	printInterval(total, 0)
	return nil
}

// send posts one report and returns the status code and latency.
func send(client *http.Client, url, apiKey string, ip netip.Addr, source string) (int, time.Duration, error) {
	body, err := json.Marshal(map[string]string{"ip": ip.String(), "source": source})
	if err != nil {
		return 0, 0, err
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", apiKey)

	began := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return 0, time.Since(began), err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp.StatusCode, time.Since(began), nil
}

// printInterval prints one line of statistics. every is the interval length
// (0 for the final summary, which has no rate).
func printInterval(c counters, every time.Duration) {
	codes := make([]int, 0, len(c.codes))
	for code := range c.codes {
		codes = append(codes, code)
	}
	slices.Sort(codes)
	var parts []string
	for _, code := range codes {
		parts = append(parts, fmt.Sprintf("%d=%d", code, c.codes[code]))
	}
	if c.transport > 0 {
		parts = append(parts, fmt.Sprintf("errors=%d", c.transport))
	}
	if len(parts) == 0 {
		parts = append(parts, "none")
	}

	rate := ""
	if every > 0 {
		rate = fmt.Sprintf(" rate=%.1f/s", float64(c.sent)/every.Seconds())
	}
	lat := ""
	if len(c.latencies) > 0 {
		l := slices.Clone(c.latencies)
		slices.Sort(l)
		lat = fmt.Sprintf(" p50=%s p95=%s max=%s",
			l[len(l)/2].Round(time.Millisecond),
			l[len(l)*95/100].Round(time.Millisecond),
			l[len(l)-1].Round(time.Millisecond))
	}
	log.Printf("sent=%d%s skipped=%d status[%s]%s", c.sent, rate, c.skipped, strings.Join(parts, " "), lat)
}
