package node_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"workflow-optimizer/internal/node"
	"workflow-optimizer/internal/node/condition"
	"workflow-optimizer/internal/node/http"
	"workflow-optimizer/internal/node/input"
	nodejson "workflow-optimizer/internal/node/json"
	"workflow-optimizer/internal/node/llm"
	"workflow-optimizer/internal/node/merge"
	"workflow-optimizer/internal/node/output"
	"workflow-optimizer/internal/node/prompt"
	"workflow-optimizer/internal/node/structured_output"
	"workflow-optimizer/internal/node/text"
	"workflow-optimizer/internal/node/transform"
	providerllm "workflow-optimizer/internal/provider/llm"
)

// Mock Node implementation for testing core interface and cancellation.
type mockNode struct {
	nodeType string
	delay    time.Duration
}

func (m *mockNode) Type() string {
	return m.nodeType
}

func (m *mockNode) Execute(ctx context.Context, in node.NodeInput) (node.NodeOutput, error) {
	if m.delay > 0 {
		select {
		case <-ctx.Done():
			return node.NodeOutput{}, ctx.Err()
		case <-time.After(m.delay):
		}
	}
	if err := ctx.Err(); err != nil {
		return node.NodeOutput{}, err
	}

	out := node.NewNodeOutput(nil)
	if val, ok := in.GetPort("in"); ok {
		out.SetPort("out", val)
	}
	return out, nil
}

// Ensure mockNode implements node.Node.
var _ node.Node = (*mockNode)(nil)

func TestNodeInterface(t *testing.T) {
	n := &mockNode{nodeType: "test_node"}

	if n.Type() != "test_node" {
		t.Fatalf("Type() = %q, want %q", n.Type(), "test_node")
	}

	ctx := context.Background()
	in := node.NodeInput{
		Ports: map[string]node.Value{
			"in": node.NewStringValue("hello"),
		},
		Config: map[string]any{
			"key": "value",
		},
	}

	out, err := n.Execute(ctx, in)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	val, ok := out.GetPort("out")
	if !ok {
		t.Fatal("expected 'out' port in output")
	}
	s, ok := val.String()
	if !ok || s != "hello" {
		t.Fatalf("out value = %v, want 'hello'", s)
	}
}

func TestNodeRegistry(t *testing.T) {
	reg := node.NewRegistry()

	// Register valid node
	n1 := &mockNode{nodeType: "node_1"}
	if err := reg.Register(n1); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	// Duplicate registration must fail
	if err := reg.Register(n1); !errors.Is(err, node.ErrNodeAlreadyRegistered) {
		t.Fatalf("duplicate Register err = %v, want %v", err, node.ErrNodeAlreadyRegistered)
	}

	// Register another node with same type must fail
	n1Dup := &mockNode{nodeType: "node_1"}
	if err := reg.Register(n1Dup); !errors.Is(err, node.ErrNodeAlreadyRegistered) {
		t.Fatalf("duplicate type Register err = %v, want %v", err, node.ErrNodeAlreadyRegistered)
	}

	// Register invalid nodes
	if err := reg.Register(nil); !errors.Is(err, node.ErrInvalidNode) {
		t.Fatalf("nil Register err = %v, want %v", err, node.ErrInvalidNode)
	}
	if err := reg.Register(&mockNode{nodeType: ""}); !errors.Is(err, node.ErrInvalidNode) {
		t.Fatalf("empty type Register err = %v, want %v", err, node.ErrInvalidNode)
	}

	// Successful lookup
	retrieved, err := reg.Get("node_1")
	if err != nil {
		t.Fatalf("Get('node_1') err = %v", err)
	}
	if retrieved.Type() != "node_1" {
		t.Fatalf("retrieved.Type() = %q, want 'node_1'", retrieved.Type())
	}

	// Unknown lookup
	_, err = reg.Get("unknown")
	if !errors.Is(err, node.ErrNodeNotFound) {
		t.Fatalf("Get('unknown') err = %v, want %v", err, node.ErrNodeNotFound)
	}

	// List
	n2 := &mockNode{nodeType: "node_2"}
	_ = reg.Register(n2)
	list := reg.List()
	if len(list) != 2 || list[0] != "node_1" || list[1] != "node_2" {
		t.Fatalf("List() = %v, want ['node_1', 'node_2']", list)
	}
}

