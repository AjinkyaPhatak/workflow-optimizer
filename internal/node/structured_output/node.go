package structured_output

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"workflow-optimizer/internal/node"
)

// NodeType is the stable identifier for Structured Output nodes.
const NodeType = "structured_output"

// Error codes of the Structured Output node. Neither is retryable: the same
// input and schema give the same result.
const (
	ErrCodeInvalidSchema node.ErrorCode = "STRUCTURED_OUTPUT_SCHEMA_INVALID"
	ErrCodeInvalidOutput node.ErrorCode = "STRUCTURED_OUTPUT_INVALID"
)

// Definition returns the canonical metadata definition for a Structured
// Output node.
func Definition() node.NodeDefinition {
	return node.NodeDefinition{
		Type:        NodeType,
		Name:        "Structured Output",
		Description: "Turns text containing JSON (such as an LLM response) into structured data and checks it against a simple schema.",
		Category:    node.CategoryAI,
		Inputs: []node.PortDefinition{
			node.NewPortDefinition("input", node.ValueTypeJSON, true, "Text containing JSON (for example an LLM response), or a JSON value"),
		},
		Outputs: []node.PortDefinition{
			node.NewPortDefinition("output", node.ValueTypeJSON, true, "The data, checked against the schema"),
		},
		Config: []node.ConfigField{
			node.NewConfigField("schema", node.ValueTypeJSON, true, map[string]any{},
				`The expected shape. Use "string", "number", "boolean", "object", "array" or "any"; nest objects; ["string"] is a list of strings; add "?" for optional ("string?"). Example: {"summary": "string", "action_items": ["string"]}`).
				WithLabel("Schema"),
		},
	}
}

// Node parses and validates structured data. It calls no model: pair it with
// an LLM node whose prompt asks for JSON.
type Node struct{}

// New constructs an executable Structured Output node. The argument is
// accepted for wiring compatibility and unused.
func New(_ any) *Node {
	return &Node{}
}

// Type returns the unique node type identifier.
func (n *Node) Type() string {
	return NodeType
}

// Execute parses the input and checks it against the schema.
func (n *Node) Execute(ctx context.Context, in node.NodeInput) (node.NodeOutput, error) {
	if err := ctx.Err(); err != nil {
		return node.NodeOutput{}, err
	}
	schema, set := in.Config["schema"]
	if !set || schema == nil {
		schema = "any" // no schema: any JSON value
	}
	if err := CheckSchema(schema); err != nil {
		return node.NodeOutput{}, node.NewNodeError(ErrCodeInvalidSchema, err.Error(), false)
	}
	v, ok := in.GetPort("input")
	if !ok {
		return node.NodeOutput{}, node.NewNodeError(ErrCodeInvalidOutput, "no input to parse", false)
	}
	data := v.Data
	if s, isStr := data.(string); isStr {
		parsed, err := ParseJSON(s)
		if err != nil {
			return node.NodeOutput{}, node.NewNodeError(ErrCodeInvalidOutput, err.Error(), false)
		}
		data = parsed
	}
	shaped, err := Conform(data, schema, "")
	if err != nil {
		return node.NodeOutput{}, node.NewNodeError(ErrCodeInvalidOutput, err.Error(), false)
	}
	out := node.NewNodeOutput(nil)
	out.SetPort("output", node.NewJSONValue(shaped))
	return out, nil
}

// ParseJSON extracts a JSON value from model text: the whole text, a fenced
// ```json block, or the outermost {...} / [...] in it.
func ParseJSON(text string) (any, error) {
	var v any
	t := strings.TrimSpace(text)
	if json.Unmarshal([]byte(t), &v) == nil {
		return v, nil
	}
	if i := strings.Index(t, "```"); i >= 0 {
		rest := t[i+3:]
		if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
			rest = rest[nl+1:]
		}
		if j := strings.Index(rest, "```"); j >= 0 && json.Unmarshal([]byte(strings.TrimSpace(rest[:j])), &v) == nil {
			return v, nil
		}
	}
	for _, pair := range [][2]string{{"{", "}"}, {"[", "]"}} {
		i, j := strings.Index(t, pair[0]), strings.LastIndex(t, pair[1])
		if i >= 0 && j > i && json.Unmarshal([]byte(t[i:j+1]), &v) == nil {
			return v, nil
		}
	}
	return nil, fmt.Errorf("input does not contain valid JSON")
}

