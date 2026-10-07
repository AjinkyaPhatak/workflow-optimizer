package workflow_test

import (
	"encoding/json"
	"strings"
	"testing"

	"workflow-optimizer/internal/app"
	"workflow-optimizer/internal/config"
	"workflow-optimizer/internal/workflow"
)

func appValidator(t *testing.T) *workflow.GraphValidator {
	t.Helper()
	a, err := app.Bootstrap(config.Config{})
	if err != nil {
		t.Fatal(err)
	}
	return workflow.NewValidator(a.NodeRegistry)
}

func llmWorkflow(cfg map[string]any, vars []workflow.Variable) workflow.Definition {
	pos := &workflow.Position{}
	return workflow.Definition{
		Version:  workflow.DefinitionSchemaVersion,
		Settings: map[string]any{},
		Nodes: []workflow.Node{
			{ID: "in", Type: "input", Name: "Input", Position: pos, Config: map[string]any{}},
			{ID: "llm_1", Type: "llm", Name: "LLM", Position: pos, Config: cfg},
			{ID: "out", Type: "output", Name: "Output", Position: pos, Config: map[string]any{}},
		},
		Edges: []workflow.Edge{
			{ID: "e1", Source: "in", SourcePort: "data", Target: "llm_1", TargetPort: "prompt"},
			{ID: "e2", Source: "llm_1", SourcePort: "response", Target: "out", TargetPort: "value"},
		},
		Variables: vars,
	}
}

func codesFor(res workflow.ValidationResult, code workflow.ValidationErrorCode) []string {
	var out []string
	for _, e := range res.Errors {
		if e.Code == code {
			out = append(out, e.Message)
		}
	}
	return out
}

func TestDefinitionRoundTripWithVariables(t *testing.T) {
	// A pre-Phase-B definition serializes exactly as before: no variables key.
	legacy := llmWorkflow(map[string]any{}, nil)
	b, _ := json.Marshal(legacy)
	if strings.Contains(string(b), "variables") {
		t.Fatalf("definitions without variables must not change: %s", b)
	}
	with := llmWorkflow(map[string]any{}, []workflow.Variable{
		{Name: "customer_name", Type: workflow.VariableString, Default: "", Description: "Customer's name"},
		{Name: "limit", Type: workflow.VariableNumber, Default: nil},
	})
	b, _ = json.Marshal(with)
	var back workflow.Definition
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if len(back.Variables) != 2 || back.Variables[0].Default != "" || back.Variables[1].Default != nil || back.Variables[0].Description != "Customer's name" {
		t.Fatalf("round trip: %+v", back.Variables)
	}
}

func TestVariableValidation(t *testing.T) {
	v := appValidator(t)
	ok := llmWorkflow(map[string]any{}, []workflow.Variable{{Name: "customer_email", Type: workflow.VariableString, Default: "a@b.c"}})
	if res := v.Validate(ok); len(codesFor(res, workflow.ErrInvalidVariable)) != 0 {
		t.Fatalf("valid variables: %+v", res.Errors)
	}
	bad := llmWorkflow(map[string]any{}, []workflow.Variable{
		{Name: "1st", Type: workflow.VariableString},
		{Name: "input", Type: workflow.VariableString},
		{Name: "dup", Type: workflow.VariableString},
		{Name: "dup", Type: workflow.VariableString},
		{Name: "llm_1", Type: workflow.VariableString},
		{Name: "when", Type: "date"},
		{Name: "count", Type: workflow.VariableNumber, Default: "three"},
	})
	got := codesFor(v.ValidateWithOptions(bad, workflow.ValidationOptions{Mode: workflow.ValidationDraft}), workflow.ErrInvalidVariable)
	if len(got) != 6 {
		t.Fatalf("want 6 variable problems (also in draft mode), got %q", got)
	}
}

func TestApplyVariables(t *testing.T) {
	d := llmWorkflow(map[string]any{}, []workflow.Variable{
		{Name: "customer_name", Type: workflow.VariableString, Default: "friend"},
		{Name: "limit", Type: workflow.VariableNumber, Default: 3.0},
		{Name: "account", Type: workflow.VariableString},
	})
	in := map[string]any{"query": "hi", "limit": 5.0, "account": "acme"}
	out, problems := d.ApplyVariables(in)
	if len(problems) != 0 || out["customer_name"] != "friend" || out["limit"] != 5.0 || out["query"] != "hi" {
		t.Fatalf("defaults fill gaps, given values win: %v %v", out, problems)
	}
	if _, set := in["customer_name"]; set {
		t.Fatal("the caller's input must not be modified")
	}
	_, problems = d.ApplyVariables(map[string]any{"limit": "five"})
	if len(problems) != 2 {
		t.Fatalf("wrong type and missing required value: %v", problems)
	}
}

func TestConfigOptionsAreValidated(t *testing.T) {
	v := appValidator(t)
	check := func(cfg map[string]any) []string {
		return codesFor(v.Validate(llmWorkflow(cfg, nil)), workflow.ErrInvalidNodeConfig)
	}
	if got := check(map[string]any{"provider": "openai", "model": "gpt-5-mini", "temperature": 0.7, "max_tokens": 2048.0}); len(got) != 0 {
		t.Fatalf("valid config: %q", got)
	}
	if got := check(map[string]any{"model": "my-fine-tune"}); len(got) != 0 {
		t.Fatalf("custom model IDs are allowed: %q", got)
	}
	if got := check(map[string]any{"provider": "acme"}); len(got) != 1 || !strings.Contains(got[0], "allowed options") {
		t.Fatalf("unknown provider: %q", got)
	}
	if got := check(map[string]any{"temperature": 3.0, "max_tokens": 0.0}); len(got) != 2 || !strings.Contains(got[0], "out of range") {
		t.Fatalf("ranges: %q", got)
	}
	if got := check(map[string]any{"provider": "{{provider_name}}"}); len(got) != 0 {
		t.Fatalf("references are checked at run time: %q", got)
	}
}
