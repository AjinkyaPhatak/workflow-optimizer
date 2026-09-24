package postgres_test

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	migratepostgres "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// freshMigrator returns a golang-migrate runner (the project's migration
// mechanism) and a pool, both bound to a new, empty, isolated schema. Nothing
// has been migrated yet, so tests control exactly which steps run.
func freshMigrator(t *testing.T) (*migrate.Migrate, *pgxpool.Pool) {
	t.Helper()
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("TEST_DATABASE_URL is required for PostgreSQL integration tests")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schema := "phase8m_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:20]
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") })
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	db, err := sql.Open("pgx", u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	driver, err := migratepostgres.WithInstance(db, &migratepostgres.Config{})
	if err != nil {
		t.Fatal(err)
	}
	m, err := migrate.NewWithDatabaseInstance("file://"+filepath.ToSlash(migrationsPath(t)), "postgres", driver)
	if err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return m, pool
}

// seedV1 inserts user -> workspace -> project -> workflow -> version 1 with
// the given version status, using only columns that exist in 000001.
func seedV1(t *testing.T, pool *pgxpool.Pool, status string) (workflowID, versionID uuid.UUID) {
	t.Helper()
	u, ws, pr := uuid.New(), uuid.New(), uuid.New()
	workflowID, versionID = uuid.New(), uuid.New()
	mustExec(t, pool, "INSERT INTO users (id, email, name, password_hash) VALUES ($1, $2, 'u', 'h')", u, u.String()+"@example.test")
	mustExec(t, pool, "INSERT INTO workspaces (id, name, owner_id) VALUES ($1, 'w', $2)", ws, u)
	mustExec(t, pool, "INSERT INTO projects (id, workspace_id, name) VALUES ($1, $2, 'p')", pr, ws)
	mustExec(t, pool, "INSERT INTO workflows (id, project_id, name) VALUES ($1, $2, 'f')", workflowID, pr)
	mustExec(t, pool, "INSERT INTO workflow_versions (id, workflow_id, version_number, definition, status) VALUES ($1, $2, 1, '{}', $3)", versionID, workflowID, status)
	return workflowID, versionID
}
