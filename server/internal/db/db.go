// Package db provides PostgreSQL and Redis access for the crowdblock server.
package db

import (
	"context"
	"crypto/rand"
	_ "embed"
	"fmt"
	"golang.org/x/crypto/bcrypt"
	"log/slog"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

// DB is the global PostgreSQL connection pool.
var DB *pgxpool.Pool

// RDB is the global Redis client.
var RDB *redis.Client

//go:embed schema.sql
var schemaSQL string

//go:embed seed.sql
var seedSQL string

// EnsureDatabase connects to the administrative "postgres" database and creates
// the target database (name taken from DATABASE_URL) if it doesn't already exist.
func EnsureDatabase(ctx context.Context, dsn string) error {
	u, err := url.Parse(dsn)
	if err != nil {
		return fmt.Errorf("invalid DATABASE_URL: %w", err)
	}

	dbName := strings.TrimPrefix(u.Path, "/")
	if dbName == "" {
		return fmt.Errorf("DATABASE_URL does not contain a database name")
	}

	adminURL := *u
	adminURL.Path = "/postgres"

	conn, err := pgx.Connect(ctx, adminURL.String())
	if err != nil {
		return fmt.Errorf("cannot connect to the administrative database: %w", err)
	}
	defer conn.Close(ctx)

	var exists bool
	err = conn.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)`, dbName,
	).Scan(&exists)
	if err != nil {
		return fmt.Errorf("error checking database existence: %w", err)
	}
	if exists {
		return nil
	}

	// the database name can't be a bound parameter in CREATE DATABASE, so we sanitize the identifier
	_, err = conn.Exec(ctx, fmt.Sprintf("CREATE DATABASE %s", pgx.Identifier{dbName}.Sanitize()))
	if err != nil {
		return fmt.Errorf("cannot create database %q: %w", dbName, err)
	}
	slog.Info("database created", "name", dbName)
	return nil
}

// EnsureSchema applies the schema (idempotently — CREATE ... IF NOT EXISTS) to the target database.
func EnsureSchema(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx, schemaSQL); err != nil {
		return fmt.Errorf("cannot apply schema: %w", err)
	}
	slog.Info("database schema OK")
	return nil
}

// EnsureSeed loads development data (seed.sql): test users with a known
// password, their API keys and sample IP reports. Development only.
func EnsureSeed(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx, seedSQL); err != nil {
		return fmt.Errorf("cannot apply seed data: %w", err)
	}
	slog.Info("development seed data loaded")
	return nil
}

// Connect opens a PostgreSQL connection pool and a Redis client.
func Connect(ctx context.Context, dsn, redisAddr string) error {
	var err error
	DB, err = pgxpool.New(ctx, dsn)
	if err != nil {
		return fmt.Errorf("cannot connect to database: %w", err)
	}

	if redisAddr == "" {
		redisAddr = "localhost:6379"
	}
	RDB = redis.NewClient(&redis.Options{Addr: redisAddr})
	if err := RDB.Ping(ctx).Err(); err != nil {
		slog.Warn("redis unavailable, API key auth will hit postgres", "addr", redisAddr, "err", err)
	}
	return nil
}

// Close closes the database pool and the Redis client.
func Close() {
	if DB != nil {
		DB.Close()
	}
	if RDB != nil {
		RDB.Close()
	}
}

// PingRedis pings Redis, used at startup to check the cache.
func PingRedis(ctx context.Context) error {
	return RDB.Ping(ctx).Err()
}

// PingTimeout pings Redis with a timeout, used at startup.
func PingTimeout(timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return RDB.Ping(ctx).Err()
}

// EnsureThreathiveSystem finds or creates the system user and API key used
// by the Threathive importer. It returns (apiKeyID, userID).
func EnsureThreathiveSystem(ctx context.Context) (apiKeyID, userID string, err error) {
	// Find or create the system user.
	err = DB.QueryRow(ctx, `SELECT id FROM users WHERE email = $1`, "threathive@system.local").Scan(&userID)
	if err == pgx.ErrNoRows {
		var hash []byte
		hash, err = bcrypt.GenerateFromPassword([]byte(generateSystemPassword()), bcrypt.DefaultCost)
		if err != nil {
			return "", "", err
		}
		err = DB.QueryRow(ctx,
			`INSERT INTO users (email, password_hash) VALUES ($1, $2) RETURNING id`,
			"threathive@system.local", string(hash),
		).Scan(&userID)
		if err != nil {
			return "", "", err
		}
	} else if err != nil {
		return "", "", err
	}

	// Find or create the API key for the system user.
	err = DB.QueryRow(ctx,
		`SELECT id FROM api_keys WHERE user_id = $1 AND name = $2`,
		userID, "threathive-import",
	).Scan(&apiKeyID)
	if err == pgx.ErrNoRows {
		rawKey, keyHash, keyPrefix, err := GenerateAPIKey()
		if err != nil {
			return "", "", err
		}
		_ = rawKey // not needed externally; used only by the importer internally
		err = DB.QueryRow(ctx,
			`INSERT INTO api_keys (user_id, key_prefix, key_hash, name) VALUES ($1, $2, $3, $4) RETURNING id`,
			userID, keyPrefix, keyHash, "threathive-import",
		).Scan(&apiKeyID)
		if err != nil {
			return "", "", err
		}
	} else if err != nil {
		return "", "", err
	}

	return apiKeyID, userID, nil
}

// generateSystemPassword returns a random password used only for the system
// user (it is never meant to be used for login).
func generateSystemPassword() string {
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	buf := make([]byte, 32)
	for i := range buf {
		var n int
		if _, err := rand.Read(buf[i : i+1]); err != nil {
			n = int(buf[i]) % len(alphabet) // fallback: deterministic-ish
		} else {
			n = int(buf[0]) % len(alphabet)
		}
		buf[i] = alphabet[n]
	}
	return string(buf)
}

// RecordExternalReport inserts a report from an external source (e.g.
// Threathive) into ip_reports. It validates the IP is public and not CIDR.
func RecordExternalReport(ctx context.Context, ip, source, apiKeyID, userID string) error {
	addr, err := netip.ParseAddr(ip)
	if err != nil || !IsPublicIP(addr) {
		return fmt.Errorf("not a public IP: %s", ip)
	}
	_, err = DB.Exec(ctx,
		`INSERT INTO ip_reports (ip, api_key_id, user_id, source) VALUES ($1, $2, $3, NULLIF($4, ''))`,
		addr.Unmap().String(), apiKeyID, userID, source,
	)
	return err
}
