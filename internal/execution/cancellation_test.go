package execution_test

import (
	"context"
	"errors"
	"testing"
)

func TestPreCancelledContextExecutesNoNodes(t *testing.T) {
	e := newEnv(t)
	def := graph(nodes(entry("in"), probe("A"), exit("out")),
		edge("in", "data", "A", "in"), edge("A", "out", "out", "value"))
	e.mustValid(t, def)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, err := e.exec.Execute(ctx, def, map[string]any{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if len(e.rec.calls) != 0 || len(res.State.Completed) != 0 {
		t.Fatalf("nodes executed on a cancelled context: %v %v", e.rec.labels(), res.State.Completed)
	}
}

func TestCancellationBetweenNodesStopsExecution(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e.rec.hooks["A"] = func(context.Context) error { cancel(); return nil } // A succeeds, then the run is cancelled
	def := graph(nodes(entry("in"), probe("A"), probe("B"), probe("C"), exit("out")),
		edge("in", "data", "A", "in"), edge("A", "out", "B", "in"),
		edge("B", "out", "C", "in"), edge("C", "out", "out", "value"))
	e.mustValid(t, def)
	res, err := e.exec.Execute(ctx, def, map[string]any{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if got := e.rec.labels(); !equalStrings(got, []string{"A"}) {
		t.Fatalf("executed = %v, want [A]", got)
	}
	if !equalStrings(res.State.Completed, []string{"in", "A"}) {
		t.Fatalf("completed = %v", res.State.Completed)
	}
}

func TestNodeDeadlineErrorPropagates(t *testing.T) {
	e := newEnv(t)
	e.rec.hooks["A"] = func(context.Context) error { return context.DeadlineExceeded }
	def := graph(nodes(entry("in"), probe("A"), probe("B"), exit("out")),
		edge("in", "data", "A", "in"), edge("A", "out", "B", "in"), edge("B", "out", "out", "value"))
	_, err := e.run(t, def, map[string]any{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want DeadlineExceeded", err)
	}
	if got := e.rec.labels(); !equalStrings(got, []string{"A"}) {
		t.Fatalf("executed = %v", got)
	}
}

type ctxKey struct{}

func TestNodesReceiveTheCallerContext(t *testing.T) {
	e := newEnv(t)
	ctx := context.WithValue(context.Background(), ctxKey{}, "caller")
	def := graph(nodes(entry("in"), probe("A"), probe("B"), exit("out")),
		edge("in", "data", "A", "in"), edge("A", "out", "B", "in"), edge("B", "out", "out", "value"))
	e.mustValid(t, def)
	if _, err := e.exec.Execute(ctx, def, map[string]any{}); err != nil {
		t.Fatalf("execute: %v", err)
	}
	for _, c := range e.rec.calls {
		if c.Ctx != ctx {
			t.Fatalf("node %q received a different context", c.Label)
		}
	}
	if len(e.rec.calls) != 2 {
		t.Fatalf("calls = %v", e.rec.labels())
	}
}
