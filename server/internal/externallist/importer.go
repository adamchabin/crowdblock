// Package externallist periodically downloads external blocklists and
// inserts their IPs into the server's ip_reports table as external reports.
// Each list is configured independently (URL, interval, source name) and can
// be enabled or disabled separately.
package externallist

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"server/config"
	"server/internal/db"
)

// Run loops forever importing every enabled external list every list.Interval.
// It returns when ctx is cancelled. When no list is enabled it returns nil
// immediately.
func Run(ctx context.Context, lists []config.ExternalList) error {
	enabled := 0
	for i := range lists {
		if lists[i].Enabled {
			enabled++
			l := lists[i] // copy for the closure
			interval, err := time.ParseDuration(l.Interval)
			if err != nil {
				return fmt.Errorf("invalid interval for list %q: %w", l.Source, err)
			}
			go func() {
				if err := runList(ctx, l, interval); err != nil && err != context.Canceled {
					slog.Warn("external list importer stopped", "source", l.Source, "err", err)
				}
			}()
		}
	}
	if enabled == 0 {
		slog.Info("no external lists enabled")
	}
	return nil
}

// runList imports a single external list on its own ticker.
func runList(ctx context.Context, l config.ExternalList, interval time.Duration) error {
	// Ensure the system user and API key exist before the first import.
	apiKeyID, userID, err := db.EnsureExternalSystem(ctx, l.Source)
	if err != nil {
		return fmt.Errorf("ensure external system user: %w", err)
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Run immediately on startup.
	if err := importOnce(ctx, l, apiKeyID, userID); err != nil {
		slog.Warn("external list initial import failed", "source", l.Source, "err", err)
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := importOnce(ctx, l, apiKeyID, userID); err != nil {
				slog.Warn("external list import failed", "source", l.Source, "err", err)
			}
		}
	}
}

// importOnce downloads the blocklist once and inserts every valid public IP.
func importOnce(ctx context.Context, l config.ExternalList, apiKeyID, userID string) error {
	req, err := http.NewRequestWithContext(ctx, "GET", l.URL, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status: %s", resp.Status)
	}

	scanned := 0
	inserted := 0
	s := bufio.NewScanner(resp.Body)
	s.Buffer(make([]byte, 1024*1024), 1024*1024)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		scanned++
		if err := db.RecordExternalReport(ctx, line, l.Source, apiKeyID, userID); err != nil {
			slog.Debug("external list: skip invalid IP", "source", l.Source, "ip", line, "err", err)
			continue
		}
		inserted++
	}
	if err := s.Err(); err != nil {
		return err
	}

	slog.Info("external list import complete", "source", l.Source, "scanned", scanned, "inserted", inserted)
	return nil
}
