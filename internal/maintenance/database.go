// Package maintenance contains explicit, offline development maintenance only.
// No application startup path imports this package.
package maintenance

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// WorkflowTables is an allowlist, never a pattern and never CASCADE. New foreign
// keys outside this list make the reset fail rather than remove unrelated data.
var WorkflowTables = []string{
	"automation_editor_layout", "automation_workflow_versions", "automation_workflow_templates", "automation_workflows",
	"automation_actions", "automation_events", "automation_triggers", "automation_outbox", "automations", "automation_runs", "assistant_sessions",
	"execution_events", "execution_status_history", "execution_dead_letters", "execution_cancel_requests", "execution_dispatch", "execution_leases", "node_executions", "executions",
	"saved_templates", "workflow_versions", "workflows",
}
var ProtectedTables = []string{"users", "workspaces", "workspace_members", "projects", "credentials", "connected_accounts", "whatsapp_sessions", "files", "knowledge_bases", "knowledge_documents", "knowledge_chunks"}

type TableState struct {
	Count  int64  `json:"count"`
	Digest string `json:"-"`
}
type Snapshot struct {
	Schema           string                `json:"schema"`
	Workflow         map[string]TableState `json:"workflow_tables"`
	Protected        map[string]TableState `json:"preserved_tables"`
	ExecutionIDs     []string              `json:"-"`
	AutomationIDs    []string              `json:"-"`
	AutomationRunIDs []string              `json:"-"`
}
type querier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func tableName(schema, table string) string { return pgx.Identifier{schema, table}.Sanitize() }
func inspectTables(ctx context.Context, q querier, schema string, names []string) (map[string]TableState, error) {
	out := make(map[string]TableState, len(names))
	for _, name := range names {
		var present bool
		if err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_schema=$1 AND table_name=$2 AND table_type='BASE TABLE')`, schema, name).Scan(&present); err != nil {
			return nil, err
		}
		if !present {
			continue
		}
		var state TableState
		// Only hashes are kept in memory. Credential/file contents are never logged.
		sql := `SELECT count(*), md5(coalesce(string_agg(digest, '' ORDER BY digest), '')) FROM (SELECT md5(row_to_json(t)::text) AS digest FROM ` + tableName(schema, name) + ` t) rows`
		if err := q.QueryRow(ctx, sql).Scan(&state.Count, &state.Digest); err != nil {
			return nil, err
		}
		out[name] = state
	}
	return out, nil
}
func Inspect(ctx context.Context, pool *pgxpool.Pool) (Snapshot, error) {
	var out Snapshot
	if err := pool.QueryRow(ctx, `SELECT current_schema()`).Scan(&out.Schema); err != nil {
		return out, err
	}
	var err error
	if out.Workflow, err = inspectTables(ctx, pool, out.Schema, WorkflowTables); err != nil {
		return out, err
	}
	if out.Protected, err = inspectTables(ctx, pool, out.Schema, ProtectedTables); err != nil {
		return out, err
	}
	for _, entry := range []struct {
		table string
		dest  *[]string
	}{{"executions", &out.ExecutionIDs}, {"automations", &out.AutomationIDs}, {"automation_runs", &out.AutomationRunIDs}} {
		if _, exists := out.Workflow[entry.table]; !exists {
			continue
		}
		rows, err := pool.Query(ctx, `SELECT id::text FROM `+tableName(out.Schema, entry.table))
		if err != nil {
			return out, err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return out, err
			}
			*entry.dest = append(*entry.dest, id)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return out, err
		}
	}
	return out, nil
}

// AssertOffline prevents cleanup while API/worker pools or another client can
// recreate rows. The command uses a one-connection pool and all services must be
// stopped. Preview does not require exclusive access.
func AssertOffline(ctx context.Context, pool *pgxpool.Pool) error {
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid() AND backend_type='client backend'`).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return fmt.Errorf("database still has %d other client connections; stop the application and database clients first", count)
	}
	return nil
}

// ClearDatabase refuses stale snapshots, locks both sets, truncates the exact
// workflow tables, verifies every protected row, and restores history guards
// within the same transaction. It deliberately does not restart identity values.
func ClearDatabase(ctx context.Context, pool *pgxpool.Pool, expected Snapshot) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(1813342579)`); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `SET LOCAL lock_timeout='5s'`); err != nil {
		return err
	}
	protected := keys(expected.Protected)
	targets := keys(expected.Workflow)
	if len(targets) == 0 {
		return errors.New("no known workflow tables found; refusing reset")
	}
	qualify := func(names []string) string {
		out := make([]string, len(names))
		for i, n := range names {
			out[i] = tableName(expected.Schema, n)
		}
		return strings.Join(out, ",")
	}
	if len(protected) > 0 {
		if _, err = tx.Exec(ctx, "LOCK TABLE "+qualify(protected)+" IN SHARE MODE"); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, "LOCK TABLE "+qualify(targets)+" IN ACCESS EXCLUSIVE MODE"); err != nil {
		return err
	}
	before, err := inspectTables(ctx, tx, expected.Schema, WorkflowTables)
	if err != nil {
		return err
	}
	retained, err := inspectTables(ctx, tx, expected.Schema, ProtectedTables)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(before, expected.Workflow) || !reflect.DeepEqual(retained, expected.Protected) {
		return errors.New("database changed after preview; inspect again before reset")
	}
	// Row-level DELETE guards need no bypass: only these two statement-level
	// TRUNCATE guards are temporarily disabled, and their original state restored.
	type guard struct{ table, name, state string }
	var guards []guard
	for _, g := range []guard{{"execution_events", "execution_events_no_truncate", ""}, {"execution_status_history", "execution_status_history_no_truncate", ""}} {
		if _, exists := before[g.table]; !exists {
			continue
		}
		err = tx.QueryRow(ctx, `SELECT t.tgenabled::text FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relname=$2 AND t.tgname=$3`, expected.Schema, g.table, g.name).Scan(&g.state)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		if g.state != "O" {
			return fmt.Errorf("expected enabled guard %s; refusing to alter existing maintenance state", g.name)
		}
		if _, err = tx.Exec(ctx, "ALTER TABLE "+tableName(expected.Schema, g.table)+" DISABLE TRIGGER "+pgx.Identifier{g.name}.Sanitize()); err != nil {
			return err
		}
		guards = append(guards, g)
	}
	if _, err = tx.Exec(ctx, "TRUNCATE TABLE "+qualify(targets)+" CONTINUE IDENTITY RESTRICT"); err != nil {
		return err
	}
	for _, g := range guards {
		if _, err = tx.Exec(ctx, "ALTER TABLE "+tableName(expected.Schema, g.table)+" ENABLE TRIGGER "+pgx.Identifier{g.name}.Sanitize()); err != nil {
			return err
		}
	}
	retained, err = inspectTables(ctx, tx, expected.Schema, ProtectedTables)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(retained, expected.Protected) {
		return errors.New("protected records changed; reset rolled back")
	}
	return tx.Commit(ctx)
}
func keys(m map[string]TableState) []string {
	out := make([]string, 0, len(m))
	for name := range m {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
