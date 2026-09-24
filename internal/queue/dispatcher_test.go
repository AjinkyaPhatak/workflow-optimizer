package queue_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"workflow-optimizer/internal/execution"
	"workflow-optimizer/internal/queue"
)

// fakeService records calls; it never executes anything.
type fakeService struct {
	createErr error
	created   []execution.Execution
	calls     []string
}

func (f *fakeService) Create(_ context.Context, wf, v uuid.UUID, input map[string]any) (execution.Execution, error) {
	f.calls = append(f.calls, "create")
	if f.createErr != nil {
		return execution.Execution{}, f.createErr
	}
	e := execution.Execution{ID: uuid.New(), WorkflowID: wf, WorkflowVersionID: v, Status: execution.StatusPending, Input: input}
	f.created = append(f.created, e)
	return e, nil
}
func (f *fakeService) Start(context.Context, uuid.UUID) error {
	f.calls = append(f.calls, "start")
	return nil
}
func (f *fakeService) Complete(context.Context, uuid.UUID, map[string]any) error {
	f.calls = append(f.calls, "complete")
	return nil
}
func (f *fakeService) Fail(context.Context, uuid.UUID, execution.ExecutionError) error {
	f.calls = append(f.calls, "fail")
	return nil
}
func (f *fakeService) Cancel(context.Context, uuid.UUID) error {
	f.calls = append(f.calls, "cancel")
	return nil
}

// failingQueue fails every Enqueue.
type failingQueue struct{ err error }

func (f failingQueue) Enqueue(context.Context, queue.Job) error { return f.err }
func (f failingQueue) Dequeue(context.Context) (queue.Job, error) {
	return queue.Job{}, f.err
}

func TestDispatcherEnqueuesOnlyTheExecutionID(t *testing.T) {
	q := queue.NewMemoryQueue()
	d, err := queue.NewDispatcher(q)
	if err != nil {
		t.Fatal(err)
	}
	id := uuid.New()
	if err := d.Dispatch(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	// Duplicate dispatch is allowed (the database claim de-duplicates).
	if err := d.Dispatch(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if j, err := q.Dequeue(context.Background()); err != nil || j.ExecutionID != id {
			t.Fatalf("job %d = %v, %v", i, j, err)
		}
	}
	if err := d.Dispatch(context.Background(), uuid.Nil); !errors.Is(err, queue.ErrInvalidJob) {
		t.Fatalf("nil id: %v", err)
	}
	if _, err := queue.NewDispatcher(nil); err == nil {
		t.Fatal("nil queue must be rejected")
	}
}

func TestSubmitterCreatesPendingThenDispatchesWithoutExecuting(t *testing.T) {
	svc := &fakeService{}
	q := queue.NewMemoryQueue()
	d, _ := queue.NewDispatcher(q)
	s, err := queue.NewSubmitter(svc, d)
	if err != nil {
		t.Fatal(err)
	}
	e, err := s.Submit(context.Background(), uuid.New(), uuid.New(), map[string]any{"q": 1})
	if err != nil {
		t.Fatal(err)
	}
	if e.Status != execution.StatusPending || len(svc.calls) != 1 || svc.calls[0] != "create" {
		t.Fatalf("request path must only create: calls=%v", svc.calls)
	}
	if j, _ := q.Dequeue(context.Background()); j.ExecutionID != e.ID {
		t.Fatalf("queued %v, want %v", j.ExecutionID, e.ID)
	}
}

func TestSubmitterCreateFailureEnqueuesNothing(t *testing.T) {
	boom := errors.New("db down")
	q := queue.NewMemoryQueue()
	d, _ := queue.NewDispatcher(q)
	s, _ := queue.NewSubmitter(&fakeService{createErr: boom}, d)
	if _, err := s.Submit(context.Background(), uuid.New(), uuid.New(), nil); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if q.Enqueued() != 0 {
		t.Fatal("nothing may be enqueued when creation fails")
	}
}

func TestSubmitterDispatchFailureReportsPersistedExecution(t *testing.T) {
	down := errors.New("redis down")
	svc := &fakeService{}
	d, _ := queue.NewDispatcher(failingQueue{err: down})
	s, _ := queue.NewSubmitter(svc, d)
	e, err := s.Submit(context.Background(), uuid.New(), uuid.New(), nil)
	var de *queue.DispatchError
	if !errors.As(err, &de) || !errors.Is(err, down) || de.ExecutionID != e.ID || e.ID == uuid.Nil {
		t.Fatalf("err = %v, execution = %+v", err, e)
	}
	if e.Status != execution.StatusPending {
		t.Fatalf("status = %s", e.Status)
	}
}
