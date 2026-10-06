package redis

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"workflow-optimizer/internal/queue"
)

// DefaultPollInterval is how long one BRPOP blocks before the consumer
// re-checks its context. It bounds how long cancellation takes to be noticed.
const DefaultPollInterval = time.Second

// MinPollInterval is the shortest BRPOP block the Redis client supports
// (go-redis truncates shorter timeouts to one second).
const MinPollInterval = time.Second

// Queue is the Redis implementation of queue.JobQueue: a Redis list, filled
// with LPUSH and consumed with BRPOP, so jobs are processed FIFO.
//
// Delivery limitation (by design in Phase 9): BRPOP removes the job before the
// worker processes it. If a worker process dies after BRPOP and before it
// claims the execution, the job is lost and the execution stays PENDING until
// it is dispatched again. There is no acknowledgement, visibility timeout,
// retry or dead-letter mechanism.
type Queue struct {
	client       *Client
	name         string
	pollInterval time.Duration
}

var _ queue.JobQueue = (*Queue)(nil)

// NewQueue returns a queue over the list named name (see
// config.DefaultRedisQueueName). pollInterval <= 0 uses DefaultPollInterval;
// values below MinPollInterval are raised to it.
func NewQueue(client *Client, name string, pollInterval time.Duration) (*Queue, error) {
	if client == nil {
		return nil, errors.New("redis: queue requires a client")
	}
	if strings.TrimSpace(name) == "" {
		return nil, errors.New("redis: queue name must not be empty")
	}
	if pollInterval <= 0 {
		pollInterval = DefaultPollInterval
	}
	if pollInterval < MinPollInterval {
		pollInterval = MinPollInterval
	}
	return &Queue{client: client, name: name, pollInterval: pollInterval}, nil
}

// Name returns the Redis key of the list.
func (q *Queue) Name() string { return q.name }

// Enqueue serializes the job and LPUSHes it.
func (q *Queue) Enqueue(ctx context.Context, job queue.Job) error {
	payload, err := queue.EncodeJob(job)
	if err != nil {
		return err
	}
	if err := q.client.rdb.LPush(ctx, q.name, payload).Err(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return fmt.Errorf("%w: enqueue on %q: %w", ErrUnavailable, q.name, err)
	}
	return nil
}

// Dequeue blocks with BRPOP until a job is available or ctx is done.
//
// Each BRPOP runs on a context detached from ctx's cancellation and blocks for
// at most pollInterval (the client adds that to its read timeout, so the call
// is bounded). ctx is checked between calls. An in-flight BRPOP is therefore
// never abandoned client-side: a job Redis already removed is always returned
// to the caller, never silently discarded by a cancelled read.
func (q *Queue) Dequeue(ctx context.Context) (queue.Job, error) {
	for {
		if err := ctx.Err(); err != nil {
			return queue.Job{}, err
		}
		res, err := q.client.rdb.BRPop(context.WithoutCancel(ctx), q.pollInterval, q.name).Result()
		if errors.Is(err, goredis.Nil) {
			continue // timed out with the list empty; re-check ctx
		}
		if err != nil {
			return queue.Job{}, fmt.Errorf("%w: dequeue from %q: %w", ErrUnavailable, q.name, err)
		}
		if len(res) != 2 {
			return queue.Job{}, fmt.Errorf("%w: unexpected BRPOP reply of %d elements", ErrUnavailable, len(res))
		}
		return queue.DecodeJob([]byte(res[1]))
	}
}

// List is a plain Redis list used for notices (e.g. the dead-letter list).
// Nothing consumes it as work: it is not a queue.JobQueue.
type List struct {
	client *Client
	name   string
}

// NewList returns a list handle for key name.
func NewList(client *Client, name string) (*List, error) {
	if client == nil {
		return nil, errors.New("redis: list requires a client")
	}
	if strings.TrimSpace(name) == "" {
		return nil, errors.New("redis: list name must not be empty")
	}
	return &List{client: client, name: name}, nil
}

// Name returns the Redis key of the list.
func (l *List) Name() string { return l.name }

// Push LPUSHes payload.
func (l *List) Push(ctx context.Context, payload []byte) error {
	if err := l.client.rdb.LPush(ctx, l.name, payload).Err(); err != nil {
		return fmt.Errorf("%w: push to %q: %w", ErrUnavailable, l.name, err)
	}
	return nil
}
