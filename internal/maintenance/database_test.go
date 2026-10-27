package maintenance

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"workflow-optimizer/internal/infrastructure/postgres"
)

func testDatabase(t *testing.T) *pgxpool.Pool {
	t.Helper()
	raw := os.Getenv("TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("TEST_DATABASE_URL is required")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	schema := "maintenance_" + uuid.New().String()[:8]
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(ctx, "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
		admin.Close()
	})
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	migrationDir, err := filepath.Abs("../../migrations")
	if err != nil {
		t.Fatal(err)
	}
	if err = postgres.ApplyMigrations(u.String(), migrationDir); err != nil {
		t.Fatal(err)
	}
	cfg, err := pgxpool.ParseConfig(u.String())
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}
func sql(t *testing.T, pool *pgxpool.Pool, query string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), query, args...); err != nil {
		t.Fatal(err)
	}
}
func seed(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	user, workspace, project, credential, file, base, document, wf, version, execution, session, run, automation := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	sql(t, pool, `INSERT INTO users(id,email,name,password_hash) VALUES($1,'reset@example.test','Preserve me','test-hash')`, user)
	sql(t, pool, `INSERT INTO workspaces(id,name,owner_id) VALUES($1,'Keep workspace',$2)`, workspace, user)
	sql(t, pool, `INSERT INTO workspace_members(workspace_id,user_id,role) VALUES($1,$2,'owner')`, workspace, user)
	sql(t, pool, `INSERT INTO projects(id,workspace_id,name) VALUES($1,$2,'Keep project')`, project, workspace)
	sql(t, pool, `INSERT INTO credentials(id,workspace_id,name,provider,credential_type,encrypted_data) VALUES($1,$2,'Keep sealed credential','google','oauth2','{"ciphertext":"test-only"}')`, credential, workspace)
	sql(t, pool, `INSERT INTO connected_accounts(workspace_id,user_id,provider,provider_account_id,credential_id,status) VALUES($1,$2,'google','test-account',$3,'ACTIVE')`, workspace, user, credential)
	sql(t, pool, `INSERT INTO whatsapp_sessions(workspace_id,user_id,encrypted_data) VALUES($1,$2,$3)`, workspace, user, []byte("test-only sealed session"))
	sql(t, pool, `INSERT INTO files(id,workspace_id,name,content_type,size_bytes,sha256,data,created_by) VALUES($1,$2,'keep.txt','text/plain',4,repeat('a',64),$3,$4)`, file, workspace, []byte("keep"), user)
	sql(t, pool, `INSERT INTO knowledge_bases(id,workspace_id,name,credential_id,embedding_model,dimensions) VALUES($1,$2,'Keep knowledge',$3,'test-model',3)`, base, workspace, credential)
	sql(t, pool, `INSERT INTO knowledge_documents(id,knowledge_base_id,workspace_id,title,source,file_id,status,content) VALUES($1,$2,$3,'Keep document','file',$4,'READY','keep')`, document, base, workspace, file)
	sql(t, pool, `INSERT INTO knowledge_chunks(document_id,knowledge_base_id,workspace_id,ordinal,content,embedding) VALUES($1,$2,$3,0,'keep',$4)`, document, base, workspace, []byte{1, 2, 3})
	sql(t, pool, `INSERT INTO workflows(id,project_id,name) VALUES($1,$2,'Delete workflow')`, wf, project)
	sql(t, pool, `INSERT INTO workflow_versions(id,workflow_id,version_number,definition,status) VALUES($1,$2,1,'{"version":1,"nodes":[],"edges":[],"settings":{}}','PUBLISHED')`, version, wf)
	sql(t, pool, `UPDATE workflows SET active_version_id=$2 WHERE id=$1`, wf, version)
	sql(t, pool, `INSERT INTO executions(id,workflow_id,workflow_version_id,status) VALUES($1,$2,$3,'PENDING')`, execution, wf, version)
	sql(t, pool, `INSERT INTO execution_events(execution_id,type) VALUES($1,'EXECUTION_STARTED')`, execution)
	sql(t, pool, `INSERT INTO saved_templates(workspace_id,name,definition,created_by) VALUES($1,'Delete template','{}',$2)`, workspace, user)
	sql(t, pool, `INSERT INTO assistant_sessions(id,workspace_id,user_id,body) VALUES($1,$2,$3,'{}')`, session, workspace, user)
	v2Version := uuid.New()
	sql(t, pool, `INSERT INTO automation_workflows(id,workspace_id,project_id,name) VALUES($1,$2,$3,'Delete compiler workflow')`, session, workspace, project)
	sql(t, pool, `INSERT INTO automation_workflow_versions(id,workflow_id,version_number,revision,body) VALUES($1,$2,1,1,'{}')`, v2Version, session)
	sql(t, pool, `UPDATE automation_workflows SET active_version_id=$2 WHERE id=$1`, session, v2Version)
	sql(t, pool, `INSERT INTO automation_editor_layout(workflow_id,positions) VALUES($1,'{"step":{"x":12,"y":24}}')`, session)
	sql(t, pool, `INSERT INTO automation_workflow_templates(id,workspace_id,body) VALUES($1,$2,'{}')`, uuid.New(), workspace)
	sql(t, pool, `INSERT INTO automation_runs(id,workspace_id,user_id,body) VALUES($1,$2,$3,'{}')`, run, workspace, user)
	sql(t, pool, `INSERT INTO automation_actions(run_id,operation_id,tool,status) VALUES($1,'test-send','mock.send','UNCERTAIN')`, run)
	sql(t, pool, `INSERT INTO automation_events(run_id,kind) VALUES($1,'started')`, run)
	sql(t, pool, `INSERT INTO automations(id,workspace_id,user_id,body) VALUES($1,$2,$3,'{}')`, automation, workspace, user)
	sql(t, pool, `INSERT INTO automation_triggers(automation_id,event_id) VALUES($1,'test-event')`, automation)
	sql(t, pool, `INSERT INTO automation_outbox(event_id,body) VALUES('test-event','{}')`)
}
func TestResetPreservesProtectedRowsAndRestoresGuards(t *testing.T) {
	pool := testDatabase(t)
	seed(t, pool)
	ctx := context.Background()
	before, err := Inspect(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if before.Workflow["execution_status_history"].Count == 0 {
		t.Fatal("fixture did not create immutable history")
	}
	if err = ClearDatabase(ctx, pool, before); err != nil {
		t.Fatal(err)
	}
	after, err := Inspect(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	for table, state := range after.Workflow {
		if state.Count != 0 {
			t.Errorf("%s: %d rows remain", table, state.Count)
		}
	}
	if !reflect.DeepEqual(before.Protected, after.Protected) {
		t.Fatal("protected data changed")
	}
	for _, table := range []string{"execution_events", "execution_status_history"} {
		if _, err = pool.Exec(ctx, "TRUNCATE TABLE "+pgx.Identifier{table}.Sanitize()); err == nil {
			t.Errorf("%s append-only guard was left disabled", table)
		}
	}
}
func TestResetRejectsChangesAfterInspection(t *testing.T) {
	pool := testDatabase(t)
	seed(t, pool)
	ctx := context.Background()
	before, err := Inspect(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	sql(t, pool, `UPDATE workflows SET name='A later edit'`)
	if err = ClearDatabase(ctx, pool, before); err == nil {
		t.Fatal("stale snapshot accepted")
	}
	after, err := Inspect(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if after.Workflow["workflows"].Count != 1 {
		t.Fatal("workflow was lost after refused reset")
	}
}
func TestUnknownForeignKeyRollsBackWithoutDisablingGuards(t *testing.T) {
	pool := testDatabase(t)
	seed(t, pool)
	ctx := context.Background()
	sql(t, pool, `CREATE TABLE preserved_external_link(workflow_id uuid REFERENCES workflows(id))`)
	before, err := Inspect(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if err = ClearDatabase(ctx, pool, before); err == nil {
		t.Fatal("reset bypassed an unknown foreign key")
	}
	after, err := Inspect(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before.Workflow, after.Workflow) {
		t.Fatal("failed reset changed workflow data")
	}
	var enabled string
	if err = pool.QueryRow(ctx, `SELECT tgenabled::text FROM pg_trigger WHERE tgrelid='execution_events'::regclass AND tgname='execution_events_no_truncate'`).Scan(&enabled); err != nil || enabled != "O" {
		t.Fatalf("guard restoration: %s %v", enabled, err)
	}
}
