package automation

import "encoding/json"

// Materialize freezes human answers into a reusable, independent plan.
func Materialize(plan Plan, answers map[string]any) (Plan, error) {
	var failure error
	binding := func(b Binding) Binding {
		if b.Source != "answer" && b.Source != "template" {
			return b
		}
		value, err := Resolve(b, nil, answers, nil, nil)
		if err != nil {
			failure = err
			return b
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			failure = err
			return b
		}
		return Binding{Name: b.Name, Source: "literal", ValueJSON: string(encoded)}
	}
	var walk func([]Step) []Step
	walk = func(steps []Step) []Step {
		out := make([]Step, len(steps))
		for i, s := range steps {
			s.Inputs = append([]Binding{}, s.Inputs...)
			for j, b := range s.Inputs {
				s.Inputs[j] = binding(b)
			}
			if s.Condition != nil {
				condition := *s.Condition
				condition.Left = binding(condition.Left)
				condition.Right = binding(condition.Right)
				s.Condition = &condition
			}
			s.Children = walk(s.Children)
			s.Otherwise = walk(s.Otherwise)
			out[i] = s
		}
		return out
	}
	plan.Steps = walk(plan.Steps)
	plan.Questions = nil
	return plan, failure
}
