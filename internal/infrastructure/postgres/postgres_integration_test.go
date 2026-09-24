package postgres_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	postgresinfra "workflow-optimizer/internal/infrastructure/postgres"
)

func TestSchemaRelationshipsConstraintsJSONBAndHistory(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is required for PostgreSQL integration tests")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, "DROP SCHEMA public CASCADE; CREATE SCHEMA public"); err != nil {
		t.Fatalf("reset test schema: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, "DROP SCHEMA public CASCADE; CREATE SCHEMA public") })
	if err := postgresinfra.ApplyMigrations(url, migrationsPath(t)); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}

	userID, workspaceID, projectID, workflowID, versionOneID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	mustExec(t, pool, "INSERT INTO users (id, email, name, password_hash) VALUES ($1, $2, 'User', 'hash')", userID, "user@example.test")
	mustExec(t, pool, "INSERT INTO workspaces (id, name, owner_id) VALUES ($1, 'Workspace', $2)", workspaceID, userID)
	mustExec(t, pool, "INSERT INTO workspace_members (workspace_id, user_id, role) VALUES ($1, $2, 'owner')", workspaceID, userID)
	mustExec(t, pool, "INSERT INTO projects (id, workspace_id, name) VALUES ($1, $2, 'Project')", projectID, workspaceID)
	mustExec(t, pool, "INSERT INTO workflows (id, project_id, name) VALUES ($1, $2, 'Workflow')", workflowID, projectID)
	mustExec(t, pool, "INSERT INTO workflow_versions (id, workflow_id, version_number, definition, status, created_by) VALUES ($1, $2, 1, '{\"nodes\": [], \"edges\": []}', 'PUBLISHED', $3)", versionOneID, workflowID, userID)
	mustExec(t, pool, "UPDATE workflows SET active_version_id = $1 WHERE id = $2", versionOneID, workflowID)
	mustExec(t, pool, "INSERT INTO credentials (id, workspace_id, name, provider, credential_type, encrypted_data) VALUES ($1, $2, 'Credential', 'openai', 'api_key', '{\"ciphertext\": \"test-only\"}')", uuid.New(), workspaceID)

	executionID := uuid.New()
	mustExec(t, pool, "INSERT INTO executions (id, workflow_id, workflow_version_id, status, input, output, error, started_at, finished_at) VALUES ($1, $2, $3, 'COMPLETED', '{\"input\": true}', '{\"output\": true}', '{\"error\": null}', now(), now())", executionID, workflowID, versionOneID)
	mustExec(t, pool, "INSERT INTO node_executions (id, execution_id, node_id, node_type, status, input, output, error, started_at, finished_at) VALUES ($1, $2, 'node-1', 'text', 'COMPLETED', '{\"input\": true}', '{\"output\": true}', '{\"error\": null}', now(), now())", uuid.New(), executionID)

	mustExec(t, pool, "INSERT INTO workflow_versions (id, workflow_id, version_number, definition, status, created_by) VALUES ($1, $2, 2, '{\"nodes\": [\"new\"]}', 'DRAFT', $3)", uuid.New(), workflowID, userID)
	var referencedVersion uuid.UUID
	if err := pool.QueryRow(ctx, "SELECT workflow_version_id FROM executions WHERE id = $1", executionID).Scan(&referencedVersion); err != nil {
		t.Fatalf("read execution version: %v", err)
	}
	if referencedVersion != versionOneID {
		t.Fatalf("execution version = %s, want historical version %s", referencedVersion, versionOneID)
	}

	assertRejected(t, pool, "INSERT INTO users (id, email, name, password_hash) VALUES ($1, $2, 'Duplicate', 'hash')", uuid.New(), "user@example.test")
	assertRejected(t, pool, "INSERT INTO workspace_members (workspace_id, user_id, role) VALUES ($1, $2, 'owner')", workspaceID, userID)
	assertRejected(t, pool, "INSERT INTO workflow_versions (id, workflow_id, version_number, definition, status) VALUES ($1, $2, 1, '{}', 'DRAFT')", uuid.New(), workflowID)
	assertRejected(t, pool, "INSERT INTO workspaces (id, name, owner_id) VALUES ($1, 'Invalid', $2)", uuid.New(), uuid.New())
	assertRejected(t, pool, "INSERT INTO projects (id, workspace_id, name) VALUES ($1, $2, 'Invalid')", uuid.New(), uuid.New())
	assertRejected(t, pool, "INSERT INTO workflows (id, project_id, name) VALUES ($1, $2, 'Invalid')", uuid.New(), uuid.New())
	assertRejected(t, pool, "INSERT INTO workflow_versions (id, workflow_id, version_number, definition, status) VALUES ($1, $2, 9, '{}', 'DRAFT')", uuid.New(), uuid.New())
	assertRejected(t, pool, "INSERT INTO credentials (id, workspace_id, name, provider, credential_type, encrypted_data) VALUES ($1, $2, 'Invalid', 'openai', 'api_key', '{}')", uuid.New(), uuid.New())
	assertRejected(t, pool, "INSERT INTO executions (id, workflow_id, workflow_version_id, status) VALUES ($1, $2, $3, 'PENDING')", uuid.New(), workflowID, uuid.New())
	assertRejected(t, pool, "INSERT INTO node_executions (id, execution_id, node_id, node_type, status) VALUES ($1, $2, 'node-invalid', 'text', 'PENDING')", uuid.New(), uuid.New())
	assertRejected(t, pool, "INSERT INTO users (id, email, name, password_hash) VALUES ($1, NULL, 'Missing', 'hash')", uuid.New())
}

func migrationsPath(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test file path")
	}
	return filepath.Join(filepath.Dir(file), "..", "..", "..", "migrations")
}

func mustExec(t *testing.T, pool *pgxpool.Pool, query string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), query, args...); err != nil {
		t.Fatalf("execute query: %v", err)
	}
}

func assertRejected(t *testing.T, pool *pgxpool.Pool, query string, args ...any) {
	t.Helper()
	_, err := pool.Exec(context.Background(), query, args...)
	if err == nil || errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("query unexpectedly succeeded: %s", query)
	}
}
