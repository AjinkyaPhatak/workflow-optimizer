package execution_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"workflow-optimizer/internal/execution"
	"workflow-optimizer/internal/node"
)

// A node panic is recovered at the node invocation boundary and becomes an
// ordinary fail-fast node failure.

func nilMapPanic(context.Context) error {
	var m map[string]int
	m["boom"] = 1 // a plain bug inside a node
	return nil
}

func TestNodePanicBecomesNodeExecutionError(t *testing.T) {
	e := newEnv(t)
	e.rec.hooks["B"] = nilMapPanic
	def := graph(nodes(entry("in"), probe("A"), probe("B"), probe("C"), exit("out")),
		edge("in", "data", "A", "in"), edge("A", "out", "B", "in"),
		edge("B", "out", "C", "in"), edge("C", "out", "out", "value"))

	var (
		res execution.ExecutionResult
		err error
	)
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("panic escaped GraphExecutor.Execute: %v", r)
			}
		}()
		res, err = e.run(t, def, map[string]any{})
	}()

	var nodeErr *execution.NodeExecutionError
	if !errors.As(err, &nodeErr) || nodeErr.NodeID != "B" || nodeErr.NodeType != probeType || nodeErr.Stage != execution.StageExecute {
		t.Fatalf("err = %#v", err)
	}
	var pe *execution.NodePanicError
	if !errors.Is(err, execution.ErrNodePanicked) || !errors.As(err, &pe) || pe.Value == nil {
		t.Fatalf("panic cause not preserved: %v", err)
	}
	for _, want := range []string{`node "B"`, probeType, "execute", "panicked", "assignment to entry in nil map"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q lacks %q", err, want)
		}
	}
	// Fail-fast: C never ran; the partial state is kept.
	if got := e.rec.labels(); !equalStrings(got, []string{"A", "B"}) {
		t.Fatalf("executed = %v", got)
	}
	if !equalStrings(res.State.Completed, []string{"in", "A"}) || res.Outputs != nil {
		t.Fatalf("result = %+v", res)
	}
	// Persisted classification: an ordinary node failure attributed to B.
	ee := execution.ErrorFromExecution(err)
	if ee.Code != execution.CodeNodeFailed || ee.NodeID == nil || *ee.NodeID != "B" || ee.Retryable {
		t.Fatalf("execution error = %+v", ee)
	}
}

// Whatever the panic value is, it is a node failure: a panic carrying
// context.Canceled or a node.NodeError is not reclassified, and panic(nil) is
// recovered too.
func TestNodePanicValueIsNeverReclassified(t *testing.T) {
	values := map[string]any{
		"canceled":   context.Canceled,
		"node error": node.NewNodeError(node.ErrCodeRateLimited, "x", true),
		"string":     "plain string",
		"nil":        nil,
	}
	for name, v := range values {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			e.rec.hooks["A"] = func(context.Context) error { panic(v) }
			def := graph(nodes(entry("in"), probe("A"), exit("out")),
				edge("in", "data", "A", "in"), edge("A", "out", "out", "value"))
			_, err := e.run(t, def, map[string]any{})
			if !errors.Is(err, execution.ErrNodePanicked) || errors.Is(err, context.Canceled) {
				t.Fatalf("err = %v", err)
			}
			var typed *node.NodeError
			if errors.As(err, &typed) {
				t.Fatal("a panic must not surface as a node-reported error")
			}
			if ee := execution.ErrorFromExecution(err); ee.Code != execution.CodeNodeFailed || ee.Retryable {
				t.Fatalf("execution error = %+v", ee)
			}
		})
	}
}

// Ordinary node errors are untouched by the recovery boundary.
func TestNormalNodeErrorIsNotAPanic(t *testing.T) {
	e := newEnv(t)
	e.rec.hooks["A"] = func(context.Context) error { return errBoom }
	def := graph(nodes(entry("in"), probe("A"), exit("out")),
		edge("in", "data", "A", "in"), edge("A", "out", "out", "value"))
	_, err := e.run(t, def, map[string]any{})
	if !errors.Is(err, errBoom) || errors.Is(err, execution.ErrNodePanicked) {
		t.Fatalf("err = %v", err)
	}
}

// The executor stays usable after a panic: the next run on the same
// executor/goroutine completes normally.
func TestExecutorUsableAfterNodePanic(t *testing.T) {
	e := newEnv(t)
	def := graph(nodes(entry("in"), probe("A"), exit("out")),
		edge("in", "data", "A", "in"), edge("A", "out", "out", "value"))
	e.rec.hooks["A"] = nilMapPanic
	if _, err := e.run(t, def, map[string]any{}); !errors.Is(err, execution.ErrNodePanicked) {
		t.Fatalf("first run: %v", err)
	}
	delete(e.rec.hooks, "A")
	if _, err := e.run(t, def, map[string]any{}); err != nil {
		t.Fatalf("second run: %v", err)
	}
}

// Through the Phase 8 Runner: the panic reaches the normal failure
// finalization, so the execution ends FAILED (not RUNNING) and the panicking
// node's record is FAILED.
func TestRunnerRecordsNodePanicAsFailed(t *testing.T) {
	r := newRunnerEnv(t, chainABC())
	r.rec.hooks["B"] = nilMapPanic
	id := r.create(t, map[string]any{})
	_, err := r.runner.Run(context.Background(), id)
	if !errors.Is(err, execution.ErrNodePanicked) {
		t.Fatalf("run err = %v", err)
	}
	e, _ := r.repo.Get(context.Background(), id)
	if e.Status != execution.StatusFailed || e.FinishedAt == nil || e.Error == nil ||
		e.Error.NodeID == nil || *e.Error.NodeID != "B" || e.Error.Code != execution.CodeNodeFailed ||
		!strings.Contains(e.Error.Message, "panicked") {
		t.Fatalf("execution = %+v err=%+v", e, e.Error)
	}
	recs := r.nodeRecords(t, id)
	if recs["A"].Status != execution.NodeStatusCompleted || recs["B"].Status != execution.NodeStatusFailed {
		t.Fatalf("records = %+v", recs)
	}
	if recs["B"].Error == nil || !strings.Contains(recs["B"].Error.Message, "panicked") {
		t.Fatalf("B error = %+v", recs["B"].Error)
	}
	if _, ran := recs["C"]; ran {
		t.Fatal("fail-fast: C must have no record")
	}
}