func TestRegistryConcurrentAccess(t *testing.T) {
	reg := node.NewRegistry()
	n := &mockNode{nodeType: "concurrent_node"}
	if err := reg.Register(n); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	const goroutines = 50
	const iterations = 100
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				resolved, err := reg.Get("concurrent_node")
				if err != nil || resolved.Type() != "concurrent_node" {
					t.Errorf("concurrent Get failed: %v", err)
				}
				_, _ = reg.Get("non_existent")
				_ = reg.List()
			}
		}()
	}

	wg.Wait()
}

func TestDefinitionRegistry(t *testing.T) {
	reg := node.NewDefinitionRegistry()

	def := node.NodeDefinition{
		Type:        "test_def",
		Name:        "Test Def",
		Description: "A test definition",
		Category:    node.CategoryGeneral,
		Inputs: []node.PortDefinition{
			node.NewPortDefinition("in", node.ValueTypeString, true, "input string"),
		},
		Outputs: []node.PortDefinition{
			node.NewPortDefinition("out", node.ValueTypeString, true, "output string"),
		},
		Config: []node.ConfigField{
			node.NewConfigField("cfg", node.ValueTypeString, false, "default", "config field"),
		},
	}

	if err := reg.Register(def); err != nil {
		t.Fatalf("Register def failed: %v", err)
	}

	// Duplicate
	if err := reg.Register(def); !errors.Is(err, node.ErrDefinitionAlreadyRegistered) {
		t.Fatalf("duplicate Register def err = %v, want %v", err, node.ErrDefinitionAlreadyRegistered)
	}

	// Lookup
	retrieved, err := reg.Get("test_def")
	if err != nil {
		t.Fatalf("Get('test_def') err = %v", err)
	}
	if retrieved.Type != "test_def" || retrieved.Name != "Test Def" {
		t.Fatalf("retrieved def mismatch: %#v", retrieved)
	}

	// Unknown
	_, err = reg.Get("unknown")
	if !errors.Is(err, node.ErrDefinitionNotFound) {
		t.Fatalf("Get('unknown') err = %v, want %v", err, node.ErrDefinitionNotFound)
	}

	// Definition validation checks
	invalidDefs := []struct {
		name string
		def  node.NodeDefinition
	}{
		{"empty type", node.NodeDefinition{Name: "Name"}},
		{"empty name", node.NodeDefinition{Type: "type"}},
		{"empty input port name", node.NodeDefinition{Type: "t", Name: "n", Inputs: []node.PortDefinition{{}}}},
		{"duplicate input port", node.NodeDefinition{Type: "t", Name: "n", Inputs: []node.PortDefinition{
			node.NewPortDefinition("p", node.ValueTypeString, false, ""),
			node.NewPortDefinition("p", node.ValueTypeNumber, false, ""),
		}}},
		{"empty output port name", node.NodeDefinition{Type: "t", Name: "n", Outputs: []node.PortDefinition{{}}}},
		{"duplicate output port", node.NodeDefinition{Type: "t", Name: "n", Outputs: []node.PortDefinition{
			node.NewPortDefinition("p", node.ValueTypeString, false, ""),
			node.NewPortDefinition("p", node.ValueTypeNumber, false, ""),
		}}},
		{"empty config name", node.NodeDefinition{Type: "t", Name: "n", Config: []node.ConfigField{{}}}},
		{"duplicate config name", node.NodeDefinition{Type: "t", Name: "n", Config: []node.ConfigField{
			node.NewConfigField("c", node.ValueTypeString, false, nil, ""),
			node.NewConfigField("c", node.ValueTypeNumber, false, nil, ""),
		}}},
	}

	for _, tc := range invalidDefs {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.def.Validate(); !errors.Is(err, node.ErrInvalidDefinition) {
				t.Fatalf("Validate() = %v, want ErrInvalidDefinition", err)
			}
		})
	}
}

