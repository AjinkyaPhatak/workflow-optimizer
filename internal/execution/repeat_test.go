package execution_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"workflow-optimizer/internal/execution"
	"workflow-optimizer/internal/node"
	"workflow-optimizer/internal/workflow"
)

func repeatOver(n workflow.Node, over string, extra ...func(*workflow.NodeSettings)) workflow.Node {
	s := workflow.NodeSettings{RepeatOver: over}
	for _, f := range extra {
		f(&s)
	}
	n.Settings = &s
	return n
}

// in(field names) -> R (repeats over {{who}}) -> out.
func repeatGraph(r workflow.Node, rTarget string) workflow.Definition {
	return graph(nodes(wfNode("in", "input", map[string]any{"field": "names"}), r, exit("out")),
		edge("in", "data", "R", rTarget), edge("R", "out", "out", "value"))
}

func probeCalls(e *env, label string) []node.NodeInput {
	var out []node.NodeInput
	for _, c := range e.rec.calls {
		if c.Label == label {
			out = append(out, c.Input)
		}
	}
	return out
}

// Phase I4: a step repeating over {{who}} runs once per item, with {{who}}
// set to the item and its own idempotency key; its outputs are lists.
func TestRepeatOverVariableInput(t *testing.T) {
	e := newEnv(t)
	def := repeatGraph(repeatOver(probeWith("R", map[string]any{"message": "Hi {{who}}, about {{topic}}"}), "var:who"), "var:who")
	e.mustValid(t, def)
	res, err := e.exec.ExecuteWithOptions(context.Background(), def, map[string]any{"names": []any{"Ada", "Grace"}, "topic": "DAGs"},
		execution.ExecuteOptions{OperationKey: "op-1"})
	if err != nil {
		t.Fatal(err)
	}
	calls := probeCalls(e, "R")
	if len(calls) != 2 || calls[0].Config["message"] != "Hi Ada, about DAGs" || calls[1].Config["message"] != "Hi Grace, about DAGs" {
		t.Fatalf("calls: %+v", calls)
	}
	if calls[0].IdempotencyKey != "op-1/R#0" || calls[1].IdempotencyKey != "op-1/R#1" {
		t.Fatalf("keys: %q %q", calls[0].IdempotencyKey, calls[1].IdempotencyKey)
	}
	out := res.Outputs["out"]
	v, _ := out.GetPort("result")
	if got := v.Data.([]any); len(got) != 2 {
		t.Fatalf("output: %#v", v.Data)
	}
}

func TestRepeatOverPort(t *testing.T) {
	e := newEnv(t)
	def := repeatGraph(repeatOver(probe("R"), "in"), "in")
	if _, err := e.run(t, def, map[string]any{"names": []any{map[string]any{"n": 1.0}, map[string]any{"n": 2.0}, map[string]any{"n": 3.0}}}); err != nil {
		t.Fatal(err)
	}
	calls := probeCalls(e, "R")
	if len(calls) != 3 || !reflect.DeepEqual(calls[2].Ports["in"].Data, map[string]any{"n": 3.0}) {
		t.Fatalf("calls: %+v", calls)
	}
	// A single value is one item; nothing is no items (the step does not run).
	e.rec.calls = nil
	if _, err := e.run(t, def, map[string]any{"names": "solo"}); err != nil || len(probeCalls(e, "R")) != 1 {
		t.Fatalf("single: %v %d", err, len(probeCalls(e, "R")))
	}
	tooMany := make([]any, workflow.MaxRepeatItems+1)
	for i := range tooMany {
		tooMany[i] = i
	}
	_, err := e.run(t, def, map[string]any{"names": tooMany})
	var ne *node.NodeError
	if !errors.As(err, &ne) || ne.Code != node.ErrCodeInvalidInput || !strings.Contains(ne.Message, "at most 100") {
		t.Fatalf("too many: %v", err)
	}
}

// A retry after one item failed continues with that item: finished items
// never run again.
func TestRepeatRetryContinuesWithTheFailedItem(t *testing.T) {
	e := newEnv(t)
	n := 0
	e.rec.hooks["R"] = func(context.Context) error {
		n++
		if n == 2 {
			return node.NewNodeError(node.ErrCodeUnavailable, "service down", true)
		}
		return nil
	}
	def := repeatGraph(repeatOver(probeWith("R", map[string]any{"message": "{{who}}"}), "var:who", func(s *workflow.NodeSettings) {
		s.RetryAttempts, s.RetryDelayMS = intp(2), intp(0)
	}), "var:who")
	if _, err := e.run(t, def, map[string]any{"names": []any{"a", "b", "c"}}); err != nil {
		t.Fatal(err)
	}
	var msgs []any
	for _, c := range probeCalls(e, "R") {
		msgs = append(msgs, c.Config["message"])
	}
	if !reflect.DeepEqual(msgs, []any{"a", "b", "b", "c"}) {
		t.Fatalf("calls: %v", msgs)
	}
	// Without retries the failure names the item.
	n = 0
	def.Nodes[1].Settings.RetryAttempts = intp(1)
	_, err := e.run(t, def, map[string]any{"names": []any{"a", "b", "c"}})
	var item *execution.RepeatItemError
	if !errors.As(err, &item) || item.Index != 1 || item.Count != 3 || !strings.Contains(err.Error(), "item 2 of 3") {
		t.Fatalf("failure: %v", err)
	}
}

func TestRepeatValidation(t *testing.T) {
	e := newEnv(t)
	check := func(name string, def workflow.Definition, code workflow.ValidationErrorCode) {
		t.Helper()
		if res := workflow.NewValidator(e.reg).Validate(def); res.Valid || !hasCode(res, code, "R") {
			t.Fatalf("%s: %+v", name, res.Errors)
		}
	}
	check("unknown input", repeatGraph(repeatOver(probe("R"), "nope"), "in"), workflow.ErrInvalidNodeSettings)
	check("bad variable", repeatGraph(repeatOver(probe("R"), "var:2x"), "in"), workflow.ErrInvalidNodeSettings)
	check("not connected", repeatGraph(repeatOver(probe("R"), "a"), "in"), workflow.ErrInvalidNodeSettings)
	// Outputs are lists: a list cannot go into a text input.
	def := graph(nodes(wfNode("in", "input", map[string]any{"field": "names"}), repeatOver(probeWith("R", map[string]any{"message": "x"}), "in"), probe("S"), exit("out")),
		edge("in", "data", "R", "in"), edge("R", "text", "S", "s"), edge("S", "out", "out", "value"))
	if res := workflow.NewValidator(e.reg).Validate(def); res.Valid || !hasCode(res, workflow.ErrIncompatiblePortTypes, "R") {
		t.Fatalf("list into text: %+v", res.Errors)
	}
	// Entry and exit steps cannot repeat.
	exitRepeats := graph(nodes(entry("in"), probe("R"), repeatOver(exit("out"), "value")), edge("in", "data", "R", "in"), edge("R", "out", "out", "value"))
	if res := workflow.NewValidator(e.reg).Validate(exitRepeats); res.Valid || !hasCode(res, workflow.ErrInvalidNodeSettings, "out") {
		t.Fatalf("exit: %+v", res.Errors)
	}
}
