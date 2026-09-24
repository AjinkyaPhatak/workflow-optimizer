package queue

import (
	"context"
	"sync"
)

// MemoryQueue is an in-process JobQueue with the same FIFO, encode/decode and
// blocking semantics as the Redis queue. It is intended for tests and
// single-process use; it provides no cross-process delivery.
//
// Jobs are stored encoded, so Dequeue exercises the same decoder (and the
// same malformed-payload behaviour) as the Redis implementation.
type MemoryQueue struct {
	mu       sync.Mutex
	items    [][]byte
	notify   chan struct{} // closed and replaced whenever an item is added
	closed   bool
	enqueues int
}

var _ JobQueue = (*MemoryQueue)(nil)

// NewMemoryQueue returns an empty queue.
func NewMemoryQueue() *MemoryQueue {
	return &MemoryQueue{notify: make(chan struct{})}
}

// Enqueue encodes and appends the job.
func (q *MemoryQueue) Enqueue(ctx context.Context, job Job) error {
	payload, err := EncodeJob(job)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return q.push(payload)
}

// PushRaw appends a raw payload without validation (to simulate corrupted
// or foreign messages).
func (q *MemoryQueue) PushRaw(payload []byte) error {
	return q.push(append([]byte(nil), payload...))
}

func (q *MemoryQueue) push(payload []byte) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return ErrQueueClosed
	}
	q.items = append(q.items, payload)
	q.enqueues++
	close(q.notify)
	q.notify = make(chan struct{})
	return nil
}

// Dequeue blocks until a job is available, ctx is done, or the queue closes.
func (q *MemoryQueue) Dequeue(ctx context.Context) (Job, error) {
	for {
		q.mu.Lock()
		if len(q.items) > 0 {
			payload := q.items[0]
			q.items = q.items[1:]
			q.mu.Unlock()
			return DecodeJob(payload)
		}
		if q.closed {
			q.mu.Unlock()
			return Job{}, ErrQueueClosed
		}
		wait := q.notify
		q.mu.Unlock()
		select {
		case <-ctx.Done():
			return Job{}, ctx.Err()
		case <-wait:
		}
	}
}

// Len reports the number of queued payloads.
func (q *MemoryQueue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items)
}

// Enqueued reports how many payloads were ever added.
func (q *MemoryQueue) Enqueued() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.enqueues
}

// Close wakes blocked consumers; later operations fail with ErrQueueClosed.
func (q *MemoryQueue) Close() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if !q.closed {
		q.closed = true
		close(q.notify)
	}
}