var typeNames = map[string]bool{"string": true, "number": true, "boolean": true, "object": true, "array": true, "any": true}

// CheckSchema reports whether a schema is well formed.
func CheckSchema(schema any) error {
	return checkSchema(schema, "")
}

func checkSchema(s any, path string) error {
	switch t := s.(type) {
	case string:
		if !typeNames[strings.TrimSuffix(t, "?")] {
			return fmt.Errorf("schema%s: unknown type %q", at(path), t)
		}
	case map[string]any:
		for k, sub := range t {
			if err := checkSchema(sub, join(path, k)); err != nil {
				return err
			}
		}
	case []any:
		if len(t) > 1 {
			return fmt.Errorf("schema%s: a list schema has one element type", at(path))
		}
		if len(t) == 1 {
			return checkSchema(t[0], path+"[]")
		}
	default:
		return fmt.Errorf("schema%s: must be a type name, an object or a list", at(path))
	}
	return nil
}

// Conform checks v against the schema and returns it shaped by the schema:
// objects described field by field keep only the described fields.
func Conform(v any, schema any, path string) (any, error) {
	switch s := schema.(type) {
	case string:
		name := strings.TrimSuffix(s, "?")
		if !matches(v, name) {
			return nil, fmt.Errorf("%s: expected %s, got %s", where(path), name, kindOf(v))
		}
		return v, nil
	case map[string]any:
		obj, ok := v.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s: expected object, got %s", where(path), kindOf(v))
		}
		if len(s) == 0 {
			return obj, nil // {}: any object
		}
		keys := make([]string, 0, len(s))
		for k := range s {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := make(map[string]any, len(s))
		for _, k := range keys {
			field, present := obj[k]
			if !present || field == nil {
				if optional(s[k]) {
					continue
				}
				return nil, fmt.Errorf("%s: required field is missing", where(join(path, k)))
			}
			shaped, err := Conform(field, s[k], join(path, k))
			if err != nil {
				return nil, err
			}
			out[k] = shaped
		}
		return out, nil
	case []any:
		arr, ok := v.([]any)
		if !ok {
			return nil, fmt.Errorf("%s: expected list, got %s", where(path), kindOf(v))
		}
		if len(s) == 0 {
			return arr, nil
		}
		out := make([]any, len(arr))
		for i, item := range arr {
			shaped, err := Conform(item, s[0], fmt.Sprintf("%s[%d]", path, i))
			if err != nil {
				return nil, err
			}
			out[i] = shaped
		}
		return out, nil
	}
	return nil, fmt.Errorf("%s: invalid schema", where(path))
}

func optional(s any) bool {
	str, ok := s.(string)
	return ok && strings.HasSuffix(str, "?")
}

func matches(v any, name string) bool {
	switch name {
	case "any":
		return v != nil
	case "string":
		_, ok := v.(string)
		return ok
	case "number":
		switch v.(type) {
		case float64, json.Number, int, int64:
			return true
		}
		return false
	case "boolean":
		_, ok := v.(bool)
		return ok
	case "object":
		_, ok := v.(map[string]any)
		return ok
	case "array":
		_, ok := v.([]any)
		return ok
	}
	return false
}

func kindOf(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case string:
		return "string"
	case float64, json.Number, int, int64:
		return "number"
	case bool:
		return "boolean"
	case map[string]any:
		return "object"
	case []any:
		return "list"
	}
	return fmt.Sprintf("%T", v)
}

func join(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

func where(path string) string {
	if path == "" {
		return "output"
	}
	return path
}

func at(path string) string {
	if path == "" {
		return ""
	}
	return " at " + path
}
