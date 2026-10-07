package observability_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"workflow-optimizer/internal/execution"
	"workflow-optimizer/internal/observability"
)

const secret = "sk-test-SECRET-abcdefghijklmnop"

func TestRedaction(t *testing.T) {
	in := map[string]any{
		"api_key":       secret,
		"Authorization": "Bearer abc.def.ghijklmnop",
		"config": map[string]any{
			"credential_id": "c2c5b0b1-0000-4000-8000-000000000000",
			"password":      "hunter2",
			"X-Api-Key":     "k",
		},
		"ports":        map[string]any{"prompt": "my key is " + secret + " ok", "list": []any{"Bearer abcdefghijk123"}},
		"usage":        map[string]any{"total_tokens": float64(12), "input_tokens": float64(7)},
		"long":         strings.Repeat("x", observability.MaxStringLength+10),
		"retry_tokens": "fine",
	}
	before, _ := json.Marshal(in)
	out := observability.RedactMap(in)
	after, _ := json.Marshal(in)
	if !bytes.Equal(before, after) {
		t.Fatal("Redact mutated its input")
	}
	b, _ := json.Marshal(out)
	s := string(b)
	for _, leak := range []string{secret, "hunter2", "abc.def", "abcdefghijk123"} {
		if strings.Contains(s, leak) {
			t.Fatalf("leaked %q: %s", leak, s)
		}
	}
	cfg := out["config"].(map[string]any)
	if cfg["credential_id"] != "c2c5b0b1-0000-4000-8000-000000000000" || cfg["X-Api-Key"] != observability.Redacted {
		t.Fatalf("config = %v", cfg)
	}
	if out["usage"].(map[string]any)["total_tokens"] != float64(12) || out["retry_tokens"] != "fine" {
		t.Fatalf("non-secret fields altered: %v", out)
	}
	if !strings.HasSuffix(out["long"].(string), "[truncated]") || len(out["long"].(string)) > observability.MaxStringLength+20 {
		t.Fatal("long string not truncated")
	}
	if out["ports"].(map[string]any)["prompt"] != "my key is [REDACTED] ok" {
		t.Fatalf("prompt = %v", out["ports"])
	}
}

func TestPricing(t *testing.T) {
	p, err := observability.ParsePricing(`{"gpt-5":{"input_per_million":1.25,"output_per_million":10},"gpt-5-mini":{"input_per_million":0.25,"output_per_million":2}}`)
	if err != nil {
		t.Fatal(err)
	}
	if c := p.Estimate("gpt-5", 1_000_000, 100_000); c == nil || *c != 2.25 {
		t.Fatalf("gpt-5 = %v", c)
	}
	// Longest prefix wins for dated model names.
	if c := p.Estimate("gpt-5-mini-2025-08-07", 1_000_000, 0); c == nil || *c != 0.25 {
		t.Fatalf("prefix = %v", c)
	}
	if p.Estimate("other-model", 10, 10) != nil || p.Estimate("", 1, 1) != nil {
		t.Fatal("estimate without a price")
	}
	empty, _ := observability.ParsePricing("")
	if empty.Estimate("gpt-5", 1, 1) != nil {
		t.Fatal("estimate without configuration")
	}
	for _, bad := range []string{"{", `{"m":{"input_per_million":-1}}`} {
		if _, err := observability.ParsePricing(bad); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
}

type memEvents struct {
	mu     sync.Mutex
	events []execution.ExecutionEvent
	err    error
}

func (m *memEvents) Append(_ context.Context, e execution.ExecutionEvent) (execution.ExecutionEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return execution.ExecutionEvent{}, m.err
	}
	m.events = append(m.events, e)
	return e, nil
}

func (m *memEvents) ListByExecution(context.Context, uuid.UUID, execution.EventQuery) ([]execution.ExecutionEvent, int, error) {
	return m.events, len(m.events), nil
}

