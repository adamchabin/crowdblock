// crowdblock-server is the collector server of the crowdblock crowd-sourced
// blocklist: it keeps the precomputed IP lists in Redis, serves them to the
// routers and collects the reports of the reporters.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"time"

	"server/config"
	"server/internal/api"
	"server/internal/db"
	"server/internal/externallist"
	"server/internal/geoip"
	"server/internal/list"
	"server/internal/logging"

	"golang.org/x/sync/errgroup"
)

// Set at build time: go build -ldflags "-X main.version=v1.2.3"
var version = "dev"

// setupLogging configures slog; DEBUG=true enables debug level.
func setupLogging() {
	level := slog.LevelInfo
	if logging.Debug {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))
}

func main() {
	setupLogging()
	slog.Info("starting server", "version", version)

	cfgPath := os.Getenv("CONFIG_PATH")
	if cfgPath == "" {
		cfgPath = "config.yaml"
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		slog.Error("cannot load config", "path", cfgPath, "err", err)
		os.Exit(1)
	}
	api.SetConfig(cfg)

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		slog.Error("DATABASE_URL not set")
		os.Exit(1)
	}

	ctx := context.Background()

	if err := db.EnsureDatabase(ctx, dsn); err != nil {
		slog.Error("cannot prepare database", "err", err)
		os.Exit(1)
	}

	if err := db.Connect(ctx, dsn, os.Getenv("REDIS_ADDR")); err != nil {
		slog.Error("cannot connect to database", "err", err)
		os.Exit(1)
	}
	defer db.Close()

	if err := db.EnsureSchema(ctx, db.DB); err != nil {
		slog.Error("cannot apply schema", "err", err)
		os.Exit(1)
	}

	if os.Getenv("SEED_DEV_DATA") == "1" {
		if err := db.EnsureSeed(ctx, db.DB); err != nil {
			slog.Error("cannot load seed data", "err", err)
			os.Exit(1)
		}
	}

	dbPath := os.Getenv("GEOIP_DB")
	if dbPath == "" {
		dbPath = "geoip/dbip-country-lite.mmdb"
	}
	if err := geoip.OpenGeoIP(dbPath); err != nil {
		slog.Error("cannot open GeoIP database", "err", err)
		os.Exit(1)
	}

	if missing := api.SMTPMissing(); len(missing) > 0 {
		slog.Warn("registration by e-mail disabled", "missing", strings.Join(missing, ", "))
	} else {
		slog.Info("registration by e-mail enabled", "host", os.Getenv("SMTP_HOST"))
	}

	// Startup timeout for initialization
	if err := db.PingTimeout(15 * time.Second); err != nil {
		slog.Warn("redis unavailable, API key auth will hit postgres", "addr", os.Getenv("REDIS_ADDR"), "err", err)
	}

	// Main context with signal handling
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	g, ctx := errgroup.WithContext(ctx)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", api.HandleUI)
	mux.HandleFunc("GET /api/v1/stats", api.HandleStats)
	mux.HandleFunc("GET /api/v1/health", api.HandleHealth)
	// Rate-limit endpoints that are expensive or abuse-prone, per remote IP.
	// Redis-backed counters are shared across instances.
	mux.Handle("POST /api/v1/register", api.RateLimit("register", 5, time.Hour, http.HandlerFunc(api.HandleRegister)))
	mux.Handle("POST /api/v1/api-keys", api.RateLimit("api-keys", 10, time.Hour, http.HandlerFunc(api.HandleCreateAPIKey)))
	mux.HandleFunc("GET /api/v1/api-keys", api.HandleListAPIKeys)
	mux.HandleFunc("DELETE /api/v1/api-keys/{id}", api.HandleRevokeAPIKey)
	mux.HandleFunc("DELETE /api/v1/account", api.HandleDeleteAccount)
	mux.Handle("POST /api/v1/reports", api.RateLimit("reports", 100, time.Hour, http.HandlerFunc(api.HandleReportIP)))
	mux.Handle("GET /api/v1/ips", api.RateLimit("ips", 1000, time.Hour, http.HandlerFunc(api.HandleListIPs)))
	// geoip is unauthenticated, so it's the one endpoint group with no built-in
	// cost (no API key, no per-user limit): rate-limit it per remote address.
	mux.Handle("GET /api/v1/geoip/{ip}", api.RateLimit("geoip", 120, time.Minute, http.HandlerFunc(api.HandleGeoIP)))
	mux.Handle("POST /api/v1/geoip/batch", api.RateLimit("geoip-batch", 30, time.Minute, http.HandlerFunc(api.HandleGeoIPBatch)))
	mux.HandleFunc("GET /api/v1/world", api.HandleWorld)

	// Admin panel routes (require admin authentication)
	adminMux := http.NewServeMux()
	adminMux.HandleFunc("GET /", api.HandleAdminUI)
	adminMux.HandleFunc("GET /api/users", api.AdminListUsers)
	adminMux.HandleFunc("GET /api/users/{id}", api.AdminGetUser)
	adminMux.HandleFunc("DELETE /api/users/{id}", api.AdminDeleteUser)
	adminMux.HandleFunc("PATCH /api/users/{id}/admin", api.AdminToggleAdmin)
	adminMux.HandleFunc("GET /api/api-keys", api.AdminListAPIKeys)
	adminMux.HandleFunc("DELETE /api/api-keys/{id}", api.AdminDeleteAPIKey)
	adminMux.HandleFunc("PATCH /api/api-keys/{id}/revoke", api.AdminRevokeAPIKey)
	adminMux.HandleFunc("GET /api/reports", api.AdminListReports)
	adminMux.HandleFunc("DELETE /api/reports/{id}", api.AdminDeleteReport)

	mux.Handle("GET /admin/", api.RequireAdmin(adminMux))
	mux.Handle("DELETE /admin/", api.RequireAdmin(adminMux))
	mux.Handle("PATCH /admin/", api.RequireAdmin(adminMux))

	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		addr = ":8080"
	}

	// List generator
	g.Go(func() error {
		return list.RunListGenerator(ctx, list.RefreshInterval())
	})

	// Report pruner: ip_reports is append-only otherwise, growing forever even
	// though nothing past the largest list window is ever read again.
	g.Go(func() error {
		maxWindow := time.Duration(slices.Max(list.AllowedMinutes)) * time.Minute
		retain := maxWindow + time.Hour // safety margin over the largest window
		return db.RunReportPruner(ctx, retain, time.Hour)
	})

	// External list importers: periodically download each enabled external
	// blocklist and insert its IPs into ip_reports as external reports.
	// Each list runs on its own ticker with its own interval.
	for i := range cfg.ExternalLists {
		l := cfg.ExternalLists[i] // copy for the closure
		if !l.Enabled {
			continue
		}
		g.Go(func() error {
			return externallist.Run(ctx, []config.ExternalList{l})
		})
	}

	// HTTP server: shut down when the signal context ends, otherwise
	// g.Wait() below would block forever on SIGINT/SIGTERM.
	g.Go(func() error {
		srv := &http.Server{
			Addr:              addr,
			Handler:           api.RecoveryMiddleware(api.TimeoutMiddleware(api.LogRequests(mux))),
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       15 * time.Second,
			WriteTimeout:      30 * time.Second,
			IdleTimeout:       60 * time.Second,
		}
		go func() {
			<-ctx.Done()
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = srv.Shutdown(shutdownCtx)
		}()
		slog.Info("server listening", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	})

	// Wait for shutdown
	if err := g.Wait(); err != nil {
		slog.Error("shutdown error", "err", err)
	}
}
