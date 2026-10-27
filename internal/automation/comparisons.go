package automation

import "strings"

// ComparisonOptions is the shared contract for planning, validation and the editor.
func ComparisonOptions() []Option {
	return []Option{
		{Label: "is equal to", Value: "equals"},
		{Label: "is not equal to", Value: "not_equals"},
		{Label: "contains", Value: "contains"},
		{Label: "is greater than", Value: "greater"},
		{Label: "is less than", Value: "less"},
		{Label: "is at least", Value: "at_least"},
		{Label: "is at most", Value: "at_most"},
	}
}

func ComparisonOperators() []string {
	options := ComparisonOptions()
	out := make([]string, len(options))
	for i, option := range options {
		out[i] = option.Value
	}
	return out
}

// CanonicalComparison accepts equivalent spellings without guessing an unknown rule.
func CanonicalComparison(op string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(op)) {
	case "equals", "eq", "=", "==", "equal", "equal_to":
		return "equals", true
	case "not_equals", "ne", "neq", "!=", "<>", "not_equal", "not_equal_to":
		return "not_equals", true
	case "contains":
		return "contains", true
	case "greater", "gt", ">", "greater_than":
		return "greater", true
	case "less", "lt", "<", "less_than":
		return "less", true
	case "at_least", "gte", "ge", ">=", "greater_than_or_equal", "greater_than_or_equal_to":
		return "at_least", true
	case "at_most", "lte", "le", "<=", "less_than_or_equal", "less_than_or_equal_to":
		return "at_most", true
	default:
		return op, false
	}
}

// ClonePlan keeps checks and repairs from mutating a saved or previous revision.
func ClonePlan(plan Plan) Plan {
	var clone func([]Step) []Step
	clone = func(steps []Step) []Step {
		if steps == nil {
			return nil
		}
		out := make([]Step, len(steps))
		for i, step := range steps {
			out[i] = step
			out[i].Inputs = append([]Binding(nil), step.Inputs...)
			out[i].Children = clone(step.Children)
			out[i].Otherwise = clone(step.Otherwise)
			if step.Condition != nil {
				condition := *step.Condition
				condition.Op, _ = CanonicalComparison(condition.Op)
				out[i].Condition = &condition
			}
		}
		return out
	}
	plan.Steps = clone(plan.Steps)
	plan.Questions = append([]Question(nil), plan.Questions...)
	return plan
}
