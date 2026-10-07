package phase14_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"workflow-optimizer/internal/app"
	"workflow-optimizer/internal/config"
	"workflow-optimizer/internal/execution"
	"workflow-optimizer/internal/infrastructure/postgres"
	"workflow-optimizer/internal/observability"
	"workflow-optimizer/internal/workflow"
)

func TestDebuggerAPIForLLMExecution(t *testing.T) {
	e := newEnv(t, nil)
	e.startWorkers()
	a, b := e.register("Ada"), e.register("Bob")
	wf := e.llmWorkflow(a)
	id := e.execute(a, wf, "What is quantum computing?")
	done := e.wait(a, id, "COMPLETED")

	// Execution details: lifecycle plus aggregates derived from node records.
	if done["duration_ms"] == nil || done["node_count"].(float64) != 3 || done["retries"].(float64) != 0 || done["attempt"].(float64) != 1 {
		t.Fatalf("details = %v", done)
	}
	usage := done["usage"].(map[string]any)
	if usage["input_tokens"].(float64) != 7 || usage["output_tokens"].(float64) != 5 || usage["total_tokens"].(float64) != 12 {
		t.Fatalf("usage = %v", usage)
	}
	// 7 * 1000/1e6 + 5 * 2000/1e6 = 0.017 (configured test prices, "gpt-5-mini" prefix of "gpt-5-mini-2025").
	if c := done["estimated_cost_usd"].(float64); c < 0.0169 || c > 0.0171 {
		t.Fatalf("estimated cost = %v", c)
	}

	// Node records in execution order, with derived AI metadata.
	nodes := e.must(e.call(a.token, "GET", "/api/v1/executions/"+id+"/nodes", nil), 200)["items"].([]any)
	if len(nodes) != 3 {
		t.Fatalf("nodes = %v", nodes)
	}
	var order []string
	for _, x := range nodes {
		order = append(order, x.(map[string]any)["node_id"].(string))
	}
	if strings.Join(order, ",") != "in,llm_1,out" {
		t.Fatalf("order = %v", order)
	}
	llm := nodes[1].(map[string]any)
	if llm["status"] != "COMPLETED" || llm["provider"] != "openai" || llm["model"] != "gpt-5-mini-2025" || llm["attempt"].(float64) != 1 ||
		llm["duration_ms"] == nil || llm["execution_id"] != id || llm["estimated_cost_usd"] == nil {
		t.Fatalf("llm node = %v", llm)
	}
	if u := llm["usage"].(map[string]any); u["total_tokens"].(float64) != 12 {
		t.Fatalf("llm usage = %v", u)
	}
	in, _ := json.Marshal(llm["input"])
	out, _ := json.Marshal(llm["output"])
	if !strings.Contains(string(in), "What is quantum computing?") || !strings.Contains(string(in), "credential_id") ||
		!strings.Contains(string(out), "answer to") {
		t.Fatalf("input = %s output = %s", in, out)
	}
	if nodes[0].(map[string]any)["provider"] != nil || nodes[0].(map[string]any)["usage"] != nil {
		t.Fatalf("non-LLM node has AI metadata: %v", nodes[0])
	}

	// Events: chronological, scoped, paginated.
	lines, events := e.eventsSettled(a, id)
	want := "EXECUTION_STARTED NODE_STARTED:in NODE_COMPLETED:in NODE_STARTED:llm_1 NODE_COMPLETED:llm_1 NODE_STARTED:out NODE_COMPLETED:out EXECUTION_COMPLETED"
	if strings.Join(lines, " ") != want {
		t.Fatalf("events:\n got %s\nwant %s", strings.Join(lines, " "), want)
	}
	for i, ev := range events {
		if ev["execution_id"] != id || ev["id"] == "" || ev["timestamp"] == "" {
			t.Fatalf("event = %v", ev)
		}
		if i > 0 && ev["timestamp"].(string) < events[i-1]["timestamp"].(string) {
			t.Fatal("events out of order")
		}
	}
	page2 := e.must(e.call(a.token, "GET", "/api/v1/executions/"+id+"/events?page=2&page_size=3", nil), 200)
	p2 := page2["events"].([]any)
	if page2["total"].(float64) != 8 || page2["page"].(float64) != 2 || len(p2) != 3 || p2[0].(map[string]any)["id"] != events[3]["id"] {
		t.Fatalf("page 2 = %v", page2)
	}
	if r := e.call(a.token, "GET", "/api/v1/executions/"+id+"/events?page_size=101", nil); r.status != 400 {
		t.Fatalf("page_size 101: %d", r.status)
	}

	// Navigation: the workflow's executions.
	list := e.must(e.call(a.token, "GET", "/api/v1/workflows/"+wf+"/executions", nil), 200)
	if list["total"].(float64) != 1 || list["items"].([]any)[0].(map[string]any)["id"] != id {
		t.Fatalf("executions = %v", list)
	}

	// Authorization: unauthenticated 401; another workspace 404; unknown 404.
	for _, p := range []string{"", "/nodes", "/events"} {
		if r := e.call("", "GET", "/api/v1/executions/"+id+p, nil); r.status != 401 {
			t.Fatalf("anonymous %s: %d", p, r.status)
		}
		if r := e.call(b.token, "GET", "/api/v1/executions/"+id+p, nil); r.status != 404 || !strings.Contains(string(r.body), "EXECUTION_NOT_FOUND") {
			t.Fatalf("cross-workspace %s: %d %s", p, r.status, r.body)
		}
		if r := e.call(a.token, "GET", "/api/v1/executions/"+uuid.NewString()+p, nil); r.status != 404 {
			t.Fatalf("missing %s: %d", p, r.status)
		}
	}
	if r := e.call(b.token, "GET", "/api/v1/workflows/"+wf+"/executions", nil); r.status != 404 {
		t.Fatalf("cross-workspace list: %d", r.status)
	}

	// The provider key never appears in any response or persisted row; the
	// worker used it (the mock saw the bearer header).
	e.mu.Lock()
	for _, body := range e.bodies {
		if strings.Contains(body, providerSecret) || strings.Contains(body, "ciphertext") || strings.Contains(body, "Bearer sk-") {
			t.Fatalf("secret material in response: %s", body)
		}
	}
	e.mu.Unlock()
	if db := e.dbText(); strings.Contains(db, providerSecret) || strings.Contains(db, "Bearer ") {
		t.Fatal("secret material persisted in execution data")
	}
	e.openai.mu.Lock()
	if len(e.openai.auth) != 1 || e.openai.auth[0] != "Bearer "+providerSecret {
		t.Fatalf("provider auth = %v", e.openai.auth)
	}
	e.openai.mu.Unlock()
}