func TestValueAbstraction(t *testing.T) {
	// String
	vStr := node.NewStringValue("test string")
	if s, ok := vStr.String(); !ok || s != "test string" {
		t.Fatalf("String() = %v, %v", s, ok)
	}
	if vStr.Type != node.ValueTypeString {
		t.Fatalf("Type = %v, want %v", vStr.Type, node.ValueTypeString)
	}

	// Number
	vNum := node.NewNumberValue(42.5)
	if n, ok := vNum.Number(); !ok || n != 42.5 {
		t.Fatalf("Number() = %v, %v", n, ok)
	}

	// Boolean
	vBool := node.NewBooleanValue(true)
	if b, ok := vBool.Boolean(); !ok || !b {
		t.Fatalf("Boolean() = %v, %v", b, ok)
	}

	// Object
	vObj := node.NewObjectValue(map[string]any{"foo": "bar"})
	if obj, ok := vObj.Object(); !ok || obj["foo"] != "bar" {
		t.Fatalf("Object() = %v, %v", obj, ok)
	}

	// Array
	vArr := node.NewArrayValue([]any{"a", "b", "c"})
	if arr, ok := vArr.Array(); !ok || len(arr) != 3 {
		t.Fatalf("Array() = %v, %v", arr, ok)
	}

	// JSON
	vJSON := node.NewJSONValue(map[string]any{"status": 200})
	if vJSON.Type != node.ValueTypeJSON {
		t.Fatalf("JSON Type = %v", vJSON.Type)
	}

	// Binary
	vBin := node.NewBinaryValue([]byte("hello binary"))
	if b, ok := vBin.Binary(); !ok || string(b) != "hello binary" {
		t.Fatalf("Binary() = %v, %v", string(b), ok)
	}

	// Extensibility: Future custom types without changing contracts
	vCustom := node.NewValue("embedding", []float64{0.1, 0.2, 0.3})
	if vCustom.Type != "embedding" {
		t.Fatalf("custom type = %v", vCustom.Type)
	}

	// JSON round-trip
	data, err := json.Marshal(vStr)
	if err != nil {
		t.Fatalf("marshal Value: %v", err)
	}
	var decoded node.Value
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal Value: %v", err)
	}
	if decoded.Type != vStr.Type || decoded.Data != vStr.Data {
		t.Fatalf("decoded Value mismatch: %#v vs %#v", decoded, vStr)
	}
}

func TestPortAndConfigDefinitionJSONRoundTrip(t *testing.T) {
	port := node.NewPortDefinition("test_port", node.ValueTypeString, true, "description")
	portData, err := json.Marshal(port)
	if err != nil {
		t.Fatalf("marshal port: %v", err)
	}
	var decodedPort node.PortDefinition
	if err := json.Unmarshal(portData, &decodedPort); err != nil {
		t.Fatalf("unmarshal port: %v", err)
	}
	if decodedPort != port {
		t.Fatalf("port mismatch: %#v vs %#v", decodedPort, port)
	}

	cfg := node.NewConfigField("model", node.ValueTypeString, true, "gpt-5", "LLM model")
	cfgData, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal cfg: %v", err)
	}
	var decodedCfg node.ConfigField
	if err := json.Unmarshal(cfgData, &decodedCfg); err != nil {
		t.Fatalf("unmarshal cfg: %v", err)
	}
	if decodedCfg.Name != cfg.Name || decodedCfg.Type != cfg.Type || decodedCfg.Required != cfg.Required {
		t.Fatalf("cfg mismatch: %#v vs %#v", decodedCfg, cfg)
	}
}

