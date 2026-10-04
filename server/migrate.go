package main

import (
	"context"
	_ "embed"
	"fmt"
	"log"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

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
	log.Printf("created database %q", dbName)
	return nil
}

// EnsureSchema applies the schema (idempotently — CREATE ... IF NOT EXISTS) to the target database.
func EnsureSchema(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx, schemaSQL); err != nil {
		return fmt.Errorf("cannot apply schema: %w", err)
	}
	log.Println("database schema OK")
	return nil
}

// EnsureSeed loads development data (seed.sql): test users with a known
// password, their API keys and sample IP reports. Development only.
func EnsureSeed(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx, seedSQL); err != nil {
		return fmt.Errorf("cannot apply seed data: %w", err)
	}
	log.Println("development seed data loaded")
	return nil
}