func TestRetryObservability(t *testing.T) {
	t.Run("in-place node retry", func(t *testing.T) {
		e := newEnv(t, nil) // DEFAULT_NODE_MAX_ATTEMPTS = 2
		e.startWorkers()
		a := e.register("Ada")
		id := e.execute(a, e.llmWorkflow(a), "fail-once please")
		done := e.wait(a, id, "COMPLETED")
		lines, events := e.eventsSettled(a, id)
		want := "EXECUTION_STARTED NODE_STARTED:in NODE_COMPLETED:in NODE_STARTED:llm_1 NODE_FAILED:llm_1 RETRY_SCHEDULED:llm_1 RETRY_STARTED:llm_1 NODE_STARTED:llm_1 NODE_COMPLETED:llm_1 NODE_STARTED:out NODE_COMPLETED:out EXECUTION_COMPLETED"
		if strings.Join(lines, " ") != want {
			t.Fatalf("events:\n got %s\nwant %s", strings.Join(lines, " "), want)
		}
		failed := events[4]["data"].(map[string]any)
		if errData := failed["error"].(map[string]any); errData["code"] != "PROVIDER_UNAVAILABLE" || errData["retryable"] != true {
			t.Fatalf("NODE_FAILED data = %v", failed)
		}
		if s := events[5]["data"].(map[string]any); s["scope"] != "node" || s["attempt"].(float64) != 2 {
			t.Fatalf("RETRY_SCHEDULED data = %v", s)
		}
		if done["retries"].(float64) != 1 {
			t.Fatalf("retries = %v", done["retries"])
		}
		nodes := e.must(e.call(a.token, "GET", "/api/v1/executions/"+id+"/nodes", nil), 200)["items"].([]any)
		var llm []map[string]any
		for _, x := range nodes {
			if n := x.(map[string]any); n["node_id"] == "llm_1" {
				llm = append(llm, n)
			}
		}
		if len(llm) != 2 || llm[0]["status"] != "FAILED" || llm[0]["attempt"].(float64) != 1 || llm[1]["status"] != "COMPLETED" || llm[1]["attempt"].(float64) != 2 {
			t.Fatalf("llm records = %v", llm)
		}
		errMsg, _ := json.Marshal(llm[0]["error"])
		if !strings.Contains(string(errMsg), "PROVIDER_UNAVAILABLE") || strings.Contains(string(errMsg), providerSecret) {
			t.Fatalf("node error = %s", errMsg)
		}
	})

	t.Run("execution retry", func(t *testing.T) {
		e := newEnv(t, func(c *config.Config) { c.Reliability.MaxAttempts, c.Reliability.NodeMaxAttempts = 2, 1 })
		e.startWorkers()
		a := e.register("Ada")
		id := e.execute(a, e.llmWorkflow(a), "fail-once please")
		done := e.wait(a, id, "COMPLETED")
		lines, events := e.eventsSettled(a, id)
		want := "EXECUTION_STARTED NODE_STARTED:in NODE_COMPLETED:in NODE_STARTED:llm_1 NODE_FAILED:llm_1 RETRY_SCHEDULED RETRY_STARTED NODE_SKIPPED:in NODE_STARTED:llm_1 NODE_COMPLETED:llm_1 NODE_STARTED:out NODE_COMPLETED:out EXECUTION_COMPLETED"
		if strings.Join(lines, " ") != want {
			t.Fatalf("events:\n got %s\nwant %s", strings.Join(lines, " "), want)
		}
		if s := events[5]["data"].(map[string]any); s["scope"] != "execution" || s["attempt"].(float64) != 2 {
			t.Fatalf("RETRY_SCHEDULED data = %v", s)
		}
		if done["attempt"].(float64) != 2 || done["retries"].(float64) != 1 {
			t.Fatalf("details = %v", done)
		}
	})

	t.Run("retry exhaustion", func(t *testing.T) {
		e := newEnv(t, func(c *config.Config) { c.Reliability.MaxAttempts, c.Reliability.NodeMaxAttempts = 2, 1 })
		e.startWorkers()
		a := e.register("Ada")
		id := e.execute(a, e.llmWorkflow(a), "always-503")
		failed := e.wait(a, id, "FAILED")
		lines, events := e.eventsSettled(a, id)
		want := "EXECUTION_STARTED NODE_STARTED:in NODE_COMPLETED:in NODE_STARTED:llm_1 NODE_FAILED:llm_1 RETRY_SCHEDULED RETRY_STARTED NODE_SKIPPED:in NODE_STARTED:llm_1 NODE_FAILED:llm_1 EXECUTION_FAILED"
		if strings.Join(lines, " ") != want {
			t.Fatalf("events:\n got %s\nwant %s", strings.Join(lines, " "), want)
		}
		last := events[len(events)-1]["data"].(map[string]any)
		if last["dead_letter"] != "attempts_exhausted" || last["error"].(map[string]any)["code"] != "PROVIDER_UNAVAILABLE" {
			t.Fatalf("EXECUTION_FAILED data = %v", last)
		}
		errJSON, _ := json.Marshal(failed["error"])
		if strings.Contains(string(errJSON), providerSecret) || !strings.Contains(string(errJSON), `"node_id":"llm_1"`) {
			t.Fatalf("execution error = %s", errJSON)
		}
	})
}