func TestContextCancellation(t *testing.T) {
	n := &mockNode{nodeType: "slow_node", delay: 200 * time.Millisecond}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	in := node.NodeInput{}
	_, err := n.Execute(ctx, in)
	if err == nil {
		t.Fatal("expected error on cancelled context, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestNodeConcurrency(t *testing.T) {
	// The same node instance serving multiple concurrent executions
	n := &mockNode{nodeType: "concurrency_test_node"}

	const goroutines = 50
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func(id int) {
			defer wg.Done()
			inputStr := fmt.Sprintf("message-%d", id)
			in := node.NodeInput{
				Ports: map[string]node.Value{
					"in": node.NewStringValue(inputStr),
				},
			}
			out, err := n.Execute(context.Background(), in)
			if err != nil {
				t.Errorf("execution %d failed: %v", id, err)
				return
			}
			val, ok := out.GetPort("out")
			if !ok {
				t.Errorf("execution %d missing out port", id)
				return
			}
			s, _ := val.String()
			if s != inputStr {
				t.Errorf("execution %d output mismatch: got %q, want %q", id, s, inputStr)
			}
		}(i)
	}

	wg.Wait()
}

func TestNodeErrorModel(t *testing.T) {
	cause := errors.New("upstream connection reset")
	nodeErr := node.WrapNodeError(node.ErrCodeUnavailable, "service unavailable", true, cause)
	nodeErr.WithDetails(map[string]any{"retry_after": 5})

	if nodeErr.Code != node.ErrCodeUnavailable {
		t.Fatalf("Code = %v", nodeErr.Code)
	}
	if !nodeErr.Retryable {
		t.Fatal("expected Retryable = true")
	}
	if !node.IsRetryable(nodeErr) {
		t.Fatal("expected IsRetryable(nodeErr) = true")
	}

	// Go error wrapping chain
	if !errors.Is(nodeErr, cause) {
		t.Fatalf("expected errors.Is(nodeErr, cause) to be true")
	}

	var target *node.NodeError
	if !errors.As(nodeErr, &target) {
		t.Fatal("expected errors.As to succeed")
	}
	if target.Details["retry_after"] != 5 {
		t.Fatalf("details not preserved: %#v", target.Details)
	}

	// Non-retryable error
	nonRetryable := node.NewNodeError(node.ErrCodeInvalidInput, "bad parameter", false)
	if node.IsRetryable(nonRetryable) {
		t.Fatal("expected IsRetryable(nonRetryable) = false")
	}
	if node.IsRetryable(errors.New("generic error")) {
		t.Fatal("expected IsRetryable(generic error) = false")
	}
}

func TestAllInitialV1NodeContracts(t *testing.T) {
	ctx := context.Background()

	// 1. Input Node
	t.Run("input", func(t *testing.T) {
		def := input.Definition()
		if def.Type != input.NodeType || def.Category != node.CategoryGeneral {
			t.Fatalf("invalid input definition: %#v", def)
		}
		if err := def.Validate(); err != nil {
			t.Fatalf("def.Validate() failed: %v", err)
		}
		n := input.New()
		out, err := n.Execute(ctx, node.NodeInput{
			Config: map[string]any{"trigger": "webhook"},
		})
		if err != nil {
			t.Fatalf("execute failed: %v", err)
		}
		if _, ok := out.GetPort("data"); !ok {
			t.Fatal("missing 'data' output port")
		}
	})

	// 2. Output Node
	t.Run("output", func(t *testing.T) {
		def := output.Definition()
		if def.Type != output.NodeType {
			t.Fatalf("invalid output type: %q", def.Type)
		}
		if err := def.Validate(); err != nil {
			t.Fatalf("invalid output def: %v", err)
		}
		n := output.New()
		out, err := n.Execute(ctx, node.NodeInput{
			Ports: map[string]node.Value{"value": node.NewStringValue("done")},
		})
		if err != nil {
			t.Fatalf("execute failed: %v", err)
		}
		res, ok := out.GetPort("result")
		if !ok || res.Data != "done" {
			t.Fatalf("unexpected output: %#v", out)
		}
	})

	// 3. Text Node
	t.Run("text", func(t *testing.T) {
		def := text.Definition()
		if err := def.Validate(); err != nil {
			t.Fatalf("invalid text def: %v", err)
		}
		n := text.New()
		out, err := n.Execute(ctx, node.NodeInput{
			Config: map[string]any{"text": "static hello"},
		})
		if err != nil {
			t.Fatalf("execute failed: %v", err)
		}
		val, _ := out.GetPort("output")
		if s, _ := val.String(); s != "static hello" {
			t.Fatalf("expected 'static hello', got %q", s)
		}
	})

	// 4. JSON Node
	t.Run("json", func(t *testing.T) {
		def := nodejson.Definition()
		if err := def.Validate(); err != nil {
			t.Fatalf("invalid json def: %v", err)
		}
		n := nodejson.New()
		out, err := n.Execute(ctx, node.NodeInput{
			Ports: map[string]node.Value{"input": node.NewJSONValue(map[string]any{"num": 10})},
		})
		if err != nil {
			t.Fatalf("execute failed: %v", err)
		}
		if _, ok := out.GetPort("output"); !ok {
			t.Fatal("missing output port")
		}
	})

	// 5. Transform Node
	t.Run("transform", func(t *testing.T) {
		def := transform.Definition()
		if err := def.Validate(); err != nil {
			t.Fatalf("invalid transform def: %v", err)
		}
		n := transform.New()
		out, err := n.Execute(ctx, node.NodeInput{
			Ports: map[string]node.Value{"input": node.NewJSONValue("raw")},
		})
		if err != nil {
			t.Fatalf("execute failed: %v", err)
		}
		if _, ok := out.GetPort("output"); !ok {
			t.Fatal("missing output port")
		}
	})

	// 6. Condition Node
	t.Run("condition", func(t *testing.T) {
		def := condition.Definition()
		if err := def.Validate(); err != nil {
			t.Fatalf("invalid condition def: %v", err)
		}
		n := condition.New()
		// Test true branch
		outTrue, err := n.Execute(ctx, node.NodeInput{
			Ports: map[string]node.Value{"value": node.NewBooleanValue(true)},
		})
		if err != nil {
			t.Fatalf("execute failed: %v", err)
		}
		if _, ok := outTrue.GetPort("true"); !ok {
			t.Fatal("missing 'true' output port")
		}

		// Test false branch
		outFalse, err := n.Execute(ctx, node.NodeInput{
			Ports: map[string]node.Value{"value": node.NewBooleanValue(false)},
		})
		if err != nil {
			t.Fatalf("execute failed: %v", err)
		}
		if _, ok := outFalse.GetPort("false"); !ok {
			t.Fatal("missing 'false' output port")
		}
	})

	// 7. Merge Node
	t.Run("merge", func(t *testing.T) {
		def := merge.Definition()
		if err := def.Validate(); err != nil {
			t.Fatalf("invalid merge def: %v", err)
		}
		n := merge.New()
		out, err := n.Execute(ctx, node.NodeInput{
			Ports: map[string]node.Value{
				"left":  node.NewStringValue("leftVal"),
				"right": node.NewStringValue("rightVal"),
			},
		})
		if err != nil {
			t.Fatalf("execute failed: %v", err)
		}
		mergedVal, ok := out.GetPort("merged")
		if !ok {
			t.Fatal("missing 'merged' port")
		}
		m, ok := mergedVal.Object()
		if !ok || m["left"] != "leftVal" || m["right"] != "rightVal" {
			t.Fatalf("unexpected merged payload: %#v", m)
		}
	})

	// 8. Prompt Node
	t.Run("prompt", func(t *testing.T) {
		def := prompt.Definition()
		if err := def.Validate(); err != nil {
			t.Fatalf("invalid prompt def: %v", err)
		}
		n := prompt.New()
		out, err := n.Execute(ctx, node.NodeInput{
			Config: map[string]any{"template": "Hello {{name}}"},
		})
		if err != nil {
			t.Fatalf("execute failed: %v", err)
		}
		promptVal, _ := out.GetPort("prompt")
		if s, _ := promptVal.String(); s != "Hello {{name}}" {
			t.Fatalf("unexpected prompt: %q", s)
		}
	})

	// 9. LLM Node
	t.Run("llm", func(t *testing.T) {
		def := llm.Definition()
		if err := def.Validate(); err != nil {
			t.Fatalf("invalid llm def: %v", err)
		}
		mockProv := &mockLLMProvider{responseText: "AI generated response"}
		n := llm.New(mockProv)
		out, err := n.Execute(ctx, node.NodeInput{
			Ports: map[string]node.Value{
				"prompt": node.NewStringValue("Tell me a joke"),
			},
			Config: map[string]any{"model": "gpt-5"},
		})
		if err != nil {
			t.Fatalf("execute failed: %v", err)
		}
		respVal, ok := out.GetPort("response")
		if !ok {
			t.Fatal("missing 'response' port")
		}
		s, _ := respVal.String()
		if s != "AI generated response" {
			t.Fatalf("got %q, want 'AI generated response'", s)
		}
	})

	// 10. Structured Output Node
	t.Run("structured_output", func(t *testing.T) {
		def := structured_output.Definition()
		if err := def.Validate(); err != nil {
			t.Fatalf("invalid structured output def: %v", err)
		}
		n := structured_output.New(nil)
		out, err := n.Execute(ctx, node.NodeInput{
			Ports: map[string]node.Value{
				"prompt": node.NewStringValue("Extract items"),
			},
		})
		if err != nil {
			t.Fatalf("execute failed: %v", err)
		}
		if _, ok := out.GetPort("output"); !ok {
			t.Fatal("missing 'output' port")
		}
	})

	// 11. HTTP Node
	t.Run("http", func(t *testing.T) {
		def := http.Definition()
		if err := def.Validate(); err != nil {
			t.Fatalf("invalid http def: %v", err)
		}
		n := http.New()
		out, err := n.Execute(ctx, node.NodeInput{
			Config: map[string]any{"url": "https://api.example.com", "method": "GET"},
		})
		if err != nil {
			t.Fatalf("execute failed: %v", err)
		}
		statusVal, ok := out.GetPort("status_code")
		if !ok {
			t.Fatal("missing status_code port")
		}
		num, _ := statusVal.Number()
		if num != 200 {
			t.Fatalf("status_code = %v, want 200", num)
		}
	})
}

type mockLLMProvider struct {
	responseText string
	err          error
}

func (m *mockLLMProvider) Generate(ctx context.Context, req providerllm.Request) (providerllm.Response, error) {
	if m.err != nil {
		return providerllm.Response{}, m.err
	}
	return providerllm.Response{
		Text:         m.responseText,
		FinishReason: "stop",
		InputTokens:  10,
		OutputTokens: 25,
	}, nil
}
