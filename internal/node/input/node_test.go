package input_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"workflow-optimizer/internal/node"
	"workflow-optimizer/internal/node/input"
)

func run(t *testing.T, cfg map[string]any, data any) (any, error) {
	t.Helper()
	in := node.NodeInput{Config: cfg, Ports: map[string]node.Value{}}
	if data != nil {
		in.Ports["data"] = node.NewJSONValue(data)
	}
	out, err := input.New().Execute(context.Background(), in)
	if err != nil {
		return nil, err
	}
	v, _ := out.GetPort("data")
	return v.Data, nil
}

// Phase I: with a Field name, an Input passes on that one field.
func TestFieldName(t *testing.T) {
	data := map[string]any{"topic": "DAGs", "file": "f-1"}
	if v, err := run(t, map[string]any{"field": " topic ", "field_type": "text"}, data); err != nil || v != "DAGs" {
		t.Fatalf("%v %v", v, err)
	}
	// Without one, the whole run input (as before).
	if v, err := run(t, map[string]any{"field_type": "text"}, data); err != nil || !reflect.DeepEqual(v, data) {
		t.Fatalf("%v %v", v, err)
	}
	// A missing or empty field says what to fill in.
	for _, d := range []any{map[string]any{}, map[string]any{"topic": ""}, nil} {
		_, err := run(t, map[string]any{"field": "topic"}, d)
		var ne *node.NodeError
		if !errors.As(err, &ne) || ne.Code != node.ErrCodeInvalidInput || ne.Message != `fill in "topic" to run this workflow` {
			t.Fatalf("%v: %v", d, err)
		}
	}
	// Its own settings are never passed on as data.
	if v, err := run(t, map[string]any{"field_type": "text"}, nil); err != nil || !reflect.DeepEqual(v, map[string]any{}) {
		t.Fatalf("%v %v", v, err)
	}
	def := input.Definition()
	if len(def.Config) != 2 || def.Config[0].Label != "Field name" || len(def.Config[1].Options) != 2 {
		t.Fatalf("%+v", def.Config)
	}
}