func TestTimeoutAndCancellationEvents(t *testing.T) {
	t.Run("execution timeout", func(t *testing.T) {
		e := newEnv(t, func(c *config.Config) { c.Reliability.ExecutionTimeout = 1500 * time.Millisecond })
		e.startWorkers()
		a := e.register("Ada")
		id := e.execute(a, e.llmWorkflow(a), "slow")
		failed := e.wait(a, id, "FAILED")
		if failed["error"].(map[string]any)["code"] != execution.CodeTimeout {
			t.Fatalf("error = %v", failed["error"])
		}
		lines, _ := e.eventsSettled(a, id)
		if got := strings.Join(lines, " "); !strings.HasPrefix(got, "EXECUTION_STARTED NODE_STARTED:in NODE_COMPLETED:in NODE_STARTED:llm_1") ||
			!strings.HasSuffix(got, "EXECUTION_FAILED") || strings.Contains(got, "COMPLETED:llm_1") {
			t.Fatalf("events = %s", got)
		}
	})

	t.Run("cancel running", func(t *testing.T) {
		e := newEnv(t, nil)
		e.startWorkers()
		a := e.register("Ada")
		id := e.execute(a, e.llmWorkflow(a), "slow")
		deadline := time.Now().Add(10 * time.Second)
		for e.must(e.call(a.token, "GET", "/api/v1/executions/"+id, nil), 200)["status"] != "RUNNING" {
			if time.Now().After(deadline) {
				t.Fatal("never RUNNING")
			}
			time.Sleep(20 * time.Millisecond)
		}
		e.must(e.call(a.token, "POST", "/api/v1/executions/"+id+"/cancel", nil), 202)
		e.wait(a, id, "CANCELLED")
		lines, _ := e.eventsSettled(a, id)
		got := strings.Join(lines, " ")
		if !strings.HasPrefix(got, "EXECUTION_STARTED") || !strings.HasSuffix(got, "EXECUTION_CANCELLED") ||
			strings.Count(got, "EXECUTION_CANCELLED") != 1 || strings.Contains(got, "EXECUTION_COMPLETED") || strings.Contains(got, "NODE_COMPLETED:llm_1") {
			t.Fatalf("events = %s", got)
		}
	})
}

