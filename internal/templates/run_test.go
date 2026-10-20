package templates_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"workflow-optimizer/internal/app"
	"workflow-optimizer/internal/config"
	"workflow-optimizer/internal/execution"
	"workflow-optimizer/internal/node"
	"workflow-optimizer/internal/templates"
	"workflow-optimizer/internal/workflow"
)

// stand-ins replace the steps that call outside services (apps, AI, files,
// knowledge): same type and definition, answering with sample values of
// their output types, and recording what the engine handed them.
var standIn = map[string]bool{"llm": true, "extract_text": true, "transcribe_audio": true, "knowledge_search": true, "knowledge_add": true, "http": true}

type call struct {
	config map[string]any
	ports  map[string]any
}

type recorder struct {
	mu    sync.Mutex
	calls map[string]call // by node ID
}

type probe struct {
	def node.NodeDefinition
	rec *recorder
}

func (p *probe) Type() string { return p.def.Type }

// llmAnswer is a model's answer: JSON with every field the templates ask
// for when the prompt asks for JSON, else text.
func llmAnswer(prompt string) string {
	if strings.Contains(prompt, "JSON only") {
		return "```json\n" + `{"shop": "Cafe Ada", "date": "2026-10-01", "amount": "12.50", "score": 4, "reason": "Good fit", "title": "Call Ada", "details": "Ask about the order", "summary": "Short", "sentiment": "positive", "action_items": ["Reply"]}` + "\n```"
	}
	return "Sample answer to: " + prompt
}

func sample(port node.PortDefinition) any {
	switch port.Type {
	case node.ValueTypeString:
		return "sample " + port.Name
	case node.ValueTypeNumber:
		return 3.0
	case node.ValueTypeBoolean:
		return true
	case node.ValueTypeArray:
		return []any{map[string]any{"from": "Ada <ada@example.test>", "subject": "Hello", "Email": "ada@example.test"}}
	default:
		return map[string]any{"email": "ada@example.test", "Email": "ada@example.test", "Name": "Ada", "Status": "Follow up"}
	}
}

func (p *probe) Execute(_ context.Context, in node.NodeInput) (node.NodeOutput, error) {
	ports := map[string]any{}
	for k, v := range in.Ports {
		ports[k] = v.Data
	}
	p.rec.mu.Lock()
	// The idempotency key is "<operation>/<node ID>".
	p.rec.calls[strings.TrimPrefix(in.IdempotencyKey, "op/")] = call{config: in.Config, ports: ports}
	p.rec.mu.Unlock()
	out := node.NewNodeOutput(nil)
	for _, port := range p.def.Outputs {
		v := sample(port)
		if p.def.Type == "llm" && port.Name == "response" {
			prompt, _ := in.Config["prompt"].(string)
			if s, ok := ports["prompt"].(string); ok { // a connected Prompt step
				prompt = s
			}
			v = llmAnswer(prompt)
		}
		out.SetPort(port.Name, node.Value{Type: port.Type, Data: v})
	}
	return out, nil
}

func standInRegistry(t *testing.T, rec *recorder) node.Registry {
	t.Helper()
	a, err := app.Bootstrap(config.Config{})
	if err != nil {
		t.Fatal(err)
	}
	reg := node.NewRegistry()
	for _, typ := range a.NodeRegistry.List() {
		def, _ := a.NodeRegistry.GetDefinition(typ)
		n, _ := a.NodeRegistry.Resolve(typ)
		if standIn[typ] || def.Integration != nil {
			n = &probe{def: def, rec: rec}
		}
		if err := reg.RegisterNode(n, def); err != nil {
			t.Fatal(err)
		}
	}
	return reg
}

// Every gallery template, copied into a new workflow, runs end to end on
// the real engine with the answers the Run window would ask for: every
// {{blank}} is filled, every step gets what it needs, and there is a result.
func TestEveryTemplateRuns(t *testing.T) {
	for _, tpl := range templates.List() {
		t.Run(tpl.ID, func(t *testing.T) {
			rec := &recorder{calls: map[string]call{}}
			reg := standInRegistry(t, rec)
			def, err := templates.Instantiate(tpl.ID)
			if err != nil {
				t.Fatal(err)
			}
			if res := workflow.NewValidator(reg).Validate(def); !res.Valid {
				t.Fatalf("invalid: %+v", res.Errors)
			}
			// The Run window's answers: one per Input card with a Field
			// name (a file is its ID), plus the developer examples' input.
			input := map[string]any{"query": "What is a DAG?"}
			for _, n := range def.Nodes {
				if f, _ := n.Config["field"].(string); f != "" {
					input[f] = "answer for " + f
					if n.Config["field_type"] == "file" {
						input[f] = "6f1c2a52-8f0e-4bd5-9a52-2f1b8c1f4d10"
					}
				}
			}
			// As the API does before every run: variables' defaults.
			input, problems := def.ApplyVariables(input)
			if len(problems) > 0 {
				t.Fatalf("variables: %v", problems)
			}
			res, err := execution.NewGraphExecutor(reg).ExecuteWithOptions(context.Background(), def, input, execution.ExecuteOptions{OperationKey: "op"})
			if err != nil {
				t.Fatalf("run failed: %v", err)
			}
			if len(res.Outputs) == 0 {
				t.Fatal("no result")
			}
			for _, out := range res.Outputs {
				for port, v := range out.Ports {
					if v.Data == nil || v.Data == "" {
						t.Errorf("empty result on %s", port)
					}
				}
			}
			names := map[string]string{}
			for _, n := range def.Nodes {
				names[n.ID] = n.Name
			}
			for id, c := range rec.calls {
				raw, _ := json.Marshal(c.config)
				if strings.Contains(string(raw), "{{") {
					t.Errorf("%s got an unfilled blank: %s", names[id], raw)
				}
			}
			for _, n := range def.Nodes {
				_, ran := rec.calls[n.ID]
				if (standIn[n.Type] || strings.Contains(n.Type, ".")) && !ran {
					t.Errorf("%s did not run", n.Name)
				}
			}
		})
	}
}
