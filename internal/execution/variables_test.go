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
