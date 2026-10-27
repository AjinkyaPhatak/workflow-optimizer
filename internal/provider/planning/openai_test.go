package planning

import (
	"context"
	"encoding/json"
	"github.com/openai/openai-go/v3/option"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"workflow-optimizer/internal/automation"
)

func TestPlanSchemaConstrainComparisonsAndControlFlow(t *testing.T) {
	defs := PlanSchema()["$defs"].(map[string]any)
	for _, tc := range []struct {
		definition, field string
		choices           []string
	}{
		{"Condition", "op", automation.ComparisonOperators()},
		{"Step", "kind", []string{"tool", "branch", "parallel", "map", "repeat", "ask", "wait", "workflow"}},
		{"Binding", "source", []string{"literal", "step", "answer", "template", "item", "event"}},
	} {
		props := defs[tc.definition].(map[string]any)["properties"].(map[string]any)
		if !reflect.DeepEqual(props[tc.field].(map[string]any)["enum"], tc.choices) {
			t.Fatalf("%s.%s was not constrained to the runtime contract", tc.definition, tc.field)
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestWriteRejectsMissingSourceBeforeCallingModel(t *testing.T) {
	for _, source := range []string{"", " \n\t"} {
		c := New("test-key", "", "gpt-6-luna")
		called := false
		c.SDK.Responses.Options = append(c.SDK.Responses.Options, option.WithHTTPClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			called = true
			return nil, io.EOF
		})}))
		_, err := c.Write(context.Background(), "Summarize the market results and prepare the email", source, 100)
		if err == nil || called {
			t.Fatalf("missing source reached the model: called=%v err=%v", called, err)
		}
	}
}
func TestBlankEnvironmentEndpointUsesOfficialDefault(t *testing.T) {
	t.Setenv("OPENAI_BASE_URL", "")
	c := New("test-key", "", "gpt-6-luna")
	plan, _ := json.Marshal(automation.Plan{SchemaVersion: 2, Name: "A greeting", Mode: "once"})
	body, _ := json.Marshal(map[string]any{"id": "test_response", "object": "response", "status": "completed", "output": []any{map[string]any{
		"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": string(plan), "annotations": []any{}}},
	}}})
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://api.openai.com/v1/responses" {
			t.Fatalf("unexpected endpoint: %s", r.URL.String())
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Fatal("missing authorization")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(string(body))), Request: r}, nil
	})}
	c.SDK.Responses.Options = append(c.SDK.Responses.Options, option.WithHTTPClient(client))
	out, err := c.Generate(context.Background(), automation.PlanRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if out.Name != "A greeting" {
		t.Fatal("response text was not decoded")
	}
}
