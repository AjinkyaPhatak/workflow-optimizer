package execution_test

import (
	"errors"
	"reflect"
	"testing"

	"workflow-optimizer/internal/execution"
	"workflow-optimizer/internal/workflow"
)

// llmChain: in -> P (emits text) -> llm_1 (real LLM node, nil provider stub) -> R -> out.
func llmChain(pCfg, rCfg map[string]any) workflow.Definition {
	return graph(nodes(entry("in"), probeWith("P", pCfg), wfNode("llm_1", "llm", nil), probeWith("R", rCfg), exit("out")),
		edge("in", "data", "P", "in"), edge("P", "text", "llm_1", "prompt"),
		edge("llm_1", "response", "R", "s"), edge("R", "out", "out", "value"))
}

func TestRuntimeVariablesResolve(t *testing.T) {
	e := newEnv(t)
	def := llmChain(
		map[string]any{"message": "{{input.query}}"},
		map[string]any{"message": "{{llm_1.response}}"},
	)
	if _, err := e.run(t, def, map[string]any{"query": "What is a DAG?"}); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got := e.rec.call(t, "P").Input.Config["message"]; got != "What is a DAG?" {
		t.Fatalf("{{input.query}} = %#v", got)
	}
	if got := e.rec.call(t, "R").Input.Config["message"]; got != "stub:llm response" {
		t.Fatalf("{{llm_1.response}} = %#v", got)
	}
}

func TestMixedLiteralInterpolation(t *testing.T) {
	e := newEnv(t)
	def := llmChain(
		map[string]any{"message": "Q: {{input.query}}"},
		map[string]any{
			"message": "Q={{input.query}} A={{llm_1.response}} n={{input.count}} ok={{input.flag}} name={{customer.name}} bare={{query}} obj={{input.customer}}",
			"payload": map[string]any{
				"count":    "{{input.count}}",    // exact reference keeps its type
				"customer": "{{input.customer}}", // object preserved
				"list":     []any{"{{P.out.label}}", "x-{{input.tags.1}}"},
			},
		},
	)
	input := map[string]any{"query": "why", "count": 2.0, "flag": true,
		"customer": map[string]any{"name": "Ada"}, "tags": []any{"t0", "t1"}}
	if _, err := e.run(t, def, input); err != nil {
		t.Fatalf("execute: %v", err)
	}
	cfg := e.rec.call(t, "R").Input.Config
	want := `Q=why A=stub:llm response n=2 ok=true name=Ada bare=why obj={"name":"Ada"}`
	if cfg["message"] != want {
		t.Fatalf("message = %q\nwant      %q", cfg["message"], want)
	}
	payload := cfg["payload"].(map[string]any)
	if payload["count"] != 2.0 {
		t.Fatalf("count = %#v, want float64 2", payload["count"])
	}
	if !reflect.DeepEqual(payload["customer"], map[string]any{"name": "Ada"}) {
		t.Fatalf("customer = %#v", payload["customer"])
	}
	if !reflect.DeepEqual(payload["list"], []any{"P", "x-t1"}) {
		t.Fatalf("list = %#v", payload["list"])
	}
	// Resolved objects must not alias the caller's input.
	payload["customer"].(map[string]any)["name"] = "mutated"
	if input["customer"].(map[string]any)["name"] != "Ada" {
		t.Fatal("resolved value aliases workflow input")
	}
}

func TestResolvedValuesAreNotReinterpreted(t *testing.T) {
	e := newEnv(t)
	def := graph(nodes(entry("in"), probeWith("A", map[string]any{"message": "x {{input.query}} y"}), exit("out")),
		edge("in", "data", "A", "in"), edge("A", "out", "out", "value"))
	if _, err := e.run(t, def, map[string]any{"query": "{{input.secret}}", "secret": "leak"}); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got := e.rec.call(t, "A").Input.Config["message"]; got != "x {{input.secret}} y" {
		t.Fatalf("message = %q", got)
	}
}

