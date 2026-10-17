// Package storage owns the Postgres connection pool and schema migrations for tenant_db.
package storage

import (
	"context"
	"embed"
	"fmt"
	"io/fs"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Config describes how to reach tenant_db.
type Config struct {
	URL string
	// Schema overrides search_path; empty means the default (public).
	Schema string
}

// Open creates a lazily connecting pool; it does not fail when the database is down.
func Open(ctx context.Context, cfg Config) (*pgxpool.Pool, error) {
	pc, err := pgxpool.ParseConfig(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	// Small pool keeps Postgres usable on an 8 GB laptop
	pc.MaxConns = 5
	if cfg.Schema != "" {
		pc.ConnConfig.RuntimeParams["search_path"] = cfg.Schema
	}
	return pgxpool.NewWithConfig(ctx, pc)
}

// Migrate applies all pending migrations to the pool's search_path.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	provider, closeDB, err := newProvider(pool)
	if err != nil {
		return err
	}
	defer closeDB()
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}
	return nil
}

// MigrationVersion reports the newest applied migration version.
func MigrationVersion(ctx context.Context, pool *pgxpool.Pool) (int64, error) {
	provider, closeDB, err := newProvider(pool)
	if err != nil {
		return 0, err
	}
	defer closeDB()
	return provider.GetDBVersion(ctx)
}

// newProvider builds a goose provider over the pool; call the returned func when done.
func newProvider(pool *pgxpool.Pool) (*goose.Provider, func(), error) {
	db := stdlib.OpenDBFromPool(pool)
	closeDB := func() { _ = db.Close() }
	provider, err := goose.NewProvider(goose.DialectPostgres, db, mustSub(migrations, "migrations"))
	if err != nil {
		closeDB()
		return nil, nil, fmt.Errorf("init migrations: %w", err)
	}
	return provider, closeDB, nil
}

func mustSub(fsys embed.FS, dir string) fs.FS {
	sub, err := fs.Sub(fsys, dir)
	if err != nil {
		panic(err)
	}
	return sub
}
