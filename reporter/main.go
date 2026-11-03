// crowdblock-reporter watches logs of attacks on this machine and reports the
// attacking addresses to the crowdblock server. Each log type is handled by a
// plugin (see plugins.go); -list-plugins shows them, -plugins selects them.
//
// Usage:
//
//	CROWDBLOCK_API_KEY=sfw_... crowdblock-reporter -server https://crowdblock.example.org -plugins auth,fail2ban
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/netip"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

func main() {
	configPath := flag.String("config", "/etc/crowdblock-reporter/config.yaml", "path to YAML config file")
	listPlugins := flag.Bool("list-plugins", false, "list available plugins and exit")
	flag.Parse()

	if *listPlugins {
		for _, name := range pluginNames() {
			p := registry[name]
			fmt.Printf("%-10s %s (%s)\n", name, p.Description, p.DefaultLog)
		}
		return
	}

	cfg, err := LoadConfig(*configPath)
	if err != nil {
		log.Fatalf("cannot load config %s: %v", *configPath, err)
	}

	server := flag.String("server", cfg.Server, "server base URL (env CROWDBLOCK_SERVER, config server)")
	pluginList := flag.String("plugins", strings.Join(cfg.Plugins, ","), "comma-separated plugins to enable (config plugins)")
	threshold := flag.Int("threshold", cfg.Threshold, "attempts within -window before an address is reported (config threshold)")
	window := flag.Duration("window", cfg.Window, "time window for -threshold (config window)")
	cooldown := flag.Duration("cooldown", cfg.Cooldown, "minimum time between two reports of the same address (config cooldown)")
	dryRun := flag.Bool("dry-run", cfg.DryRun, "only log what would be reported (config dry_run)")

	logPaths := map[string]*string{}
	for _, name := range pluginNames() {
		p := registry[name]
		defaultLog := p.DefaultLog
		switch name {
		case "auth":
			defaultLog = cfg.AuthLog
		case "fail2ban":
			defaultLog = cfg.Fail2Ban
		}
		logPaths[name] = flag.String(name+"-log", defaultLog, "log file of the "+name+" plugin")
	}
	flag.Parse()

	if *listPlugins {
		for _, name := range pluginNames() {
			p := registry[name]
			fmt.Printf("%-10s %s (%s)\n", name, p.Description, p.DefaultLog)
		}
		return
	}

	plugins, err := selectPlugins(*pluginList)
	if err != nil {
		log.Fatal(err)
	}

	// Not a flag: command lines are visible to every user in `ps`.
	apiKey := cfg.APIKey

	if !*dryRun && (*server == "" || apiKey == "") {
		log.Fatal("-server (or CROWDBLOCK_SERVER) and CROWDBLOCK_API_KEY are required, or use -dry-run")
	}

	if !*dryRun {
		if err := ValidateServerURL(*server); err != nil {
			log.Fatal(err)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	client := NewClient(*server, apiKey)
	tracker := NewTracker(*window, *cooldown)

	// Reports are sent one by one in the background, so a slow server never
	// holds up reading the logs. If it can't keep up, reports are dropped.
	type report struct {
		ip     netip.Addr
		source string
	}
	queue := make(chan report, 1000)
	go func() {
		for r := range queue {
			if err := client.Report(ctx, r.ip, r.source); err != nil {
				log.Printf("report %s failed: %v", r.ip, err)
				// Report again on the next attempt instead of after the cooldown.
				tracker.Forget(r.ip)
				continue
			}
			log.Printf("reported %s (%s)", r.ip, r.source)
		}
	}()

	log.Printf("threshold %d in %s, cooldown %s, dry-run %v", *threshold, *window, *cooldown, *dryRun)

	var wg sync.WaitGroup
	for _, p := range plugins {
		path := *logPaths[p.Name]
		limit := p.Threshold
		if limit == 0 {
			limit = max(*threshold, 1)
		}

		if _, err := os.Stat(path); err != nil {
			log.Printf("%s: %v (waiting for it to appear)", p.Name, err)
		}
		log.Printf("%s: watching %s", p.Name, path)

		wg.Add(1)
		go func() {
			defer wg.Done()

			err := Follow(ctx, path, func(line string) {
				ip, detail, ok := p.Parse(line)
				if !ok || !IsReportable(ip) || !tracker.Hit(ip, time.Now(), limit) {
					return
				}

				if *dryRun {
					log.Printf("would report %s (%s: %s)", ip, p.Name, detail)
					return
				}

				select {
				case queue <- report{ip, p.Name}:
				default:
					log.Printf("queue full, dropping report of %s", ip)
					tracker.Forget(ip)
				}
			})
			if err != nil && ctx.Err() == nil {
				log.Printf("%s: %v", p.Name, err)
			}
		}()
	}
	wg.Wait()
}
