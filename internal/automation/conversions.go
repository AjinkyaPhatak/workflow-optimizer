package automation

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ConversionRules is a small explicit catalog, also exposed to the planner/UI.
// Every inserted conversion remains visible as a normal workflow step.
type ConversionRule struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Tool   string `json:"tool"`
	Input  string `json:"input"`
	Output string `json:"output"`
}

var ConversionRules = []ConversionRule{
	{"number", "string", "value.text", "value", "text"},
	{"boolean", "string", "value.text", "value", "text"},
	{"object", "string", "value.text", "value", "text"},
	{"array", "string", "value.text", "value", "text"},
}

func PortType(field Field, path []string) string {
	if len(path) == 0 {
		return field.Type
	}
	if field.Name == "artifact" && field.Type == "object" {
		switch path[0] {
		case "id", "name", "content_type":
			return "string"
		case "size":
			return "number"
		}
	}
	// An unspecified nested property is checked at runtime, not guessed.
	if field.Type == "object" || field.Type == "array" || field.Type == "json" {
		return "json"
	}
	return ""
}
func InsertConversions(plan Plan, tools []ToolDescriptor) Plan {
	plan = ClonePlan(plan)
	catalog := map[string]ToolDescriptor{}
	for _, t := range tools {
		catalog[t.ID] = t
	}
	known := map[string]ToolDescriptor{}
	used := map[string]bool{}
	var collect func([]Step)
	collect = func(steps []Step) {
		for _, s := range steps {
			used[s.ID] = true
			collect(s.Children)
			collect(s.Otherwise)
		}
	}
	collect(plan.Steps)
	var walk func([]Step, map[string]ToolDescriptor) []Step
	walk = func(steps []Step, available map[string]ToolDescriptor) []Step {
		out := []Step{}
		for _, s := range steps {
			target := catalog[s.Tool]
			for i, b := range s.Inputs {
				if b.Source != "step" {
					continue
				}
				parts := strings.Split(b.Ref, ".")
				if len(parts) < 2 {
					continue
				}
				source, ok := available[parts[0]]
				if !ok {
					continue
				}
				var from, to string
				for _, f := range source.Outputs {
					if f.Name == parts[1] {
						from = PortType(f, parts[2:])
					}
				}
				for _, f := range target.Inputs {
					if f.Name == b.Name {
						to = f.Type
					}
				}
				for _, rule := range ConversionRules {
					if rule.From != from || rule.To != to {
						continue
					}
					if _, ok := catalog[rule.Tool]; !ok {
						continue
					}
					id := "convert_" + s.ID + "_" + strings.NewReplacer(".", "_", " ", "_").Replace(b.Name)
					if len(id) > 70 {
						id = id[:70]
					}
					if used[id] {
						continue
					}
					used[id] = true
					input := b
					input.Name = rule.Input
					convert := Step{ID: id, Name: "Format the result as readable text", Kind: "tool", Tool: rule.Tool, Inputs: []Binding{input}, Children: []Step{}, Otherwise: []Step{}}
					out = append(out, convert)
					available[id] = catalog[rule.Tool]
					s.Inputs[i] = Binding{Name: b.Name, Source: "step", Ref: id + "." + rule.Output}
					break
				}
			}
			if s.Kind == "parallel" {
				for i, child := range s.Children {
					nested := walk([]Step{child}, copyCatalog(available))
					if len(nested) == 1 {
						s.Children[i] = nested[0]
					}
				}
			} else {
				s.Children = walk(s.Children, copyCatalog(available))
			}
			s.Otherwise = walk(s.Otherwise, copyCatalog(available))
			out = append(out, s)
			if s.Kind == "tool" {
				available[s.ID] = target
			} else {
				available[s.ID] = genericOutput()
			}
		}
		return out
	}
	plan.Steps = walk(plan.Steps, known)
	return plan
}
func WithSchemas(tools []ToolDescriptor) []ToolDescriptor {
	typeSchema := func(kind string) map[string]any {
		switch kind {
		case "string", "number", "boolean", "object", "array":
		default:
			kind = ""
		}
		if kind == "" {
			return map[string]any{}
		}
		schema := map[string]any{"type": kind}
		if kind == "array" {
			schema["items"] = map[string]any{}
		}
		return schema
	}
	for i := range tools {
		build := func(fields []Field) map[string]any {
			props := map[string]any{}
			required := []string{}
			for _, f := range fields {
				schema := typeSchema(f.Type)
				schema["description"] = f.Description
				if len(f.Options) > 0 {
					values := []string{}
					for _, o := range f.Options {
						values = append(values, o.Value)
					}
					schema["enum"] = values
				}
				if f.Name == "artifact" {
					schema = map[string]any{"type": "object", "required": []string{"id", "name", "content_type", "size"}, "properties": map[string]any{"id": map[string]any{"type": "string"}, "name": map[string]any{"type": "string"}, "content_type": map[string]any{"type": "string"}, "size": map[string]any{"type": "number"}}}
				}
				props[f.Name] = schema
				if f.Required {
					required = append(required, f.Name)
				}
			}
			return map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}
		}
		tools[i].InputSchema = build(tools[i].Inputs)
		tools[i].OutputSchema = build(tools[i].Outputs)
	}
	return tools
}
func LiteralType(value any, kind string) bool {
	switch kind {
	case "string":
		_, ok := value.(string)
		return ok
	case "number":
		_, ok := value.(float64)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	case "object":
		_, ok := value.(map[string]any)
		return ok
	default:
		return true
	}
}
func ValidateLiteral(b Binding, f Field) error {
	if b.Source != "literal" {
		return nil
	}
	var v any
	if err := json.Unmarshal([]byte(b.ValueJSON), &v); err != nil {
		return err
	}
	if v == nil || v == "" {
		if f.Required {
			return fmt.Errorf("add a value for %s", f.Name)
		}
		return nil
	}
	if !LiteralType(v, f.Type) {
		return fmt.Errorf("%s needs a %s value or an explicit conversion", f.Name, f.Type)
	}
	if n, ok := v.(float64); ok && ((f.Min != nil && n < *f.Min) || (f.Max != nil && n > *f.Max)) {
		return fmt.Errorf("%s is outside the allowed range", f.Name)
	}
	if len(f.Options) > 0 && !f.AllowCustom {
		for _, o := range f.Options {
			if fmt.Sprint(v) == o.Value {
				return nil
			}
		}
		return fmt.Errorf("choose a supported value for %s", f.Name)
	}
	return nil
}
