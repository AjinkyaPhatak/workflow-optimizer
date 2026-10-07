package node_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"workflow-optimizer/internal/node"
	"workflow-optimizer/internal/node/llm"
	"workflow-optimizer/internal/node/prompt"
	"workflow-optimizer/internal/node/structured_output"
	"workflow-optimizer/internal/node/transform"
	providerllm "workflow-optimizer/internal/provider/llm"
)

func nodeErrCode(t *testing.T, err error) node.ErrorCode {
	t.Helper()
	var ne *node.NodeError
	if !errors.As(err, &ne) {
		t.Fatalf("want a NodeError, got %v", err)
	}
	return ne.Code
}

func TestLLMDefinitionFromProviders(t *testing.T) {
	def := llm.DefinitionFor([]providerllm.Info{
		{Name: "openai", Label: "OpenAI", Models: []providerllm.Model{{ID: "gpt-5", Label: "GPT-5"}, {ID: "gpt-5-mini", Label: "GPT-5 mini"}}},
		{Name: "other", Label: "Other", Models: []providerllm.Model{{ID: "o-1", Label: "O1"}}},
	})
	if err := def.Validate(); err != nil {
		t.Fatal(err)
	}
	provider, _ := def.GetConfigField("provider")
	model, _ := def.GetConfigField("model")
	if len(provider.Options) != 2 || provider.AllowCustom || provider.Options[0].Label != "OpenAI" {
		t.Fatalf("provider options: %+v", provider)
	}
	if !model.AllowCustom || len(model.Options) != 3 || model.Options[2].When["provider"] != "other" {
		t.Fatalf("model options are per provider and allow custom IDs: %+v", model)
	}
	for _, f := range []string{"system", "prompt"} {
		if fld, ok := def.GetConfigField(f); !ok || !fld.Multiline || fld.Label == "" {
			t.Fatalf("%s prompt field: %+v", f, fld)
		}
	}
	if p, _ := def.GetInputPort("prompt"); p.Required {
		t.Fatal("the prompt input is optional (the User prompt setting can supply it)")
	}
	if len(llm.Definition().Config[0].Options) != 0 {
		t.Fatal("Definition() carries no provider metadata")
	}
}

func TestLLMPromptFromConfigOrPort(t *testing.T) {
	prov := &mockLLMProvider{responseText: "ok"}
	reg := providerllm.NewRegistry()
	_ = reg.Register("mock", prov)
	n := llm.New(llm.Dependencies{Providers: reg, Credentials: mockResolver{}})
	cfg := map[string]any{"provider": "mock", "model": "m", "credential_id": uuid.NewString(), "prompt": "from config", "system": "be brief"}
	run := func(ports map[string]node.Value) error {
		_, err := n.Execute(context.Background(), node.NodeInput{Ports: ports, Config: cfg, Scope: node.Scope{WorkspaceID: uuid.New()}})
		return err
	}

	if err := run(nil); err != nil {
		t.Fatal(err)
	}
	if prov.lastReq.Messages[0].Content != "from config" || prov.lastReq.System != "be brief" {
		t.Fatalf("config prompts: %+v", prov.lastReq)
	}
	if err := run(map[string]node.Value{"prompt": node.NewStringValue("from port")}); err != nil {
		t.Fatal(err)
	}
	if prov.lastReq.Messages[0].Content != "from port" || prov.lastReq.System != "be brief" {
		t.Fatalf("a connected input wins: %+v", prov.lastReq)
	}
	delete(cfg, "prompt")
	if code := nodeErrCode(t, run(nil)); code != node.ErrCodeConfiguration {
		t.Fatalf("no prompt at all: %s", code)
	}
}

func TestPromptTemplate(t *testing.T) {
	n := prompt.New()
	// The engine resolves {{references}} before the node runs; the node
	// emits the resolved template unchanged.
	out, err := n.Execute(context.Background(), node.NodeInput{Config: map[string]any{"template": "Summarize:\nhello world"}})
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := out.GetPort("prompt"); v.Data != "Summarize:\nhello world" {
		t.Fatalf("prompt: %#v", v.Data)
	}
	_, err = n.Execute(context.Background(), node.NodeInput{Config: map[string]any{"template": "  "}})
	if code := nodeErrCode(t, err); code != node.ErrCodeConfiguration {
		t.Fatalf("empty template: %s", code)
	}
}

