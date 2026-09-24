package redis_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	goredis "github.com/redis/go-redis/v9"

	redisinfra "workflow-optimizer/internal/infrastructure/redis"
	"workflow-optimizer/internal/queue"
)

// These tests run against a real Redis server named by TEST_REDIS_URL
// (for example redis://127.0.0.1:6379/0). They are skipped when it is unset.
// Every test uses its own key under test:phase9: and deletes it afterwards,
// so unrelated Redis data is never touched.

func redisURL(t *testing.T) string {
	t.Helper()
	url := os.Getenv("TEST_REDIS_URL")
	if url == "" {
		t.Skip("TEST_REDIS_URL not set; skipping Redis integration test")
	}
	return url
}

// testQueue opens a client and a queue on a unique key, plus a raw go-redis
// client for inspecting and seeding the list directly.
func testQueue(t *testing.T, poll time.Duration) (*redisinfra.Client, *redisinfra.Queue, *goredis.Client) {
	t.Helper()
	url := redisURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := redisinfra.Open(ctx, url)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	key := "test:phase9:" + uuid.NewString()
	q, err := redisinfra.NewQueue(c, key, poll)
	if err != nil {
		t.Fatal(err)
	}
	opts, err := goredis.ParseURL(url)
	if err != nil {
		t.Fatal(err)
	}
	raw := goredis.NewClient(opts)
	t.Cleanup(func() {
		_ = raw.Del(context.Background(), key).Err()
		_ = raw.Close()
		_ = c.Close()
	})
	return c, q, raw
}

func TestRedisOpenPingClose(t *testing.T) {
	url := redisURL(t)
	c, err := redisinfra.Open(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Ping(context.Background()); err != nil {
		t.Fatalf("ping: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("second close must be a no-op: %v", err)
	}
	if err := c.Ping(context.Background()); !errors.Is(err, redisinfra.ErrUnavailable) {
		t.Fatalf("ping after close = %v", err)
	}
}

func TestRedisOpenRejectsBadConfiguration(t *testing.T) {
	redisURL(t)
	if _, err := redisinfra.Open(context.Background(), ""); !errors.Is(err, redisinfra.ErrUnavailable) {
		t.Fatalf("empty url: %v", err)
	}
	if _, err := redisinfra.Open(context.Background(), "http://not-redis"); err == nil {
		t.Fatal("non-redis URL must be rejected")
	}
	// Port 1 on loopback: nothing listens there.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := redisinfra.Open(ctx, "redis://127.0.0.1:1/0"); !errors.Is(err, redisinfra.ErrUnavailable) {
		t.Fatalf("unreachable: %v", err)
	}
}

func TestRedisQueueRoundTripFIFO(t *testing.T) {
	_, q, raw := testQueue(t, 0)
	ctx := context.Background()
	ids := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}
	for _, id := range ids {
		if err := q.Enqueue(ctx, queue.Job{ExecutionID: id}); err != nil {
			t.Fatal(err)
		}
	}
	// The stored payload is exactly the small identity-only JSON.
	vals, err := raw.LRange(ctx, q.Name(), 0, -1).Result()
	if err != nil {
		t.Fatal(err)
	}
	if len(vals) != 3 {
		t.Fatalf("list length = %d", len(vals))
	}
	want := `{"v":1,"execution_id":"` + ids[0].String() + `"}`
	if vals[2] != want { // LPUSH puts the oldest at the tail
		t.Fatalf("stored payload = %s, want %s", vals[2], want)
	}
	for i, id := range ids {
		j, err := q.Dequeue(ctx)
		if err != nil || j.ExecutionID != id {
			t.Fatalf("dequeue %d = %v, %v", i, j, err)
		}
	}
	if n, _ := raw.LLen(ctx, q.Name()).Result(); n != 0 {
		t.Fatalf("list not drained: %d", n)
	}
}

func TestRedisQueueMalformedPayloadThenContinues(t *testing.T) {
	_, q, raw := testQueue(t, 0)
	ctx := context.Background()
	if err := raw.LPush(ctx, q.Name(), `{"not":"a job"}`).Err(); err != nil {
		t.Fatal(err)
	}
	id := uuid.New()
	if err := q.Enqueue(ctx, queue.Job{ExecutionID: id}); err != nil {
		t.Fatal(err)
	}
	_, err := q.Dequeue(ctx)
	var mal *queue.MalformedJobError
	if !errors.As(err, &mal) || string(mal.Payload) != `{"not":"a job"}` {
		t.Fatalf("err = %v", err)
	}
	if j, err := q.Dequeue(ctx); err != nil || j.ExecutionID != id {
		t.Fatalf("next = %v, %v", j, err)
	}
}

func TestRedisQueueEmptyDeadlineReturnsPromptly(t *testing.T) {
	_, q, _ := testQueue(t, 0)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := q.Dequeue(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
	// Bounded by one poll interval plus slack.
	if d := time.Since(start); d > redisinfra.MinPollInterval+time.Second {
		t.Fatalf("Dequeue took %v", d)
	}
}

func TestRedisQueueCancelUnblocksWithinPollInterval(t *testing.T) {
	_, q, _ := testQueue(t, 0)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := q.Dequeue(ctx); done <- err }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(redisinfra.DefaultPollInterval + time.Second):
		t.Fatal("cancel did not unblock Dequeue within one poll interval")
	}
}

func TestRedisQueueBlockedConsumerIsWoken(t *testing.T) {
	_, q, _ := testQueue(t, time.Second)
	id := uuid.New()
	got := make(chan queue.Job, 1)
	go func() {
		j, _ := q.Dequeue(context.Background())
		got <- j
	}()
	time.Sleep(50 * time.Millisecond)
	if err := q.Enqueue(context.Background(), queue.Job{ExecutionID: id}); err != nil {
		t.Fatal(err)
	}
	select {
	case j := <-got:
		if j.ExecutionID != id {
			t.Fatalf("got %v", j)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("blocked consumer was not woken")
	}
}

func TestRedisQueueAfterCloseFails(t *testing.T) {
	c, q, _ := testQueue(t, 0)
	_ = c.Close()
	if err := q.Enqueue(context.Background(), queue.Job{ExecutionID: uuid.New()}); !errors.Is(err, redisinfra.ErrUnavailable) {
		t.Fatalf("enqueue after close = %v", err)
	}
	if _, err := q.Dequeue(context.Background()); !errors.Is(err, redisinfra.ErrUnavailable) {
		t.Fatalf("dequeue after close = %v", err)
	}
}

func TestRedisQueueValidation(t *testing.T) {
	c, q, raw := testQueue(t, 0)
	if _, err := redisinfra.NewQueue(c, "  ", 0); err == nil {
		t.Fatal("empty name must be rejected")
	}
	if _, err := redisinfra.NewQueue(nil, "x", 0); err == nil {
		t.Fatal("nil client must be rejected")
	}
	if err := q.Enqueue(context.Background(), queue.Job{}); !errors.Is(err, queue.ErrInvalidJob) {
		t.Fatalf("invalid job: %v", err)
	}
	if n, _ := raw.LLen(context.Background(), q.Name()).Result(); n != 0 {
		t.Fatal("invalid job must not be pushed")
	}
}
