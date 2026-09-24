package queue_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"workflow-optimizer/internal/queue"
)

func TestMemoryQueueFIFO(t *testing.T) {
	q := queue.NewMemoryQueue()
	ctx := context.Background()
	ids := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}
	for _, id := range ids {
		if err := q.Enqueue(ctx, queue.Job{ExecutionID: id}); err != nil {
			t.Fatal(err)
		}
	}
	for i, id := range ids {
		job, err := q.Dequeue(ctx)
		if err != nil || job.ExecutionID != id {
			t.Fatalf("dequeue %d = %v, %v", i, job, err)
		}
	}
}

func TestMemoryQueueEmptyBlocksUntilContextDone(t *testing.T) {
	q := queue.NewMemoryQueue()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := q.Dequeue(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) < 40*time.Millisecond {
		t.Fatal("Dequeue on an empty queue must block until ctx ends")
	}
}

func TestMemoryQueueCancellationUnblocksConsumer(t *testing.T) {
	q := queue.NewMemoryQueue()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := q.Dequeue(ctx); done <- err }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancel did not unblock Dequeue")
	}
}

func TestMemoryQueueWakesBlockedConsumer(t *testing.T) {
	q := queue.NewMemoryQueue()
	id := uuid.New()
	got := make(chan queue.Job, 1)
	go func() { j, _ := q.Dequeue(context.Background()); got <- j }()
	time.Sleep(20 * time.Millisecond)
	_ = q.Enqueue(context.Background(), queue.Job{ExecutionID: id})
	select {
	case j := <-got:
		if j.ExecutionID != id {
			t.Fatalf("got %v", j)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked consumer was not woken")
	}
}

func TestMemoryQueueMalformedPayloadThenContinues(t *testing.T) {
	q := queue.NewMemoryQueue()
	_ = q.PushRaw([]byte(`{"garbage":true}`))
	id := uuid.New()
	_ = q.Enqueue(context.Background(), queue.Job{ExecutionID: id})
	if _, err := q.Dequeue(context.Background()); !errors.Is(err, queue.ErrMalformedJob) {
		t.Fatalf("err = %v", err)
	}
	if j, err := q.Dequeue(context.Background()); err != nil || j.ExecutionID != id {
		t.Fatalf("next = %v, %v", j, err)
	}
}

func TestMemoryQueueRejectsInvalidAndCancelledEnqueue(t *testing.T) {
	q := queue.NewMemoryQueue()
	if err := q.Enqueue(context.Background(), queue.Job{}); !errors.Is(err, queue.ErrInvalidJob) {
		t.Fatalf("err = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := q.Enqueue(ctx, queue.Job{ExecutionID: uuid.New()}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if q.Len() != 0 {
		t.Fatal("rejected jobs must not be queued")
	}
}

func TestMemoryQueueClose(t *testing.T) {
	q := queue.NewMemoryQueue()
	done := make(chan error, 1)
	go func() { _, err := q.Dequeue(context.Background()); done <- err }()
	time.Sleep(20 * time.Millisecond)
	q.Close()
	if err := <-done; !errors.Is(err, queue.ErrQueueClosed) {
		t.Fatalf("err = %v", err)
	}
	if err := q.Enqueue(context.Background(), queue.Job{ExecutionID: uuid.New()}); !errors.Is(err, queue.ErrQueueClosed) {
		t.Fatalf("enqueue after close = %v", err)
	}
}

// Concurrent producers and consumers: every job is delivered exactly once by
// the queue itself (duplicates in real systems come from re-enqueueing).
func TestMemoryQueueConcurrentDelivery(t *testing.T) {
	q := queue.NewMemoryQueue()
	const n = 500
	var wg sync.WaitGroup
	for p := 0; p < 5; p++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < n/5; i++ {
				_ = q.Enqueue(context.Background(), queue.Job{ExecutionID: uuid.New()})
			}
		}()
	}
	seen := make(chan uuid.UUID, n)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for c := 0; c < 5; c++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < n/5; i++ {
				j, err := q.Dequeue(ctx)
				if err != nil {
					return
				}
				seen <- j.ExecutionID
			}
		}()
	}
	wg.Wait()
	close(seen)
	unique := map[uuid.UUID]bool{}
	for id := range seen {
		if unique[id] {
			t.Fatalf("job %s delivered twice", id)
		}
		unique[id] = true
	}
	if len(unique) != n {
		t.Fatalf("delivered %d of %d", len(unique), n)
	}
}
