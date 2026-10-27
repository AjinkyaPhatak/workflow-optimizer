package toolbox

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"workflow-optimizer/internal/automation"
	"workflow-optimizer/internal/templates"
	"workflow-optimizer/internal/workflow"
)

var templateReference = regexp.MustCompile(`\{\{\s*([^{}]+?)\s*\}\}`)

// BuiltinTemplates deterministically ports the shipped templates. It does not
// ask a model to reinterpret their actions or call any external service.
func (t *Toolbox) BuiltinTemplates() ([]automation.WorkflowTemplate, error) {
	tools := t.Catalog()
	out := []automation.WorkflowTemplate{}
	for _, original := range templates.List() {
		plan, layout, err := convertTemplate(original, tools)
		if err != nil {
			return nil, fmt.Errorf("template %s: %w", original.ID, err)
		}
		out = append(out, automation.WorkflowTemplate{ID: original.ID, Name: original.Name, Description: original.Description, Category: original.Category, Builtin: true, Plan: plan, Layout: layout})
	}
	return out, nil
}

func convertTemplate(original templates.Template, tools []ToolDescriptor) (automation.Plan, automation.EditorLayout, error) {
	plan := automation.EmptyPlan(original.Name)
	plan.Summary = original.Description
	layout := automation.EditorLayout{Positions: map[string]automation.Position{}}
	nodes := map[string]workflow.Node{}
	catalog := map[string]ToolDescriptor{}
	questions := map[string]bool{}
	for _, n := range original.Definition.Nodes {
		nodes[n.ID] = n
	}
	for _, tool := range tools {
		catalog[tool.ID] = tool
	}
	literal := func(value any) automation.Binding {
		b, _ := json.Marshal(value)
		return automation.Binding{Source: "literal", ValueJSON: string(b)}
	}
	question := func(id, label, kind string) automation.Binding {
		id = "input_" + strings.NewReplacer(".", "_", " ", "_", "-", "_").Replace(id)
		if !questions[id] {
			questions[id] = true
			plan.Questions = append(plan.Questions, automation.Question{ID: id, Prompt: "What should I use for " + strings.ReplaceAll(label, "_", " ") + "?", Kind: kind, Required: true, Options: []automation.Option{}})
		}
		return automation.Binding{Source: "answer", Ref: id}
	}
	var source func(string, string) automation.Binding
	source = func(id, port string) automation.Binding {
		n := nodes[id]
		if n.Type == "input" {
			field, _ := n.Config["field"].(string)
			if field == "" && port == "data" {
				return literal(map[string]any{})
			}
			if field == "" {
				field = port
			}
			kind := "text"
			if strings.Contains(field, "file_id") {
				kind = "file"
			}
			return question(id+"_"+field, n.Name+" ("+field+")", kind)
		}
		if n.Type == "llm" && port == "response" {
			port = "text"
		}
		return automation.Binding{Source: "step", Ref: id + "." + port}
	}
	resolve := func(n workflow.Node, token string) automation.Binding {
		for _, edge := range original.Definition.Edges {
			if edge.Target == n.ID && edge.TargetPort == "var:"+token {
				return source(edge.Source, edge.SourcePort)
			}
		}
		root, path, ok := strings.Cut(token, ".")
		if ok {
			if _, found := nodes[root]; found {
				return source(root, path)
			}
		}
		for _, v := range original.Definition.Variables {
			if v.Name == token {
				if v.Default != nil {
					return literal(v.Default)
				}
				kind := string(v.Type)
				if kind != "number" && kind != "boolean" {
					kind = "text"
				}
				return question(v.Name, v.Name, kind)
			}
		}
		for _, input := range original.Definition.Nodes {
			if input.Type == "input" && input.Config["field"] == token {
				return source(input.ID, "data")
			}
		}
		return question(token, token, "text")
	}
	counter := 0
	bind := func(n workflow.Node, key string, value any) automation.Binding {
		if text, ok := value.(string); ok {
			match := templateReference.FindStringSubmatch(text)
			if len(match) > 0 && match[0] == text {
				b := resolve(n, strings.TrimSpace(match[1]))
				b.Name = key
				return b
			}
		}
		tokens := []string{}
		seen := map[string]bool{}
		var prepare func(any) any
		prepare = func(value any) any {
			switch v := value.(type) {
			case string:
				return templateReference.ReplaceAllStringFunc(v, func(match string) string {
					token := strings.TrimSpace(templateReference.FindStringSubmatch(match)[1])
					if !seen[token] {
						seen[token] = true
						tokens = append(tokens, token)
					}
					return "$(" + token + ")"
				})
			case map[string]any:
				out := map[string]any{}
				keys := make([]string, 0, len(v))
				for k := range v {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				for _, k := range keys {
					out[k] = prepare(v[k])
				}
				return out
			case []any:
				out := make([]any, len(v))
				for i, child := range v {
					out[i] = prepare(child)
				}
				return out
			default:
				return value
			}
		}
		b := literal(prepare(value))
		for _, token := range tokens {
			counter++
			id := fmt.Sprintf("fill_%s_%d", n.ID, counter)
			b.Name = "template"
			name := literal(token)
			name.Name = "name"
			replacement := resolve(n, token)
			replacement.Name = "value"
			plan.Steps = append(plan.Steps, automation.Step{ID: id, Name: "Fill " + strings.ReplaceAll(token, "_", " "), Kind: "tool", Tool: "value.bind", Inputs: []automation.Binding{b, name, replacement}, Children: []automation.Step{}, Otherwise: []automation.Step{}})
			b = automation.Binding{Source: "step", Ref: id + ".result"}
		}
		b.Name = key
		return b
	}
	for _, n := range original.Definition.Nodes {
		if n.Type == "input" || n.Type == "output" {
			continue
		}
		if n.Settings != nil && (n.Settings.OnError != "" || n.Settings.RetryAttempts != nil || n.Settings.RepeatOver != "") {
			return plan, layout, fmt.Errorf("unsupported execution settings on %s", n.ID)
		}
		tool := "node." + n.Type
		if n.Type == "llm" {
			tool = "ai.write"
		}
		descriptor, ok := catalog[tool]
		if !ok {
			return plan, layout, fmt.Errorf("missing tool %s", tool)
		}
		step := automation.Step{ID: n.ID, Name: n.Name, Kind: "tool", Tool: tool, Inputs: []automation.Binding{}, Children: []automation.Step{}, Otherwise: []automation.Step{}}
		fields := map[string]Field{}
		for _, f := range descriptor.Inputs {
			fields[f.Name] = f
		}
		set := func(b automation.Binding) {
			for i, old := range step.Inputs {
				if old.Name == b.Name {
					step.Inputs[i] = b
					return
				}
			}
			step.Inputs = append(step.Inputs, b)
		}
		keys := make([]string, 0, len(n.Config))
		for key := range n.Config {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			name := "config." + key
			if n.Type == "llm" {
				if key != "prompt" {
					continue
				}
				name = "text"
			}
			if _, known := fields[name]; known {
				set(bind(n, name, n.Config[key]))
			}
		}
		for _, edge := range original.Definition.Edges {
			if edge.Target != n.ID || strings.HasPrefix(edge.TargetPort, "var:") {
				continue
			}
			name := "port." + edge.TargetPort
			if n.Type == "llm" {
				if edge.TargetPort != "prompt" {
					continue
				}
				name = "text"
			}
			f, known := fields[name]
			if !known {
				continue
			}
			from := nodes[edge.Source]
			if from.Type == "input" && from.Config["field"] == nil && !f.Required {
				continue
			}
			b := source(edge.Source, edge.SourcePort)
			b.Name = name
			set(b)
		}
		if n.Type == "llm" {
			b := literal("Complete the writing request in the supplied text. Use its source facts and return the requested output.")
			b.Name = "instructions"
			set(b)
		}
		plan.Steps = append(plan.Steps, step)
		if n.Position != nil {
			layout.Positions[n.ID] = automation.Position{X: n.Position.X, Y: n.Position.Y}
		}
	}
	checked := automation.Check(automation.InsertConversions(plan, tools), tools)
	if len(checked.Diagnostics) > 0 {
		return plan, layout, fmt.Errorf("invalid native plan: %v", checked.Diagnostics)
	}
	return checked.Plan, layout, nil
}

// fillNamedValue is data interpolation, not evaluation. A complete slot keeps
// the value's type; a slot inside prose gets an explicit readable serialization.
func fillNamedValue(template any, name string, value any) (any, error) {
	token := "$(" + name + ")"
	var replace func(any) (any, error)
	replace = func(input any) (any, error) {
		switch v := input.(type) {
		case string:
			if v == token {
				return value, nil
			}
			text, ok := value.(string)
			if !ok {
				raw, err := json.Marshal(value)
				if err != nil {
					return nil, err
				}
				text = string(raw)
			}
			return strings.ReplaceAll(v, token, text), nil
		case map[string]any:
			out := map[string]any{}
			for key, child := range v {
				next, err := replace(child)
				if err != nil {
					return nil, err
				}
				out[key] = next
			}
			return out, nil
		case []any:
			out := make([]any, len(v))
			for i, child := range v {
				next, err := replace(child)
				if err != nil {
					return nil, err
				}
				out[i] = next
			}
			return out, nil
		default:
			return input, nil
		}
	}
	return replace(template)
}