func TestUnresolvedReferencesFailClearly(t *testing.T) {
	cases := map[string]workflow.Definition{
		"missing input key": graph(nodes(entry("in"), probeWith("A", map[string]any{"message": "{{input.missing}}"}), exit("out")),
			edge("in", "data", "A", "in"), edge("A", "out", "out", "value")),
		"unknown bare name": graph(nodes(entry("in"), probeWith("A", map[string]any{"message": "hi {{nobody.here}}"}), exit("out")),
			edge("in", "data", "A", "in"), edge("A", "out", "out", "value")),
		"missing output port": graph(nodes(entry("in"), probe("A"), probeWith("B", map[string]any{"message": "{{A.nope}}"}), exit("out")),
			edge("in", "data", "A", "in"), edge("A", "out", "B", "in"), edge("B", "out", "out", "value")),
		"node without port": graph(nodes(entry("in"), probe("A"), probeWith("B", map[string]any{"message": "{{A}}"}), exit("out")),
			edge("in", "data", "A", "in"), edge("A", "out", "B", "in"), edge("B", "out", "out", "value")),
		// A sibling runs earlier in the plan but is not an upstream dependency.
		"non-upstream node": graph(nodes(entry("in"), probe("A"), probeWith("B", map[string]any{"message": "{{A.out}}"}), exit("out_a"), exit("out_b")),
			edge("in", "data", "A", "in"), edge("in", "data", "B", "in"),
			edge("A", "out", "out_a", "value"), edge("B", "out", "out_b", "value")),
	}
	for name, def := range cases {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			_, err := e.run(t, def, map[string]any{"query": "q"})
			if !errors.Is(err, execution.ErrUnresolvedReference) {
				t.Fatalf("err = %v, want ErrUnresolvedReference", err)
			}
			var nodeErr *execution.NodeExecutionError
			if !errors.As(err, &nodeErr) || nodeErr.Stage != execution.StageResolveConfig {
				t.Fatalf("err = %#v", err)
			}
			for _, c := range e.rec.calls {
				if c.Label == nodeErr.NodeID {
					t.Fatalf("node %q executed with an unresolved reference", c.Label)
				}
			}
		})
	}
}

// Phase H4: an edge into "var:<name>" feeds {{name}} in the target's
// settings, with the value's type kept for an exact reference; it is not a
// port. Unconnected names still read the workflow input.
func TestVariableInputsFeedReferences(t *testing.T) {
	e := newEnv(t)
	def := graph(nodes(entry("in"), probeWith("P", map[string]any{"message": "Ada"}),
		probeWith("R", map[string]any{"message": "Hi {{who}}, about {{query}}", "payload": map[string]any{"whole": "{{data}}", "deep": "{{data.query}}"}}), exit("out")),
		edge("in", "data", "P", "in"),
		edge("P", "text", "R", "var:who"), edge("in", "data", "R", "var:data"),
		edge("R", "out", "out", "value"))
	if res := workflow.NewValidator(e.reg).Validate(def); !res.Valid {
		t.Fatalf("invalid: %+v", res.Errors)
	}
	if _, err := e.run(t, def, map[string]any{"query": "DAGs"}); err != nil {
		t.Fatalf("execute: %v", err)
	}
	r := e.rec.call(t, "R").Input
	payload := r.Config["payload"].(map[string]any)
	if r.Config["message"] != "Hi Ada, about DAGs" || !reflect.DeepEqual(payload["whole"], map[string]any{"query": "DAGs"}) || payload["deep"] != "DAGs" {
		t.Fatalf("config = %#v", r.Config)
	}
	if len(r.Ports) != 0 {
		t.Fatalf("variable inputs are not ports: %#v", r.Ports)
	}
}

func TestVariableInputValidation(t *testing.T) {
	e := newEnv(t)
	for name, c := range map[string]struct {
		port string
		code workflow.ValidationErrorCode
	}{
		"bad name":     {"var:2x", workflow.ErrInvalidTargetPort},
		"reserved":     {"var:input", workflow.ErrInvalidTargetPort},
		"empty":        {"var:", workflow.ErrInvalidTargetPort},
		"not declared": {"who", workflow.ErrInvalidTargetPort},
	} {
		def := graph(nodes(entry("in"), probe("P"), exit("out")), edge("in", "data", "P", c.port), edge("P", "out", "out", "value"))
		if res := workflow.NewValidator(e.reg).Validate(def); res.Valid || !hasCode(res, c.code, "P") {
			t.Fatalf("%s: %+v", name, res.Errors)
		}
	}
	// One connection per variable input; never into the entry node.
	def := graph(nodes(entry("in"), probe("P"), probe("Q"), exit("out")),
		edge("in", "data", "P", "in"), edge("in", "data", "Q", "var:x"), edge("P", "out", "Q", "var:x"), edge("Q", "out", "out", "value"))
	if res := workflow.NewValidator(e.reg).Validate(def); res.Valid || !hasCode(res, workflow.ErrMultipleConnections, "Q") || len(res.Errors) != 1 {
		t.Fatalf("fan-in: %+v", res.Errors)
	}
	def = graph(nodes(entry("in"), probe("P"), exit("out")), edge("P", "out", "in", "var:x"), edge("in", "data", "P", "in"), edge("P", "out", "out", "value"))
	if res := workflow.NewValidator(e.reg).Validate(def); res.Valid {
		t.Fatal("a variable input on the entry node was accepted")
	}
}

func hasCode(res workflow.ValidationResult, code workflow.ValidationErrorCode, nodeID string) bool {
	for _, e := range res.Errors {
		if e.Code == code && e.NodeID == nodeID {
			return true
		}
	}
	return false
}
