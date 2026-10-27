package postgres_test

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestSchemaRelationshipsConstraintsJSONBAndHistory(t *testing.T) {
	// The caller's database may contain development data. Use the same isolated
	// schema as the other repository tests; never reset the public schema.
	pool := newPGEnv(t).raw
	ctx := context.Background()

	userID, workspaceID, projectID, workflowID, versionOneID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	mustExec(t, pool, "INSERT INTO users (id, email, name, password_hash) VALUES ($1, $2, 'User', 'hash')", userID, "user@example.test")
	mustExec(t, pool, "INSERT INTO workspaces (id, name, owner_id) VALUES ($1, 'Workspace', $2)", workspaceID, userID)
	mustExec(t, pool, "INSERT INTO workspace_members (workspace_id, user_id, role) VALUES ($1, $2, 'owner')", workspaceID, userID)
	mustExec(t, pool, "INSERT INTO projects (id, workspace_id, name) VALUES ($1, $2, 'Project')", projectID, workspaceID)
	mustExec(t, pool, "INSERT INTO workflows (id, project_id, name) VALUES ($1, $2, 'Workflow')", workflowID, projectID)
	mustExec(t, pool, "INSERT INTO workflow_versions (id, workflow_id, version_number, definition, status, created_by) VALUES ($1, $2, 1, '{\"nodes\": [], \"edges\": []}', 'PUBLISHED', $3)", versionOneID, workflowID, userID)
	mustExec(t, pool, "UPDATE workflows SET active_version_id = $1 WHERE id = $2", versionOneID, workflowID)
	mustExec(t, pool, "INSERT INTO credentials (id, workspace_id, name, provider, credential_type, encrypted_data) VALUES ($1, $2, 'Credential', 'openai', 'api_key', '{\"ciphertext\": \"test-only\"}')", uuid.New(), workspaceID)

	// Phase 8 makes the database the lifecycle authority: rows are created
	// PENDING and reach COMPLETED only through legal transitions.
	executionID, nodeExecutionID := uuid.New(), uuid.New()
	mustExec(t, pool, "INSERT INTO executions (id, workflow_id, workflow_version_id, status, input) VALUES ($1, $2, $3, 'PENDING', '{\"input\": true}')", executionID, workflowID, versionOneID)
	mustExec(t, pool, "UPDATE executions SET status = 'RUNNING', claim_token = $2 WHERE id = $1", executionID, uuid.New())
	mustExec(t, pool, "INSERT INTO node_executions (id, execution_id, node_id, node_type, status, input) VALUES ($1, $2, 'node-1', 'text', 'PENDING', '{\"input\": true}')", nodeExecutionID, executionID)
	mustExec(t, pool, "UPDATE node_executions SET status = 'RUNNING' WHERE id = $1", nodeExecutionID)
	mustExec(t, pool, "UPDATE node_executions SET status = 'COMPLETED', output = '{\"output\": true}' WHERE id = $1", nodeExecutionID)
	mustExec(t, pool, "UPDATE executions SET status = 'COMPLETED', output = '{\"output\": true}' WHERE id = $1", executionID)

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
