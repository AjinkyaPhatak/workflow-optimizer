package execution_test

import (
	"context"
	"testing"
	"time"

	"workflow-optimizer/internal/execution"
	"workflow-optimizer/internal/node"
	"workflow-optimizer/internal/workflow"
)

func intp(v int) *int { return &v }

func withSettings(n workflow.Node, s workflow.NodeSettings) workflow.Node {
	n.Settings = &s
	return n
}

// fails makes a probe fail (retryable or not) the first `times` times.
func (e *env) fails(label string, times int, retryable bool) *int {
	n := 0
	e.rec.hooks[label] = func(context.Context) error {
		n++
		if n <= times {
			return node.NewNodeError(node.ErrCodeUnavailable, "service down", retryable)
		}
		return nil
	}
	return &n
}

// Phase E2: on_error continue records the failure, skips what only the
// failed node feeds, and lets the rest of the run complete.
func TestOnErrorContinue(t *testing.T) {
	e := newEnv(t)
	def := graph(nodes(entry("in"), withSettings(probe("A"), workflow.NodeSettings{OnError: workflow.OnErrorContinue}), probe("B"), probe("C"),
		exit("oB"), exit("oC")),
		edge("in", "data", "A", "in"), edge("A", "out", "B", "in"), edge("in", "data", "C", "in"),
		edge("B", "out", "oB", "value"), edge("C", "out", "oC", "value"))
	e.fails("A", 99, false)
	res, err := e.run(t, def, map[string]any{})
	if err != nil {
		t.Fatalf("the run must survive a handled failure: %v", err)
	}
	if !res.State.Failed["A"] || !res.State.Skipped["B"] || res.State.Skipped["C"] {
		t.Fatalf("failed %v skipped %v", res.State.Failed, res.State.Skipped)
	}
	if len(res.OutputNodeIDs) != 1 || res.OutputNodeIDs[0] != "oC" {
		t.Fatalf("outputs: %v", res.OutputNodeIDs)
	}
	// Without the setting the same failure stops the run.
	def.Nodes[1].Settings = nil
	if _, err := e.run(t, def, map[string]any{}); err == nil {
		t.Fatal("stop is the default")
	}
}

// error_output routes the failure to a handler branch.
func TestOnErrorOutput(t *testing.T) {
	e := newEnv(t)
	def := graph(nodes(entry("in"), withSettings(probe("A"), workflow.NodeSettings{OnError: workflow.OnErrorOutput}), probe("ok"), probe("handler"),
		exit("o1"), exit("o2")),
		edge("in", "data", "A", "in"), edge("A", "out", "ok", "in"), edge("A", "error", "handler", "in"),
		edge("ok", "out", "o1", "value"), edge("handler", "out", "o2", "value"))
	e.fails("A", 99, false)
	if _, err := e.run(t, def, map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if got := e.rec.labels(); !equalStrings(got, []string{"A", "handler"}) {
		t.Fatalf("executed %v", got)
	}
	failure := portData(t, e.rec.call(t, "handler"), "in").(map[string]any)
	if failure["code"] != "UNAVAILABLE" || failure["node_id"] != "A" || failure["message"] == "" {
		t.Fatalf("error output: %v", failure)
	}
	// When A succeeds, the error branch is skipped instead.
	e.rec.calls, e.rec.hooks = nil, map[string]func(context.Context) error{}
	if _, err := e.run(t, def, map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if got := e.rec.labels(); !equalStrings(got, []string{"A", "ok"}) {
		t.Fatalf("executed %v", got)
	}
	// The error output exists only with error_output.
	def.Nodes[1].Settings = &workflow.NodeSettings{OnError: workflow.OnErrorContinue}
	if r := workflow.NewValidator(e.reg).Validate(def); r.Valid {
		t.Fatal("an error edge without error_output must be rejected")
	}
}

// A failure while resolving settings ({{variables}}) is handled too.
func TestOnErrorContinueBeforeExecute(t *testing.T) {
	e := newEnv(t)
	def := graph(nodes(entry("in"), withSettings(probeWith("A", map[string]any{"message": "{{input.missing}}"}), workflow.NodeSettings{OnError: workflow.OnErrorOutput}),
		probe("handler"), exit("out")),
		edge("in", "data", "A", "in"), edge("A", "error", "handler", "in"), edge("handler", "out", "out", "value"))
	if _, err := e.run(t, def, map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if got := e.rec.labels(); !equalStrings(got, []string{"handler"}) {
		t.Fatalf("executed %v", got)
	}
}

// Per-node retries: only temporary failures, as many attempts as set.
func TestNodeRetrySettings(t *testing.T) {
	e := newEnv(t)
	def := graph(nodes(entry("in"), withSettings(probe("A"), workflow.NodeSettings{RetryAttempts: intp(3), RetryDelayMS: intp(10)}), exit("out")),
		edge("in", "data", "A", "in"), edge("A", "out", "out", "value"))
	e.mustValid(t, def)
	run := func() error {
		_, err := e.exec.ExecuteWithOptions(context.Background(), def, map[string]any{}, execution.ExecuteOptions{})
		return err
	}
	calls := e.fails("A", 2, true)
	start := time.Now()
	if err := run(); err != nil || *calls != 3 {
		t.Fatalf("two temporary failures, third attempt succeeds: %v after %d calls", err, *calls)
	}
	if time.Since(start) < 20*time.Millisecond {
		t.Fatal("the retry delay is honoured")
	}
	calls = e.fails("A", 3, true)
	if err := run(); err == nil || *calls != 3 {
		t.Fatalf("at most 3 attempts: %v after %d calls", err, *calls)
	}
	calls = e.fails("A", 1, false)
	if err := run(); err == nil || *calls != 1 {
		t.Fatalf("a permanent failure is never retried: %v after %d calls", err, *calls)
	}
}

func TestNodeSettingsValidation(t *testing.T) {
	e := newEnv(t)
	for name, s := range map[string]workflow.NodeSettings{
		"on_error":       {OnError: "ignore"},
		"attempts":       {RetryAttempts: intp(9)},
		"zero attempts":  {RetryAttempts: intp(0)},
		"negative delay": {RetryDelayMS: intp(-1)},
		"long delay":     {RetryDelayMS: intp(60000)},
	} {
		def := graph(nodes(entry("in"), withSettings(probe("A"), s), exit("out")), edge("in", "data", "A", "in"), edge("A", "out", "out", "value"))
		r := workflow.NewValidator(e.reg).Validate(def)
		if r.Valid || r.Errors[0].Code != workflow.ErrInvalidNodeSettings {
			t.Fatalf("%s: %+v", name, r.Errors)
		}
	}
}
