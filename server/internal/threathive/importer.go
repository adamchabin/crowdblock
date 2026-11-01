// Package threathive periodically downloads the Threathive blocklist and
// inserts its IPs into the server's ip_reports table as external reports
// with source "threathive.net".
package threathive

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"server/internal/db"
)

type Config struct {
	// Enabled turns on periodic import.
	Enabled bool

	// URL of the plain-text blocklist (one IP per line).
	URL string

	// Interval between imports.
	Interval time.Duration

	// Source name stored with every imported report.
	Source string

	// ReportWindow is kept for documentation / future use.
	ReportWindow time.Duration
}

// Run loops forever importing the Threathive blocklist every cfg.Interval.
// It returns when ctx is cancelled. When disabled it returns nil immediately.
func Run(ctx context.Context, cfg Config) error {
	if !cfg.Enabled {
		slog.Info("threathive import disabled")
		return nil
	}

	// Ensure the system user and API key exist before the first import.
	apiKeyID, userID, err := db.EnsureThreathiveSystem(ctx)
	if err != nil {
		return fmt.Errorf("ensure threathive system user: %w", err)
	}

	ticker := time.NewTicker(cfg.Interval)
	defer ticker.Stop()

	// Run immediately on startup.
	if err := importOnce(ctx, cfg, apiKeyID, userID); err != nil {
		slog.Warn("threathive initial import failed", "err", err)
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := importOnce(ctx, cfg, apiKeyID, userID); err != nil {
				slog.Warn("threathive import failed", "err", err)
			}
		}
	}
}

// importOnce downloads the blocklist once and inserts every valid public IP.
func importOnce(ctx context.Context, cfg Config, apiKeyID, userID string) error {
	req, err := http.NewRequestWithContext(ctx, "GET", cfg.URL, nil)
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
		if err := db.RecordExternalReport(ctx, line, cfg.Source, apiKeyID, userID); err != nil {
			slog.Debug("threathive: skip invalid IP", "ip", line, "err", err)
			continue
		}
		inserted++
	}
	if err := s.Err(); err != nil {
		return err
	}

	slog.Info("threathive import complete", "scanned", scanned, "inserted", inserted)
	return nil
}