func TestRecorderEventsAreSafeAndStructured(t *testing.T) {
	store := &memEvents{}
	var logs bytes.Buffer
	metrics := observability.NewMetrics()
	r := observability.NewRecorder(store, slog.New(slog.NewJSONHandler(&logs, nil)), metrics)
	ctx, id := context.Background(), uuid.New()
	nodeID := "llm_1"
	ref := execution.NodeRef{ID: nodeID, Type: "llm", Invocation: 1}
	leaky := execution.ExecutionError{Code: "AUTHENTICATION_FAILED", Message: "provider said: invalid key " + secret,
		Retryable: false, Source: execution.SourceProvider, NodeID: &nodeID}

	r.ExecutionStarted(ctx, id, 1, 3, 40*time.Millisecond)
	r.NodeStarted(ctx, id, ref)
	r.NodeFailed(ctx, id, ref, leaky, 120*time.Millisecond)
	r.RetryScheduled(ctx, id, &nodeID, 2, time.Second, leaky)
	r.RetryStarted(ctx, id, &nodeID, 2)
	r.NodeCompleted(ctx, id, execution.NodeRef{ID: nodeID, Type: "llm", Invocation: 2}, 80*time.Millisecond)
	r.ExecutionCompleted(ctx, id, 1, time.Second)

	var types []string
	for _, e := range store.events {
		types = append(types, string(e.Type))
		if e.ExecutionID != id {
			t.Fatalf("event for another execution: %+v", e)
		}
	}
	if strings.Join(types, ",") != "EXECUTION_STARTED,NODE_STARTED,NODE_FAILED,RETRY_SCHEDULED,RETRY_STARTED,NODE_COMPLETED,EXECUTION_COMPLETED" {
		t.Fatalf("types = %v", types)
	}
	failed := store.events[2]
	if *failed.NodeID != nodeID || failed.Data["error"].(map[string]any)["code"] != "AUTHENTICATION_FAILED" || failed.Data["duration_ms"] != int64(120) {
		t.Fatalf("NODE_FAILED = %+v", failed)
	}
	all, _ := json.Marshal(store.events)
	if strings.Contains(string(all), secret) || strings.Contains(logs.String(), secret) {
		t.Fatal("secret in events or logs")
	}
	// One structured log line per event, with correlation IDs.
	if !strings.Contains(logs.String(), `"event":"node_execution_failed"`) || !strings.Contains(logs.String(), `"error_code":"AUTHENTICATION_FAILED"`) ||
		!strings.Contains(logs.String(), `"execution_id":"`+id.String()+`"`) || !strings.Contains(logs.String(), `"node_id":"llm_1"`) {
		t.Fatalf("logs = %s", logs.String())
	}
	snap := metrics.Snapshot()
	if snap.Counters[observability.MetricExecutionsStarted] != 1 || snap.Counters[observability.MetricExecutionsCompleted] != 1 ||
		snap.Counters[observability.MetricRetries] != 1 || snap.Counters[observability.MetricProviderErrors] != 1 ||
		snap.Durations[observability.MetricNodeDuration].Count != 2 || snap.Durations[observability.MetricQueueLatency].Max != 40*time.Millisecond {
		t.Fatalf("metrics = %+v", snap)
	}
}

// Failure policy: an event store failure is logged and counted; the caller
// is never affected (no panic, no error, bounded time).
func TestRecorderSurvivesStoreFailure(t *testing.T) {
	store := &memEvents{err: errors.New("connection refused")}
	var logs bytes.Buffer
	metrics := observability.NewMetrics()
	r := observability.NewRecorder(store, slog.New(slog.NewJSONHandler(&logs, nil)), metrics)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	r.ExecutionCompleted(cancelled, uuid.New(), 1, time.Second)
	if time.Since(start) > time.Second {
		t.Fatal("recorder blocked")
	}
	if metrics.Snapshot().Counters[observability.MetricEventPersistFailures] != 1 || !strings.Contains(logs.String(), "execution_event_persist_failed") {
		t.Fatalf("failure not reported: %s", logs.String())
	}
	// A nil store and nil metrics are allowed.
	observability.NewRecorder(nil, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), nil).ExecutionStarted(context.Background(), uuid.New(), 1, 1, 0)
}

func TestContextLoggerAddsRequestID(t *testing.T) {
	var buf bytes.Buffer
	logger := observability.NewContextLogger(slog.New(slog.NewJSONHandler(&buf, nil)))
	logger.InfoContext(observability.WithRequestID(context.Background(), "req-42"), "hello", "execution_id", "e-1")
	if !strings.Contains(buf.String(), `"request_id":"req-42"`) || !strings.Contains(buf.String(), `"execution_id":"e-1"`) {
		t.Fatalf("log = %s", buf.String())
	}
}