// failingEvents is an event store that is down.
type failingEvents struct{}

func (failingEvents) Append(context.Context, execution.ExecutionEvent) (execution.ExecutionEvent, error) {
	return execution.ExecutionEvent{}, errors.New("event store unavailable")
}
func (failingEvents) ListByExecution(context.Context, uuid.UUID, execution.EventQuery) ([]execution.ExecutionEvent, int, error) {
	return nil, 0, errors.New("event store unavailable")
}

// Observability failure never changes the execution: with the event store
// down, the run completes and its execution/node state is fully recorded.
func TestEventStoreFailureDoesNotCorruptExecution(t *testing.T) {
	e := newEnv(t, nil) // no workers: this test runs the attempt itself
	a := e.register("Ada")
	wf := e.llmWorkflow(a)
	id := uuid.MustParse(e.execute(a, wf, "hello"))

	ctx := context.Background()
	store, err := postgres.Open(ctx, e.cfg.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	engine, err := app.Bootstrap(config.Config{}) // LLM node in stub mode: no provider needed here
	if err != nil {
		t.Fatal(err)
	}
	metrics := observability.NewMetrics()
	runner, err := execution.NewRunner(execution.RunnerConfig{
		Executions: postgres.NewExecutionRepository(store), NodeExecutions: postgres.NewNodeExecutionRepository(store),
		Definitions: postgres.NewWorkflowVersionRepository(store), Validator: workflow.NewValidator(engine.NodeRegistry),
		Graph:    execution.NewGraphExecutor(engine.NodeRegistry),
		Observer: observability.NewRecorder(failingEvents{}, quiet, metrics),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(ctx, id); err != nil {
		t.Fatalf("run failed because events could not be stored: %v", err)
	}
	done := e.wait(a, id.String(), "COMPLETED")
	if done["node_count"].(float64) != 3 || done["error"] != nil {
		t.Fatalf("execution = %v", done)
	}
	nodes := e.must(e.call(a.token, "GET", "/api/v1/executions/"+id.String()+"/nodes", nil), 200)["items"].([]any)
	for _, n := range nodes {
		if n.(map[string]any)["status"] != "COMPLETED" {
			t.Fatalf("node = %v", n)
		}
	}
	if got := metrics.Snapshot().Counters[observability.MetricEventPersistFailures]; got != 8 {
		t.Fatalf("persist failures counted = %d, want 8", got)
	}
	if ev := e.must(e.call(a.token, "GET", "/api/v1/executions/"+id.String()+"/events", nil), 200); ev["total"].(float64) != 0 {
		t.Fatalf("events = %v", ev)
	}
}

// OBSERVABILITY_EXPOSE_NODE_DATA=false hides node inputs/outputs from the
// debugger; status, timing and usage stay visible.
func TestNodeDataPolicy(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.ExposeNodeData = false })
	e.startWorkers()
	a := e.register("Ada")
	id := e.execute(a, e.llmWorkflow(a), "private question")
	e.wait(a, id, "COMPLETED")
	r := e.call(a.token, "GET", "/api/v1/executions/"+id+"/nodes", nil)
	if strings.Contains(string(r.body), "private question") || strings.Contains(string(r.body), `"input":`) || strings.Contains(string(r.body), `"output":`) {
		t.Fatalf("node data exposed: %s", r.body)
	}
	if !strings.Contains(string(r.body), `"total_tokens":12`) || !strings.Contains(string(r.body), `"status":"COMPLETED"`) {
		t.Fatalf("metadata missing: %s", r.body)
	}
}
