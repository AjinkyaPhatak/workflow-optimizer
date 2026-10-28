package automation

import (
	"encoding/json"
	"strings"
)

func literalBudget(name string, value any) Binding {
	data, _ := json.Marshal(value)
	return Binding{Name: name, Source: "literal", ValueJSON: string(data)}
}

// Automatic execution does not turn implementation limits into user questions.
// Preserve a budget step's result if another step actually consumes it.
func normalizeAutomaticBudget(plan Plan, tools []ToolDescriptor) Plan {
	if !plan.Budget.Automatic {
		return plan
	}
	values := map[string]int{"max_actions": 30, "max_tokens": 50000, "max_active_seconds": 600}
	if plan.Budget.MaxActions > 0 {
		values["max_actions"] = plan.Budget.MaxActions
	}
	if plan.Budget.MaxTokens > 0 {
		values["max_tokens"] = plan.Budget.MaxTokens
	}
	if plan.Budget.MaxActiveSeconds > 0 {
		values["max_active_seconds"] = plan.Budget.MaxActiveSeconds
	}
	attached, referenced := map[string]bool{}, map[string]bool{}
	for _, step := range plan.Steps {
		if step.Kind == "ask" && step.Question != nil && step.Question.Kind == "number" && len(step.Children) == 0 && len(step.Otherwise) == 0 && len(step.Inputs) == 0 && step.Condition == nil && step.Tool == "" {
			attached[step.ID+"/"+step.Question.ID] = true
		}
	}
	var walk func([]Step)
	walk = func(steps []Step) {
		for _, step := range steps {
			bindings := append([]Binding{}, step.Inputs...)
			if step.Condition != nil {
				bindings = append(bindings, step.Condition.Left, step.Condition.Right)
			}
			for _, b := range bindings {
				if b.Source == "step" {
					id, _, _ := strings.Cut(b.Ref, ".")
					referenced[id] = true
				}
			}
			walk(step.Children)
			walk(step.Otherwise)
		}
	}
	walk(plan.Steps)
	bindAvailable := false
	for _, tool := range tools {
		bindAvailable = bindAvailable || tool.ID == "value.bind"
	}
	resolved, answerValues := map[string]int{}, map[string]int{}
	questions := []Question{}
	for _, q := range plan.Questions {
		field := ""
		for key := range values {
			if q.Input == "budget."+key || q.ID == key && q.Input == "answer" {
				field = key
				break
			}
		}
		configuration := field != "" && q.StepID == "" && q.Input == "budget."+field
		if field != "" && q.Kind == "number" && q.Purpose != "intent" && (configuration || attached[q.StepID+"/"+q.ID]) && (!referenced[q.StepID] || bindAvailable) {
			if q.StepID != "" {
				resolved[q.StepID] = values[field]
			}
			answerValues[q.ID] = values[field]
			continue
		}
		questions = append(questions, q)
	}
	plan.Questions = questions
	steps := []Step{}
	for _, step := range plan.Steps {
		value, ok := resolved[step.ID]
		if !ok {
			steps = append(steps, step)
			continue
		}
		if referenced[step.ID] {
			step.Kind, step.Tool, step.Question = "tool", "value.bind", nil
			step.Inputs = []Binding{literalBudget("template", value), literalBudget("name", "value"), literalBudget("value", value)}
			steps = append(steps, step)
		}
	}
	plan.Steps = steps
	var rewrite func([]Step)
	bind := func(b Binding) Binding {
		if value, ok := answerValues[b.Ref]; ok && b.Source == "answer" {
			return literalBudget(b.Name, value)
		}
		return b
	}
	rewrite = func(steps []Step) {
		for i := range steps {
			for j, b := range steps[i].Inputs {
				steps[i].Inputs[j] = bind(b)
			}
			if c := steps[i].Condition; c != nil {
				c.Left = bind(c.Left)
				c.Right = bind(c.Right)
			}
			rewrite(steps[i].Children)
			rewrite(steps[i].Otherwise)
		}
	}
	rewrite(plan.Steps)
	return plan
}
