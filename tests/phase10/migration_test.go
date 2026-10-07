package phase10_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	migratepostgres "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/google/uuid"

	"workflow-optimizer/internal/execution"
)

// Migration 000003 rolls back to 000002 without losing Phase 10 data and a
// re-applied UP restores it: attempts, limits, deadlines, retry schedules,
// last errors, per-attempt history, dead letters and cancellation requests.
// A retry that was waiting before the round trip is still dispatched and
// completes afterwards.
func TestMigration000003RoundTripPreservesReliabilityData(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	waiting, wkey := e.create(t, 3, time.Minute, "503", "ok")
	if _, err := e.runner(t, runnerOpts{backoff: fast}).Run(ctx, waiting); !errors.As(err, new(*execution.RetryScheduledError)) {
		t.Fatal(err)
	}
	dead, _ := e.create(t, 1, time.Minute, "503")
	if _, err := e.runner(t, runnerOpts{backoff: fast}).Run(ctx, dead); !errors.As(err, new(*execution.DeadLetteredError)) {
		t.Fatal(err)
	}
	cancelled, _ := e.create(t, 3, time.Minute)
	if err := e.rel.RequestCancel(ctx, cancelled); err != nil {
		t.Fatal(err)
	}
	before := map[string]execution.Execution{"waiting": e.get(t, waiting), "dead": e.get(t, dead), "cancelled": e.get(t, cancelled)}
	historyBefore := e.history(t, waiting)
	dlBefore, _ := e.rel.DeadLetter(ctx, dead)

	db, err := sql.Open("pgx", e.dbURL)
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
	if err := m.Migrate(2); err != nil {
		t.Fatalf("DOWN 000003: %v", err)
	}
	if v, dirty, _ := m.Version(); v != 2 || dirty {
		t.Fatalf("after down: %d dirty=%v", v, dirty)
	}
	if n := e.count(t, "SELECT count(*) FROM executions WHERE id = ANY(ARRAY[$1, $2, $3]::uuid[])", waiting, dead, cancelled); n != 3 {
		t.Fatal("executions lost by DOWN")
	}
	if n := e.count(t, "SELECT count(*) FROM phase10_rollback_executions"); n < 3 {
		t.Fatal("phase 10 columns not archived")
	}

	if err := m.Up(); err != nil {
		t.Fatalf("UP 000003 again: %v", err)
	}
	for name, id := range map[string]execution.Execution{"waiting": e.get(t, waiting), "dead": e.get(t, dead), "cancelled": e.get(t, cancelled)} {
		b := before[name]
		if id.Status != b.Status || id.Attempt != b.Attempt || id.MaxAttempts != b.MaxAttempts || id.Timeout != b.Timeout ||
			!sameTime(id.DeadlineAt, b.DeadlineAt) || !sameTime(id.NextAttemptAt, b.NextAttemptAt) ||
			(id.LastError == nil) != (b.LastError == nil) || id.CancelRequested != b.CancelRequested {
			t.Fatalf("%s after round trip: %+v\nbefore: %+v", name, id, b)
		}
	}
	if got := e.history(t, waiting); !equal(got, historyBefore) {
		t.Fatalf("history after round trip = %v, before %v", got, historyBefore)
	}
	dlAfter, _ := e.rel.DeadLetter(ctx, dead)
	if dlAfter == nil || dlAfter.ID != dlBefore.ID || dlAfter.Reason != dlBefore.Reason {
		t.Fatalf("dead letter after round trip = %+v", dlAfter)
	}
	if n := e.count(t, "SELECT count(*) FROM pg_tables WHERE schemaname = current_schema() AND tablename LIKE 'phase10_rollback_%'"); n != 0 {
		t.Fatalf("%d archive tables left", n)
	}

	// The waiting retry is dispatchable again and completes.
	time.Sleep(time.Until(*before["waiting"].NextAttemptAt) + 20*time.Millisecond)
	due, err := e.rel.ClaimDue(ctx, 10, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, d := range due {
		found = found || d.ExecutionID == waiting
	}
	if !found {
		t.Fatalf("waiting retry not dispatchable after round trip: %+v", due)
	}
	if _, err := e.runner(t, runnerOpts{backoff: fast}).Run(ctx, waiting); err != nil {
		t.Fatal(err)
	}
	if ex := e.get(t, waiting); ex.Status != execution.StatusCompleted || ex.Attempt != 2 || e.s.preCount(wkey) != 1 {
		t.Fatalf("after resume: %+v", ex)
	}
}

// Rollback with a workload: an execution waiting for a retry is rolled back
// to the Phase 8 schema (000002), where it must be an ordinary PENDING
// execution that Phase 8 can claim (its history may not already hold a
// PENDING -> RUNNING record, which 000002 allows once per execution). Phase 8
// runs it to completion; UP 000003/000004 then yields a valid Phase 10 state:
// the archived retry history does not resurface over the newer Phase 8
// history, no archive tables remain, and new executions run normally.
func TestRollbackWithWaitingRetryIsClaimableByPhase8(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	waiting, _ := e.create(t, 3, time.Minute, "503", "ok")
	if _, err := e.runner(t, runnerOpts{backoff: fast}).Run(ctx, waiting); !errors.As(err, new(*execution.RetryScheduledError)) {
		t.Fatal(err)
	}
	untouched, ukey := e.create(t, 3, time.Minute, "503", "ok")
	if _, err := e.runner(t, runnerOpts{backoff: fast}).Run(ctx, untouched); !errors.As(err, new(*execution.RetryScheduledError)) {
		t.Fatal(err)
	}
	untouchedHistory := e.history(t, untouched)

	m := e.migrator(t)
	if err := m.Migrate(2); err != nil {
		t.Fatalf("DOWN to 000002: %v", err)
	}
	// Valid 000002 state: PENDING with only its creation record.
	if got := e.rawHistory(t, waiting); !equal(got, []string{"NULL->PENDING"}) {
		t.Fatalf("history after DOWN = %v", got)
	}
	var status string
	_ = e.raw.QueryRow(ctx, "SELECT status FROM executions WHERE id = $1", waiting).Scan(&status)
	if status != "PENDING" {
		t.Fatalf("status after DOWN = %s", status)
	}
	// Phase 8 claims it (what 000002's Start does) and completes it.
	if _, err := e.raw.Exec(ctx, "UPDATE executions SET status = 'RUNNING', claim_token = $2 WHERE id = $1 AND status = 'PENDING'", waiting, uuid.New()); err != nil {
		t.Fatalf("Phase 8 claim after rollback: %v", err)
	}
	if _, err := e.raw.Exec(ctx, "UPDATE executions SET status = 'COMPLETED', output = '{}' WHERE id = $1", waiting); err != nil {
		t.Fatalf("Phase 8 completion after rollback: %v", err)
	}
	if got := e.rawHistory(t, waiting); !equal(got, []string{"NULL->PENDING", "PENDING->RUNNING", "RUNNING->COMPLETED"}) {
		t.Fatalf("Phase 8 history = %v", got)
	}

	if err := m.Up(); err != nil {
		t.Fatalf("UP again: %v", err)
	}
	if v, dirty, _ := m.Version(); v != 6 || dirty { // head: 000006 (Phase 14)
		t.Fatalf("after up: %d dirty=%v", v, dirty)
	}
	got := e.get(t, waiting)
	if got.Status != execution.StatusCompleted || got.Attempt != 1 {
		t.Fatalf("after UP: %+v", got)
	}
	if h := e.history(t, waiting); !equal(h, []string{"NULL->PENDING@0", "PENDING->RUNNING@1", "RUNNING->COMPLETED@1"}) {
		t.Fatalf("history after UP = %v", h)
	}
	if n := e.count(t, "SELECT count(*) FROM pg_tables WHERE schemaname = current_schema() AND tablename LIKE 'phase10_rollback_%'"); n != 0 {
		t.Fatalf("%d archive tables left", n)
	}
	// The execution left alone while rolled back is restored and resumes.
	if h := e.history(t, untouched); !equal(h, untouchedHistory) {
		t.Fatalf("untouched history = %v, before %v", h, untouchedHistory)
	}
	e.waitRetryDue(t, untouched)
	if _, err := e.runner(t, runnerOpts{backoff: fast}).Run(ctx, untouched); err != nil {
		t.Fatal(err)
	}
	if got := e.get(t, untouched); got.Status != execution.StatusCompleted || got.Attempt != 2 || e.s.preCount(ukey) != 1 {
		t.Fatalf("untouched after resume: %+v", got)
	}
	// New work runs on the restored schema.
	fresh, _ := e.create(t, 3, time.Minute, "ok")
	if _, err := e.runner(t, runnerOpts{backoff: fast}).Run(ctx, fresh); err != nil {
		t.Fatal(err)
	}
}

func (e *env) migrator(t *testing.T) *migrate.Migrate {
	t.Helper()
	db, err := sql.Open("pgx", e.dbURL)
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
	return m
}

// rawHistory reads history without the Phase 10 attempt column (it works on
// the 000002 schema too).
func (e *env) rawHistory(t *testing.T, id uuid.UUID) []string {
	t.Helper()
	rows, err := e.raw.Query(context.Background(), `
SELECT COALESCE(from_status, 'NULL') || '->' || to_status FROM execution_status_history
 WHERE execution_id = $1
 ORDER BY created_at, CASE to_status WHEN 'PENDING' THEN 0 WHEN 'RUNNING' THEN 1 ELSE 2 END`, id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

func sameTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}
