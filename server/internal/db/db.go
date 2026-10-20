// Package db provides PostgreSQL and Redis access for the crowdblock server.
package db

import (
	"context"
	_ "embed"
	"fmt"
	"log/slog"
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