func TestStructuredOutput(t *testing.T) {
	schema := map[string]any{"summary": "string", "score": "number?", "action_items": []any{"string"},
		"owner": map[string]any{"name": "string", "email": "string?"}}
	run := func(input any, schema any) (any, error) {
		cfg := map[string]any{}
		if schema != nil {
			cfg["schema"] = schema
		}
		out, err := structured_output.New(nil).Execute(context.Background(), node.NodeInput{
			Ports: map[string]node.Value{"input": node.NewJSONValue(input)}, Config: cfg})
		if err != nil {
			return nil, err
		}
		v, _ := out.GetPort("output")
		return v.Data, nil
	}

	text := "Here you go:\n```json\n{\"summary\": \"Done\", \"action_items\": [\"a\", \"b\"], \"owner\": {\"name\": \"Ana\"}, \"extra\": 1}\n```"
	got, err := run(text, schema)
	if err != nil {
		t.Fatal(err)
	}
	m := got.(map[string]any)
	if m["summary"] != "Done" || len(m["action_items"].([]any)) != 2 || m["owner"].(map[string]any)["name"] != "Ana" {
		t.Fatalf("parsed: %#v", got)
	}
	if _, kept := m["extra"]; kept {
		t.Fatal("fields outside the schema are dropped")
	}
	if _, err := run(map[string]any{"summary": "already JSON", "action_items": []any{}, "owner": map[string]any{"name": "x"}}, schema); err != nil {
		t.Fatalf("a JSON value needs no parsing: %v", err)
	}
	if got, err := run(`[1, 2]`, nil); err != nil || len(got.([]any)) != 2 {
		// No schema configured: any JSON passes.
		t.Fatalf("no schema: %v %v", got, err)
	}

	for name, tc := range map[string]struct {
		input  any
		schema any
		code   node.ErrorCode
		msg    string
	}{
		"not json":      {"no json here", schema, structured_output.ErrCodeInvalidOutput, "does not contain valid JSON"},
		"missing field": {`{"action_items": [], "owner": {"name": "x"}}`, schema, structured_output.ErrCodeInvalidOutput, "summary: required field is missing"},
		"wrong item":    {`{"summary": "s", "action_items": ["a", 3], "owner": {"name": "x"}}`, schema, structured_output.ErrCodeInvalidOutput, "action_items[1]: expected string, got number"},
		"nested":        {`{"summary": "s", "action_items": [], "owner": {}}`, schema, structured_output.ErrCodeInvalidOutput, "owner.name: required field is missing"},
		"bad schema":    {`{}`, map[string]any{"a": "date"}, structured_output.ErrCodeInvalidSchema, `unknown type "date"`},
		"two-item list": {`{}`, []any{"string", "number"}, structured_output.ErrCodeInvalidSchema, "one element type"},
	} {
		_, err := run(tc.input, tc.schema)
		var ne *node.NodeError
		if !errors.As(err, &ne) || ne.Code != tc.code || ne.Retryable || !contains(ne.Message, tc.msg) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestTransform(t *testing.T) {
	data := map[string]any{"customer": map[string]any{"email": "a@b.c"}, "items": []any{"x", "y"}}
	exec := func(cfg map[string]any) (any, error) {
		out, err := transform.New().Execute(context.Background(), node.NodeInput{
			Ports: map[string]node.Value{"input": node.NewJSONValue(data)}, Config: cfg})
		if err != nil {
			return nil, err
		}
		v, _ := out.GetPort("output")
		return v.Data, nil
	}
	if got, _ := exec(map[string]any{"path": "customer.email"}); got != "a@b.c" {
		t.Fatalf("path: %#v", got)
	}
	if got, _ := exec(map[string]any{"path": "items.1"}); got != "y" {
		t.Fatalf("index: %#v", got)
	}
	if got, _ := exec(map[string]any{"path": "", "expression": "ignored legacy rule"}); got.(map[string]any)["items"] == nil {
		t.Fatalf("empty path passes through: %#v", got)
	}
	if got, _ := exec(map[string]any{"path": "items", "mapping": map[string]any{"title": "resolved"}}); got.(map[string]any)["title"] != "resolved" {
		t.Fatalf("mapping wins: %#v", got)
	}
	if _, err := exec(map[string]any{"path": "nope"}); nodeErrCode(t, err) != node.ErrCodeConfiguration {
		t.Fatal("unknown path is a configuration error")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
