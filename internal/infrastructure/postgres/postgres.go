// Package postgres contains PostgreSQL infrastructure adapters. Domain packages
// depend on repository interfaces, never on this package.
package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/golang-migrate/migrate/v4"
	postgresdriver "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// Store owns a PostgreSQL connection pool.
type Store struct {
	Pool *pgxpool.Pool
}

// Open creates and verifies a PostgreSQL connection pool.
func Open(ctx context.Context, databaseURL string) (*Store, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("create PostgreSQL pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping PostgreSQL: %w", err)
	}
	return &Store{Pool: pool}, nil
}

// Close releases PostgreSQL connections.
func (s *Store) Close() {
	if s != nil && s.Pool != nil {
		s.Pool.Close()
	}
}

// ApplyMigrations applies pending SQL migrations from migrationsDir.
func ApplyMigrations(databaseURL, migrationsDir string) error {
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return fmt.Errorf("open migration database: %w", err)
	}
	defer db.Close()

	driver, err := postgresdriver.WithInstance(db, &postgresdriver.Config{})
	if err != nil {
		return fmt.Errorf("create PostgreSQL migration driver: %w", err)
	}
	sourceURL := "file://" + filepath.ToSlash(migrationsDir)
	m, err := migrate.NewWithDatabaseInstance(sourceURL, "postgres", driver)
	if err != nil {
		return fmt.Errorf("create migration runner: %w", err)
	}
	defer m.Close()
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("apply migrations: %w", err)
	}
	return nil
}
