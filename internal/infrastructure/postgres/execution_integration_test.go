package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"workflow-optimizer/internal/app"
	"workflow-optimizer/internal/config"
	"workflow-optimizer/internal/execution"
	postgresinfra "workflow-optimizer/internal/infrastructure/postgres"
	"workflow-optimizer/internal/node"
	"workflow-optimizer/internal/workflow"
)

// pgEnv is one isolated PostgreSQL environment: a uniquely named schema in the
// TEST_DATABASE_URL database, migrated with the project's own migrations and
// dropped afterwards. Nothing outside that schema is touched.
type pgEnv struct {
	url      string
	store    *postgresinfra.Store
	raw      *pgxpool.Pool
	execs    *postgresinfra.ExecutionRepository
	nodes    *postgresinfra.NodeExecutionRepository
	versions *postgresinfra.WorkflowVersionRepository
	svc      *execution.LifecycleService
}

func newPGEnv(t *testing.T) *pgEnv {
	t.Helper()
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("TEST_DATABASE_URL is required for PostgreSQL integration tests")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, base)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(admin.Close)
	schema := "phase8_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:20]
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create test schema: %v", err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") })

	u, err := url.Parse(base)
	if err != nil {
		t.Fatalf("parse TEST_DATABASE_URL: %v", err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	scoped := u.String()

	if err := postgresinfra.ApplyMigrations(scoped, migrationsPath(t)); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	store := openStore(t, scoped)
	return &pgEnv{
		url:      scoped,
		store:    store,
		raw:      store.Pool,
		execs:    postgresinfra.NewExecutionRepository(store),
		nodes:    postgresinfra.NewNodeExecutionRepository(store),
		versions: postgresinfra.NewWorkflowVersionRepository(store),
		svc:      execution.NewLifecycleService(postgresinfra.NewExecutionRepository(store)),
	}
}

// openStore opens an independent pool, standing in for a separate worker process.
func openStore(t *testing.T, dsn string) *postgresinfra.Store {
	t.Helper()
	store, err := postgresinfra.Open(context.Background(), dsn)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(store.Close)
	return store
}

// seedWorkflow inserts user -> workspace -> project -> workflow -> version 1.
func (p *pgEnv) seedWorkflow(t *testing.T, def any) (workflowID, versionID uuid.UUID) {
	t.Helper()
	if def == nil {
		def = map[string]any{"version": 1, "nodes": []any{}, "edges": []any{}, "settings": map[string]any{}}
	}
	defJSON, err := json.Marshal(def)
	if err != nil {
		t.Fatal(err)
	}
	userID, wsID, projectID := uuid.New(), uuid.New(), uuid.New()
	workflowID, versionID = uuid.New(), uuid.New()
	mustExec(t, p.raw, "INSERT INTO users (id, email, name, password_hash) VALUES ($1, $2, 'U', 'h')", userID, userID.String()+"@example.test")
	mustExec(t, p.raw, "INSERT INTO workspaces (id, name, owner_id) VALUES ($1, 'W', $2)", wsID, userID)
	mustExec(t, p.raw, "INSERT INTO projects (id, workspace_id, name) VALUES ($1, $2, 'P')", projectID, wsID)
	mustExec(t, p.raw, "INSERT INTO workflows (id, project_id, name) VALUES ($1, $2, 'F')", workflowID, projectID)
	mustExec(t, p.raw, "INSERT INTO workflow_versions (id, workflow_id, version_number, definition, status) VALUES ($1, $2, 1, $3, 'PUBLISHED')", versionID, workflowID, defJSON)
	return workflowID, versionID
}

func (p *pgEnv) create(t *testing.T, input map[string]any) execution.Execution {
	t.Helper()
	wf, v := p.seedWorkflow(t, nil)
	e, err := p.svc.Create(context.Background(), wf, v, input)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	return e
}

func (p *pgEnv) get(t *testing.T, id uuid.UUID) execution.Execution {
	t.Helper()
	e, err := p.execs.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	return e
}

func (p *pgEnv) history(t *testing.T, id uuid.UUID) []execution.StatusTransition {
	t.Helper()
	h, err := p.execs.History(context.Background(), id)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	return h
}

func historyPairs(h []execution.StatusTransition) []string {
	out := make([]string, len(h))
	for i, tr := range h {
		from := "NULL"
		if tr.From != nil {
			from = string(*tr.From)
		}
		out[i] = from + "->" + string(tr.To)
	}
	return out
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// Creation, retrieval, JSONB, foreign keys
// ---------------------------------------------------------------------------

func TestPGExecutionCreateGetAndJSONBInput(t *testing.T) {
	p := newPGEnv(t)
	input := map[string]any{
		"query":  "hello",
		"n":      42.5,
		"flag":   true,
		"none":   nil,
		"nested": map[string]any{"list": []any{"a", 1.0, map[string]any{"k": "v"}}},
	}
	e := p.create(t, input)
	if e.Status != execution.StatusPending || e.StartedAt != nil || e.FinishedAt != nil || e.Output != nil || e.Error != nil {
		t.Fatalf("created = %+v", e)
	}
	if e.CreatedAt.IsZero() || e.UpdatedAt.IsZero() {
		t.Fatal("created_at / updated_at must be set")
	}
	got := p.get(t, e.ID)
	if b1, _ := json.Marshal(got.Input); string(b1) != mustJSON(t, input) {
		t.Fatalf("input round trip = %s", b1)
	}
	var kind string
	if err := p.raw.QueryRow(context.Background(), "SELECT jsonb_typeof(input->'nested'->'list') FROM executions WHERE id=$1", e.ID).Scan(&kind); err != nil || kind != "array" {
		t.Fatalf("input stored as JSONB: kind=%q err=%v", kind, err)
	}
	if h := historyPairs(p.history(t, e.ID)); !sameStrings(h, []string{"NULL->PENDING"}) {
		t.Fatalf("history = %v", h)
	}
	if _, err := p.execs.Get(context.Background(), uuid.New()); !errors.Is(err, execution.ErrExecutionNotFound) {
		t.Fatalf("missing execution: %v", err)
	}
	// Create never starts: a non-PENDING create is refused.
	bad := execution.Execution{ID: uuid.New(), WorkflowID: e.WorkflowID, WorkflowVersionID: e.WorkflowVersionID, Status: execution.StatusRunning}
	if _, err := p.execs.Create(context.Background(), bad); !errors.Is(err, execution.ErrInvalidExecution) {
		t.Fatalf("RUNNING create: %v", err)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestPGExecutionWorkflowAndVersionIntegrity(t *testing.T) {
	p := newPGEnv(t)
	ctx := context.Background()
	wfA, vA := p.seedWorkflow(t, nil)
	wfB, vB := p.seedWorkflow(t, nil)

	if _, err := p.svc.Create(ctx, uuid.New(), vA, nil); !errors.Is(err, execution.ErrWorkflowNotFound) {
		t.Fatalf("unknown workflow: %v", err)
	}
	if _, err := p.svc.Create(ctx, wfA, uuid.New(), nil); !errors.Is(err, execution.ErrWorkflowVersionNotFound) && !errors.Is(err, execution.ErrWorkflowVersionMismatch) {
		t.Fatalf("unknown version: %v", err)
	}
	// Both rows exist, but version B belongs to workflow B.
	if _, err := p.svc.Create(ctx, wfA, vB, nil); !errors.Is(err, execution.ErrWorkflowVersionMismatch) {
		t.Fatalf("cross-workflow version: %v", err)
	}
	// The invariant is enforced by the database, not only the repository.
	assertRejected(t, p.raw, "INSERT INTO executions (id, workflow_id, workflow_version_id, status) VALUES ($1, $2, $3, 'PENDING')", uuid.New(), wfA, vB)
	if _, err := p.svc.Create(ctx, wfB, vB, nil); err != nil {
		t.Fatalf("matching workflow/version must succeed: %v", err)
	}
	var n int
	_ = p.raw.QueryRow(ctx, "SELECT count(*) FROM executions").Scan(&n)
	if n != 1 {
		t.Fatalf("executions = %d, want 1", n)
	}
}

// ---------------------------------------------------------------------------
// Lifecycle persistence, timestamps, history
// ---------------------------------------------------------------------------

func TestPGCompletedLifecyclePersistsTimestampsOutputAndHistory(t *testing.T) {
	p := newPGEnv(t)
	ctx := context.Background()
	e := p.create(t, map[string]any{"q": 1.0})

	time.Sleep(5 * time.Millisecond)
	if err := p.svc.Start(ctx, e.ID); err != nil {
		t.Fatalf("start: %v", err)
	}
	running := p.get(t, e.ID)
	if running.Status != execution.StatusRunning || running.StartedAt == nil || running.FinishedAt != nil ||
		!running.UpdatedAt.After(e.UpdatedAt) || !running.CreatedAt.Equal(e.CreatedAt) {
		t.Fatalf("running = %+v", running)
	}

	time.Sleep(5 * time.Millisecond)
	output := map[string]any{"out": map[string]any{"result": map[string]any{"answer": "42", "items": []any{1.0, 2.0}}}}
	if err := p.svc.Complete(ctx, e.ID, output); err != nil {
		t.Fatalf("complete: %v", err)
	}
	done := p.get(t, e.ID)
	if done.Status != execution.StatusCompleted || done.FinishedAt == nil || !done.StartedAt.Equal(*running.StartedAt) ||
		done.FinishedAt.Before(*done.StartedAt) || !done.UpdatedAt.After(running.UpdatedAt) || done.Error != nil {
		t.Fatalf("completed = %+v", done)
	}
	if mustJSON(t, done.Output) != mustJSON(t, output) {
		t.Fatalf("output = %s", mustJSON(t, done.Output))
	}
	var answer string
	_ = p.raw.QueryRow(ctx, "SELECT output->'out'->'result'->>'answer' FROM executions WHERE id=$1", e.ID).Scan(&answer)
	if answer != "42" {
		t.Fatalf("JSONB output path = %q", answer)
	}
	h := p.history(t, e.ID)
	if got := historyPairs(h); !sameStrings(got, []string{"NULL->PENDING", "PENDING->RUNNING", "RUNNING->COMPLETED"}) {
		t.Fatalf("history = %v", got)
	}
	if h[0].CreatedAt.After(h[1].CreatedAt) || h[1].CreatedAt.After(h[2].CreatedAt) {
		t.Fatal("history timestamps must be ordered")
	}
}

func TestPGFailedExecutionPersistsStructuredErrorJSONB(t *testing.T) {
	p := newPGEnv(t)
	ctx := context.Background()
	e := p.create(t, nil)
	_ = p.svc.Start(ctx, e.ID)
	nodeID := "llm_1"
	if err := p.svc.Fail(ctx, e.ID, execution.ExecutionError{Code: "RATE_LIMITED", Message: "429", NodeID: &nodeID, Retryable: true}); err != nil {
		t.Fatalf("fail: %v", err)
	}
	got := p.get(t, e.ID)
	if got.Status != execution.StatusFailed || got.Error == nil || got.Error.Code != "RATE_LIMITED" || *got.Error.NodeID != "llm_1" ||
		!got.Error.Retryable || got.Output != nil || got.FinishedAt == nil {
		t.Fatalf("failed = %+v / %+v", got, got.Error)
	}
	var code, node string
	var retryable bool
	if err := p.raw.QueryRow(ctx, "SELECT error->>'code', error->>'node_id', (error->>'retryable')::boolean FROM executions WHERE id=$1", e.ID).
		Scan(&code, &node, &retryable); err != nil || code != "RATE_LIMITED" || node != "llm_1" || !retryable {
		t.Fatalf("JSONB error = %q %q %v %v", code, node, retryable, err)
	}
	h := p.history(t, e.ID)
	if last := h[len(h)-1]; last.To != execution.StatusFailed || last.Metadata["error_code"] != "RATE_LIMITED" || last.Metadata["node_id"] != "llm_1" {
		t.Fatalf("failure history = %+v", last)
	}
	// FAILED requires an error at the database level too.
	e2 := p.create(t, nil)
	_ = p.svc.Start(ctx, e2.ID)
	assertRejected(t, p.raw, "UPDATE executions SET status='FAILED', finished_at=now() WHERE id=$1", e2.ID)
}

func TestPGCancelledExecutionPersists(t *testing.T) {
	p := newPGEnv(t)
	ctx := context.Background()
	e := p.create(t, nil)
	_ = p.svc.Start(ctx, e.ID)
	if err := p.svc.Cancel(ctx, e.ID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	got := p.get(t, e.ID)
	if got.Status != execution.StatusCancelled || got.FinishedAt == nil || got.StartedAt == nil || got.Output != nil {
		t.Fatalf("cancelled = %+v", got)
	}
	if h := historyPairs(p.history(t, e.ID)); !sameStrings(h, []string{"NULL->PENDING", "PENDING->RUNNING", "RUNNING->CANCELLED"}) {
		t.Fatalf("history = %v", h)
	}
}

func TestPGInvalidTransitionsAreRejected(t *testing.T) {
	p := newPGEnv(t)
	ctx := context.Background()
	apply := func(id uuid.UUID, to execution.ExecutionStatus) error {
		switch to {
		case execution.StatusRunning:
			return p.svc.Start(ctx, id)
		case execution.StatusCompleted:
			return p.svc.Complete(ctx, id, map[string]any{})
		case execution.StatusFailed:
			return p.svc.Fail(ctx, id, execution.ExecutionError{Code: "X", Message: "m"})
		default:
			return p.svc.Cancel(ctx, id)
		}
	}
	cases := []struct{ from, to execution.ExecutionStatus }{
		{execution.StatusPending, execution.StatusCompleted},
		{execution.StatusPending, execution.StatusFailed},
		{execution.StatusPending, execution.StatusCancelled},
		{execution.StatusCompleted, execution.StatusRunning},
		{execution.StatusCompleted, execution.StatusFailed},
		{execution.StatusFailed, execution.StatusRunning},
		{execution.StatusFailed, execution.StatusCompleted},
		{execution.StatusCancelled, execution.StatusRunning},
		{execution.StatusCancelled, execution.StatusCompleted},
	}
	for _, tc := range cases {
		t.Run(string(tc.from)+"->"+string(tc.to), func(t *testing.T) {
			e := p.create(t, nil)
			if tc.from != execution.StatusPending {
				_ = p.svc.Start(ctx, e.ID)
				if err := apply(e.ID, tc.from); err != nil {
					t.Fatal(err)
				}
			}
			before := p.get(t, e.ID)
			hBefore := len(p.history(t, e.ID))
			err := apply(e.ID, tc.to)
			var te *execution.TransitionError
			if !errors.Is(err, execution.ErrInvalidTransition) || !errors.As(err, &te) || te.From != tc.from {
				t.Fatalf("err = %v", err)
			}
			after := p.get(t, e.ID)
			if after.Status != tc.from || !after.UpdatedAt.Equal(before.UpdatedAt) || len(p.history(t, e.ID)) != hBefore {
				t.Fatal("rejected transition changed persisted state or history")
			}
			if (before.FinishedAt == nil) != (after.FinishedAt == nil) || (before.FinishedAt != nil && !after.FinishedAt.Equal(*before.FinishedAt)) {
				t.Fatal("finished_at overwritten by rejected transition")
			}
		})
	}
	// A repository call with an illegal pair never reaches the database.
	e := p.create(t, nil)
	if err := p.execs.Transition(ctx, e.ID, execution.StatusPending, execution.StatusCompleted, execution.TransitionUpdate{}); !errors.Is(err, execution.ErrInvalidTransition) {
		t.Fatalf("illegal repository pair: %v", err)
	}
}

func TestPGDatabaseEnforcesStateMachineForAnyWriter(t *testing.T) {
	p := newPGEnv(t)
	ctx := context.Background()
	pending := p.create(t, nil)
	assertRejected(t, p.raw, "UPDATE executions SET status='COMPLETED', started_at=now(), finished_at=now() WHERE id=$1", pending.ID)
	assertRejected(t, p.raw, "UPDATE executions SET input='{\"x\":1}' WHERE id=$1", pending.ID)

	running := p.create(t, nil)
	_ = p.svc.Start(ctx, running.ID)
	assertRejected(t, p.raw, "UPDATE executions SET status='PENDING', started_at=NULL WHERE id=$1", running.ID)
	assertRejected(t, p.raw, "UPDATE executions SET started_at = started_at + interval '1 hour' WHERE id=$1", running.ID)

	done := p.create(t, nil)
	_ = p.svc.Start(ctx, done.ID)
	_ = p.svc.Complete(ctx, done.ID, map[string]any{"a": 1.0})
	assertRejected(t, p.raw, "UPDATE executions SET output='{\"tampered\":true}' WHERE id=$1", done.ID)
	assertRejected(t, p.raw, "UPDATE executions SET finished_at = now() + interval '1 day' WHERE id=$1", done.ID)
	if got := p.get(t, done.ID); mustJSON(t, got.Output) != `{"a":1}` {
		t.Fatalf("terminal output changed: %+v", got.Output)
	}
}

func TestPGStatusHistoryIsAppendOnly(t *testing.T) {
	p := newPGEnv(t)
	ctx := context.Background()
	e := p.create(t, nil)
	_ = p.svc.Start(ctx, e.ID)
	h := p.history(t, e.ID)

	assertRejected(t, p.raw, "UPDATE execution_status_history SET to_status='COMPLETED' WHERE id=$1", h[1].ID)
	assertRejected(t, p.raw, "UPDATE execution_status_history SET metadata='{\"x\":1}' WHERE execution_id=$1", e.ID)
	assertRejected(t, p.raw, "DELETE FROM execution_status_history WHERE id=$1", h[0].ID)
	assertRejected(t, p.raw, "TRUNCATE execution_status_history")
	// Only lifecycle-legal pairs may be recorded, and a status is entered once.
	assertRejected(t, p.raw, "INSERT INTO execution_status_history (execution_id, from_status, to_status) VALUES ($1, 'PENDING', 'COMPLETED')", e.ID)
	assertRejected(t, p.raw, "INSERT INTO execution_status_history (execution_id, from_status, to_status) VALUES ($1, 'PENDING', 'RUNNING')", e.ID)
	assertRejected(t, p.raw, "INSERT INTO execution_status_history (execution_id, from_status, to_status) VALUES ($1, NULL, 'PENDING')", uuid.New())

	after := p.history(t, e.ID)
	if len(after) != len(h) || after[1].To != execution.StatusRunning || after[0].ID != h[0].ID {
		t.Fatalf("history mutated: %v", historyPairs(after))
	}
}

// ---------------------------------------------------------------------------
// Atomic ownership
// ---------------------------------------------------------------------------

// TestPGConcurrentStartExactlyOneClaim is the mandatory ownership test: workers
// with independent connection pools (separate "processes") race to claim the
// same PENDING execution. PostgreSQL must grant exactly one claim.
func TestPGConcurrentStartExactlyOneClaim(t *testing.T) {
	p := newPGEnv(t)
	for _, tc := range []struct {
		name              string
		workers, attempts int
	}{
		{"two workers", 2, 50},
		{"eight workers", 8, 25},
	} {
		t.Run(tc.name, func(t *testing.T) {
			services := make([]*execution.LifecycleService, tc.workers)
			for i := range services {
				services[i] = execution.NewLifecycleService(postgresinfra.NewExecutionRepository(openStore(t, p.url)))
			}
			for attempt := 0; attempt < tc.attempts; attempt++ {
				e := p.create(t, nil)
				start := make(chan struct{})
				results := make(chan error, tc.workers)
				for _, svc := range services {
					go func(svc *execution.LifecycleService) {
						<-start
						results <- svc.Start(context.Background(), e.ID)
					}(svc)
				}
				close(start)
				wins, losses := 0, 0
				for i := 0; i < tc.workers; i++ {
					err := <-results
					switch {
					case err == nil:
						wins++
					case errors.Is(err, execution.ErrExecutionNotClaimable):
						losses++
					default:
						t.Fatalf("attempt %d: unexpected error: %v", attempt, err)
					}
				}
				if wins != 1 || losses != tc.workers-1 {
					t.Fatalf("attempt %d: wins=%d losses=%d, want exactly one claim", attempt, wins, losses)
				}
				got := p.get(t, e.ID)
				if got.Status != execution.StatusRunning || got.StartedAt == nil {
					t.Fatalf("attempt %d: final state %+v", attempt, got)
				}
				var claims int
				_ = p.raw.QueryRow(context.Background(),
					"SELECT count(*) FROM execution_status_history WHERE execution_id=$1 AND to_status='RUNNING'", e.ID).Scan(&claims)
				if claims != 1 {
					t.Fatalf("attempt %d: %d successful ownership transitions recorded", attempt, claims)
				}
			}
		})
	}
}

// TestPGBlockedClaimLosesAfterWinnerCommits makes the race deterministic: a
// competing transaction holds the claim uncommitted; Start must block on the
// row lock and, once the competitor commits, re-check and lose.
func TestPGBlockedClaimLosesAfterWinnerCommits(t *testing.T) {
	p := newPGEnv(t)
	ctx := context.Background()
	e := p.create(t, nil)
	other := openStore(t, p.url)
	tx, err := other.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, "UPDATE executions SET status='RUNNING', claim_token=$2 WHERE id=$1 AND status='PENDING'", e.ID, uuid.New()); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- p.svc.Start(ctx, e.ID) }()
	select {
	case err := <-done:
		t.Fatalf("Start returned while the competing claim was uncommitted: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, execution.ErrExecutionNotClaimable) {
			t.Fatalf("blocked claim err = %v, want ErrExecutionNotClaimable", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Start did not resume after the competitor committed")
	}
}

// ---------------------------------------------------------------------------
// Node executions
// ---------------------------------------------------------------------------

func TestPGNodeExecutionPersistence(t *testing.T) {
	p := newPGEnv(t)
	ctx := context.Background()
	machine := execution.NewNodeExecutionStateMachine(p.nodes)
	e := p.create(t, nil)
	other := p.create(t, nil)
	for _, x := range []uuid.UUID{e.ID, other.ID} {
		if err := p.svc.Start(ctx, x); err != nil {
			t.Fatal(err)
		}
	}

	orphan := execution.NodeExecution{ID: uuid.New(), ExecutionID: uuid.New(), NodeID: "x", NodeType: "text", Status: execution.NodeStatusPending}
	if err := p.nodes.Create(ctx, orphan); !errors.Is(err, execution.ErrExecutionNotFound) {
		t.Fatalf("orphan node execution: %v", err)
	}
	newNode := func(execID uuid.UUID, id string) uuid.UUID {
		rec := execution.NodeExecution{ID: uuid.New(), ExecutionID: execID, NodeID: id, NodeType: "text", Status: execution.NodeStatusPending}
		if err := p.nodes.Create(ctx, rec); err != nil {
			t.Fatalf("create node %s: %v", id, err)
		}
		return rec.ID
	}

	a := newNode(e.ID, "A")
	pending, _ := p.nodes.Get(ctx, a)
	if pending.Status != execution.NodeStatusPending || pending.StartedAt != nil || pending.FinishedAt != nil || pending.ExecutionID != e.ID {
		t.Fatalf("pending node = %+v", pending)
	}
	if err := machine.Transition(ctx, a, execution.NodeStatusCompleted, execution.NodeTransitionUpdate{}); !errors.Is(err, execution.ErrInvalidTransition) {
		t.Fatalf("PENDING->COMPLETED: %v", err)
	}
	input := map[string]any{"ports": map[string]any{"input": "hi"}, "config": map[string]any{"text": "x"}}
	if err := machine.Transition(ctx, a, execution.NodeStatusRunning, execution.NodeTransitionUpdate{Input: input}); err != nil {
		t.Fatal(err)
	}
	running, _ := p.nodes.Get(ctx, a)
	if running.StartedAt == nil || running.FinishedAt != nil || mustJSON(t, running.Input) != mustJSON(t, input) {
		t.Fatalf("running node = %+v", running)
	}
	if err := machine.Transition(ctx, a, execution.NodeStatusCompleted, execution.NodeTransitionUpdate{Output: map[string]any{"output": "hi"}}); err != nil {
		t.Fatal(err)
	}
	completed, _ := p.nodes.Get(ctx, a)
	if completed.FinishedAt == nil || !completed.StartedAt.Equal(*running.StartedAt) || completed.Output["output"] != "hi" {
		t.Fatalf("completed node = %+v", completed)
	}
	if err := machine.Transition(ctx, a, execution.NodeStatusRunning, execution.NodeTransitionUpdate{}); !errors.Is(err, execution.ErrInvalidTransition) {
		t.Fatalf("COMPLETED->RUNNING: %v", err)
	}
	assertRejected(t, p.raw, "UPDATE node_executions SET output='{\"x\":1}' WHERE id=$1", a)

	b := newNode(e.ID, "B")
	_ = machine.Transition(ctx, b, execution.NodeStatusRunning, execution.NodeTransitionUpdate{})
	nid := "B"
	if err := machine.Transition(ctx, b, execution.NodeStatusFailed, execution.NodeTransitionUpdate{Error: &execution.ExecutionError{Code: "EXECUTION_FAILED", Message: "boom", NodeID: &nid}}); err != nil {
		t.Fatal(err)
	}
	var code, errNode string
	_ = p.raw.QueryRow(ctx, "SELECT error->>'code', error->>'node_id' FROM node_executions WHERE id=$1", b).Scan(&code, &errNode)
	if code != "EXECUTION_FAILED" || errNode != "B" {
		t.Fatalf("node error JSONB = %q %q", code, errNode)
	}

	c := newNode(e.ID, "C")
	_ = machine.Transition(ctx, c, execution.NodeStatusRunning, execution.NodeTransitionUpdate{})
	if err := machine.Transition(ctx, c, execution.NodeStatusSkipped, execution.NodeTransitionUpdate{}); err != nil {
		t.Fatalf("RUNNING->SKIPPED: %v", err)
	}
	assertRejected(t, p.raw, "INSERT INTO node_executions (id, execution_id, node_id, node_type, status) VALUES ($1, $2, 'D', 'text', 'CANCELLED')", uuid.New(), e.ID)
	// One record per node attempt.
	if err := p.nodes.Create(ctx, execution.NodeExecution{ID: uuid.New(), ExecutionID: e.ID, NodeID: "A", NodeType: "text", Status: execution.NodeStatusPending}); !errors.Is(err, execution.ErrInvalidExecution) {
		t.Fatalf("duplicate node record: %v", err)
	}

	newNode(other.ID, "A")
	list, err := p.nodes.ListByExecution(ctx, e.ID)
	if err != nil || len(list) != 3 {
		t.Fatalf("ListByExecution = %d, %v", len(list), err)
	}
	for _, n := range list {
		if n.ExecutionID != e.ID {
			t.Fatalf("foreign node record listed: %+v", n)
		}
	}
	assertRejected(t, p.raw, "INSERT INTO node_executions (id, execution_id, node_id, node_type, status) VALUES ($1, $2, 'Z', 'text', 'PENDING')", uuid.New(), uuid.New())
}

// ---------------------------------------------------------------------------
// End-to-end: Runner + GraphExecutor + PostgreSQL
// ---------------------------------------------------------------------------

type failNode struct{}

func (failNode) Type() string { return "test.fail" }
func (failNode) Execute(context.Context, node.NodeInput) (node.NodeOutput, error) {
	return node.NodeOutput{}, node.NewNodeError(node.ErrCodeRateLimited, "provider throttled", true)
}

// cancelNode cancels the run's context mid-graph (a caller cancelling).
type cancelNode struct{ cancel *context.CancelFunc }

func (cancelNode) Type() string { return "test.cancel" }
func (c cancelNode) Execute(ctx context.Context, in node.NodeInput) (node.NodeOutput, error) {
	if *c.cancel != nil {
		(*c.cancel)()
	}
	out := node.NewNodeOutput(nil)
	v, _ := in.GetPort("input")
	out.SetPort("output", v)
	return out, nil
}

func passDefinition(typ string) node.NodeDefinition {
	return node.NodeDefinition{
		Type: typ, Name: typ, Category: node.CategoryUtilities,
		Inputs:  []node.PortDefinition{node.NewPortDefinition("input", node.ValueTypeJSON, true, "")},
		Outputs: []node.PortDefinition{node.NewPortDefinition("output", node.ValueTypeJSON, true, "")},
	}
}

type runnerFixture struct {
	*pgEnv
	runner *execution.Runner
	cancel *context.CancelFunc
}

func newRunnerFixture(t *testing.T, p *pgEnv, store *postgresinfra.Store) *runnerFixture {
	t.Helper()
	a, err := app.Bootstrap(config.Config{})
	if err != nil {
		t.Fatal(err)
	}
	var cancel context.CancelFunc
	if err := a.NodeRegistry.RegisterNode(failNode{}, passDefinition("test.fail")); err != nil {
		t.Fatal(err)
	}
	if err := a.NodeRegistry.RegisterNode(cancelNode{cancel: &cancel}, passDefinition("test.cancel")); err != nil {
		t.Fatal(err)
	}
	r, err := execution.NewRunner(execution.RunnerConfig{
		Executions:     postgresinfra.NewExecutionRepository(store),
		NodeExecutions: postgresinfra.NewNodeExecutionRepository(store),
		Definitions:    postgresinfra.NewWorkflowVersionRepository(store),
		Validator:      workflow.NewValidator(a.NodeRegistry),
		Graph:          execution.NewGraphExecutor(a.NodeRegistry),
	})
	if err != nil {
		t.Fatal(err)
	}
	return &runnerFixture{pgEnv: p, runner: r, cancel: &cancel}
}

func wfDef(middle workflow.Node) workflow.Definition {
	pos := &workflow.Position{}
	return workflow.Definition{
		Version: workflow.DefinitionSchemaVersion,
		Nodes: []workflow.Node{
			{ID: "in", Type: "input", Name: "in", Position: pos, Config: map[string]any{}},
			middle,
			{ID: "shape", Type: "json", Name: "shape", Position: pos, Config: map[string]any{}},
			{ID: "out", Type: "output", Name: "out", Position: pos, Config: map[string]any{}},
		},
		Edges: []workflow.Edge{
			{ID: "e1", Source: "in", SourcePort: "data", Target: middle.ID, TargetPort: "input"},
			{ID: "e2", Source: middle.ID, SourcePort: "output", Target: "shape", TargetPort: "input"},
			{ID: "e3", Source: "shape", SourcePort: "output", Target: "out", TargetPort: "value"},
		},
		Settings: map[string]any{},
	}
}

func transformNode(expr string) workflow.Node {
	return workflow.Node{ID: "mid", Type: "transform", Name: "mid", Position: &workflow.Position{}, Config: map[string]any{"expression": expr}}
}

func (f *runnerFixture) nodesByID(t *testing.T, id uuid.UUID) map[string]execution.NodeExecution {
	t.Helper()
	list, err := f.nodes.ListByExecution(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]execution.NodeExecution{}
	for _, n := range list {
		out[n.NodeID] = n
	}
	return out
}

func TestPGRunnerCompletesExactVersionAndRecordsNodes(t *testing.T) {
	p := newPGEnv(t)
	f := newRunnerFixture(t, p, p.store)
	ctx := context.Background()
	wf, v1 := p.seedWorkflow(t, wfDef(transformNode("{{input.query}}")))
	e, err := f.runner.Service().Create(ctx, wf, v1, map[string]any{"query": "What is a DAG?"})
	if err != nil {
		t.Fatal(err)
	}
	// A newer version is added after creation; the run must use version 1.
	v2def, _ := json.Marshal(wfDef(workflow.Node{ID: "mid", Type: "test.fail", Name: "mid", Position: &workflow.Position{}, Config: map[string]any{}}))
	mustExec(t, p.raw, "INSERT INTO workflow_versions (id, workflow_id, version_number, definition, status) VALUES ($1, $2, 2, $3, 'PUBLISHED')", uuid.New(), wf, v2def)

	if _, err := f.runner.Run(ctx, e.ID); err != nil {
		t.Fatalf("run: %v", err)
	}
	got := p.get(t, e.ID)
	if got.Status != execution.StatusCompleted || got.WorkflowVersionID != v1 || got.FinishedAt == nil {
		t.Fatalf("execution = %+v", got)
	}
	result, _ := got.Output["out"].(map[string]any)
	if mustJSON(t, result["result"]) != `{"query":"What is a DAG?"}` {
		t.Fatalf("output = %s", mustJSON(t, got.Output))
	}
	recs := f.nodesByID(t, e.ID)
	for _, id := range []string{"in", "mid", "shape", "out"} {
		n, ok := recs[id]
		if !ok || n.Status != execution.NodeStatusCompleted || n.StartedAt == nil || n.FinishedAt == nil || n.Output == nil {
			t.Fatalf("node %s = %+v", id, n)
		}
	}
	cfg, _ := recs["mid"].Input["config"].(map[string]any)
	if cfg["expression"] != "What is a DAG?" || recs["mid"].NodeType != "transform" {
		t.Fatalf("mid runtime input = %#v", recs["mid"].Input)
	}
	if h := historyPairs(p.history(t, e.ID)); !sameStrings(h, []string{"NULL->PENDING", "PENDING->RUNNING", "RUNNING->COMPLETED"}) {
		t.Fatalf("history = %v", h)
	}
}

func TestPGRunnerFailurePersistsNodeErrorDetails(t *testing.T) {
	p := newPGEnv(t)
	f := newRunnerFixture(t, p, p.store)
	ctx := context.Background()
	wf, v := p.seedWorkflow(t, wfDef(workflow.Node{ID: "mid", Type: "test.fail", Name: "mid", Position: &workflow.Position{}, Config: map[string]any{}}))
	e, _ := f.runner.Service().Create(ctx, wf, v, map[string]any{})
	if _, err := f.runner.Run(ctx, e.ID); err == nil {
		t.Fatal("expected failure")
	}
	got := p.get(t, e.ID)
	if got.Status != execution.StatusFailed || got.Error == nil || got.Error.Code != string(node.ErrCodeRateLimited) ||
		got.Error.NodeID == nil || *got.Error.NodeID != "mid" || !got.Error.Retryable || got.Output != nil {
		t.Fatalf("execution = %+v / %+v", got, got.Error)
	}
	recs := f.nodesByID(t, e.ID)
	if recs["in"].Status != execution.NodeStatusCompleted || recs["mid"].Status != execution.NodeStatusFailed || recs["mid"].Error == nil {
		t.Fatalf("records = %+v", recs)
	}
	if _, ran := recs["shape"]; ran {
		t.Fatal("fail-fast: downstream node recorded")
	}

	// Runtime variable failure is also FAILED with the node captured.
	wf2, v2 := p.seedWorkflow(t, wfDef(transformNode("{{input.missing}}")))
	e2, _ := f.runner.Service().Create(ctx, wf2, v2, map[string]any{})
	_, _ = f.runner.Run(ctx, e2.ID)
	got2 := p.get(t, e2.ID)
	if got2.Status != execution.StatusFailed || got2.Error.Code != execution.CodeVariableResolution || *got2.Error.NodeID != "mid" {
		t.Fatalf("variable failure = %+v", got2.Error)
	}
}

func TestPGRunnerCancellationPersistsCancelled(t *testing.T) {
	p := newPGEnv(t)
	f := newRunnerFixture(t, p, p.store)
	wf, v := p.seedWorkflow(t, wfDef(workflow.Node{ID: "mid", Type: "test.cancel", Name: "mid", Position: &workflow.Position{}, Config: map[string]any{}}))
	e, _ := f.runner.Service().Create(context.Background(), wf, v, map[string]any{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	*f.cancel = cancel
	if _, err := f.runner.Run(ctx, e.ID); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	got := p.get(t, e.ID)
	if got.Status != execution.StatusCancelled || got.FinishedAt == nil {
		t.Fatalf("execution = %+v", got)
	}
	recs := f.nodesByID(t, e.ID)
	if recs["mid"].Status != execution.NodeStatusCompleted {
		t.Fatalf("mid = %+v", recs["mid"])
	}
	if _, ran := recs["shape"]; ran {
		t.Fatal("work scheduled after cancellation")
	}
	if h := historyPairs(p.history(t, e.ID)); !sameStrings(h, []string{"NULL->PENDING", "PENDING->RUNNING", "RUNNING->CANCELLED"}) {
		t.Fatalf("history = %v", h)
	}
}

// Two runners on separate pools race to run the same execution: exactly one
// executes the graph, and every node is recorded exactly once.
func TestPGConcurrentRunnersExecuteOnce(t *testing.T) {
	p := newPGEnv(t)
	fA := newRunnerFixture(t, p, openStore(t, p.url))
	fB := newRunnerFixture(t, p, openStore(t, p.url))
	for i := 0; i < 20; i++ {
		wf, v := p.seedWorkflow(t, wfDef(transformNode("x")))
		e, _ := fA.runner.Service().Create(context.Background(), wf, v, map[string]any{"i": float64(i)})
		start := make(chan struct{})
		errs := make(chan error, 2)
		for _, f := range []*runnerFixture{fA, fB} {
			go func(f *runnerFixture) {
				<-start
				_, err := f.runner.Run(context.Background(), e.ID)
				errs <- err
			}(f)
		}
		close(start)
		ok, lost := 0, 0
		for j := 0; j < 2; j++ {
			err := <-errs
			switch {
			case err == nil:
				ok++
			case errors.Is(err, execution.ErrExecutionNotClaimable):
				lost++
			default:
				t.Fatalf("run %d: %v", i, err)
			}
		}
		if ok != 1 || lost != 1 {
			t.Fatalf("run %d: ok=%d lost=%d", i, ok, lost)
		}
		var nodeRecords int
		_ = p.raw.QueryRow(context.Background(), "SELECT count(*) FROM node_executions WHERE execution_id=$1", e.ID).Scan(&nodeRecords)
		if nodeRecords != 4 || p.get(t, e.ID).Status != execution.StatusCompleted {
			t.Fatalf("run %d: node records = %d status = %s", i, nodeRecords, p.get(t, e.ID).Status)
		}
	}
}
