package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/google/uuid"

	"workflow-optimizer/internal/execution"
	postgresinfra "workflow-optimizer/internal/infrastructure/postgres"
)

func (p *pgEnv) newExecution(t *testing.T) uuid.UUID {
	t.Helper()
	wf, v := p.seedWorkflow(t, nil)
	e, err := p.svc.Create(context.Background(), wf, v, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	return e.ID
}

func TestPGExecutionEventsAppendOrderAndPagination(t *testing.T) {
	p := newPGEnv(t)
	ctx := context.Background()
	repo := postgresinfra.NewExecutionEventRepository(p.store)
	a, b := p.newExecution(t), p.newExecution(t)

	node := "llm_1"
	types := []execution.EventType{execution.EventExecutionStarted, execution.EventNodeStarted, execution.EventNodeFailed,
		execution.EventRetryScheduled, execution.EventRetryStarted, execution.EventNodeStarted, execution.EventNodeCompleted, execution.EventExecutionCompleted}
	for i, typ := range types {
		ev := execution.ExecutionEvent{ExecutionID: a, Type: typ, Data: map[string]any{"i": i}}
		if typ != execution.EventExecutionStarted && typ != execution.EventExecutionCompleted {
			ev.NodeID = &node
		}
		stored, err := repo.Append(ctx, ev)
		if err != nil {
			t.Fatal(err)
		}
		if stored.ID == uuid.Nil || stored.Timestamp.IsZero() || stored.Type != typ {
			t.Fatalf("stored = %+v", stored)
		}
	}
	if _, err := repo.Append(ctx, execution.ExecutionEvent{ExecutionID: b, Type: execution.EventExecutionStarted}); err != nil {
		t.Fatal(err)
	}

	// Chronological, scoped to the execution, stable across pages.
	all, total, err := repo.ListByExecution(ctx, a, execution.EventQuery{Page: 1, PageSize: 100})
	if err != nil || total != len(types) || len(all) != len(types) {
		t.Fatalf("list = %d/%d %v", len(all), total, err)
	}
	for i, e := range all {
		if e.Type != types[i] || e.ExecutionID != a || e.Data["i"] != float64(i) {
			t.Fatalf("event %d = %+v", i, e)
		}
		if i > 0 && e.Timestamp.Before(all[i-1].Timestamp) {
			t.Fatal("timestamps out of order")
		}
	}
	if all[1].NodeID == nil || *all[1].NodeID != node || all[0].NodeID != nil {
		t.Fatalf("node ids = %v %v", all[0].NodeID, all[1].NodeID)
	}
	var paged []execution.ExecutionEvent
	for page := 1; page <= 3; page++ {
		got, total, err := repo.ListByExecution(ctx, a, execution.EventQuery{Page: page, PageSize: 3})
		if err != nil || total != len(types) {
			t.Fatal(err)
		}
		paged = append(paged, got...)
	}
	if len(paged) != len(all) {
		t.Fatalf("paged %d events", len(paged))
	}
	for i := range all {
		if paged[i].ID != all[i].ID {
			t.Fatalf("page order differs at %d", i)
		}
	}
	if other, total, _ := repo.ListByExecution(ctx, b, execution.EventQuery{Page: 1, PageSize: 50}); total != 1 || len(other) != 1 {
		t.Fatalf("execution b = %v", other)
	}
	if none, total, _ := repo.ListByExecution(ctx, uuid.New(), execution.EventQuery{}); total != 0 || len(none) != 0 {
		t.Fatal("events for an unknown execution")
	}
	// Unknown execution and unknown type are refused.
	if _, err := repo.Append(ctx, execution.ExecutionEvent{ExecutionID: uuid.New(), Type: execution.EventNodeStarted}); !errors.Is(err, execution.ErrExecutionNotFound) {
		t.Fatalf("unknown execution: %v", err)
	}
	if _, err := repo.Append(ctx, execution.ExecutionEvent{ExecutionID: a, Type: "PROVIDER_REQUEST"}); err == nil {
		t.Fatal("unknown event type stored")
	}
}

func TestPGExecutionEventsAreAppendOnly(t *testing.T) {
	p := newPGEnv(t)
	ctx := context.Background()
	repo := postgresinfra.NewExecutionEventRepository(p.store)
	ev, err := repo.Append(ctx, execution.ExecutionEvent{ExecutionID: p.newExecution(t), Type: execution.EventExecutionStarted})
	if err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		"UPDATE execution_events SET type = 'EXECUTION_FAILED' WHERE id = $1",
		"DELETE FROM execution_events WHERE id = $1",
	} {
		if _, err := p.raw.Exec(ctx, sql, ev.ID); err == nil {
			t.Fatalf("%s succeeded", sql)
		}
	}
	if _, err := p.raw.Exec(ctx, "TRUNCATE execution_events"); err == nil {
		t.Fatal("TRUNCATE succeeded")
	}
}

func TestPGExecutionEventsConcurrentAppends(t *testing.T) {
	p := newPGEnv(t)
	ctx := context.Background()
	repo := postgresinfra.NewExecutionEventRepository(p.store)
	id := p.newExecution(t)
	const writers, each = 8, 25
	var wg sync.WaitGroup
	errs := make(chan error, writers*each)
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				node := fmt.Sprintf("n%d", w)
				if _, err := repo.Append(ctx, execution.ExecutionEvent{ExecutionID: id, NodeID: &node, Type: execution.EventNodeStarted,
					Data: map[string]any{"seq": i}}); err != nil {
					errs <- err
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	all, total, err := repo.ListByExecution(ctx, id, execution.EventQuery{Page: 1, PageSize: 100})
	if err != nil || total != writers*each {
		t.Fatalf("total = %d %v", total, err)
	}
	// Each writer's own events keep their order.
	last := map[string]float64{}
	seen := map[uuid.UUID]bool{}
	for page := 1; page <= 2; page++ {
		got, _, _ := repo.ListByExecution(ctx, id, execution.EventQuery{Page: page, PageSize: 100})
		if page == 1 {
			all = got
		} else {
			all = append(all, got...)
		}
	}
	for _, e := range all {
		if seen[e.ID] {
			t.Fatal("duplicate across pages")
		}
		seen[e.ID] = true
		n, s := *e.NodeID, e.Data["seq"].(float64)
		if prev, ok := last[n]; ok && s <= prev {
			t.Fatalf("writer %s out of order: %v after %v", n, s, prev)
		}
		last[n] = s
	}
	if len(seen) != writers*each {
		t.Fatalf("%d distinct events", len(seen))
	}
}
