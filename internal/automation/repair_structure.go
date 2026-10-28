package automation

// foldIncompleteRepeats repairs a narrow planner scaffold: an empty, invalid
// repeat wrapper ends in a complete repeat with the same ID and name. The
// prefix belongs inside each iteration, before the complete repeat's body.
// Other duplicate IDs remain diagnostics; we never guess their identity.
func foldIncompleteRepeats(plan Plan) Plan {
	counts := map[string]int{}
	var count func([]Step)
	count = func(steps []Step) {
		for _, step := range steps {
			counts[step.ID]++
			count(step.Children)
			count(step.Otherwise)
		}
	}
	count(plan.Steps)
	var walk func([]Step) []Step
	walk = func(steps []Step) []Step {
		for i := range steps {
			s := &steps[i]
			s.Children = walk(s.Children)
			s.Otherwise = walk(s.Otherwise)
			if s.ID == "" || counts[s.ID] != 2 || s.Kind != "repeat" || s.Condition != nil || s.MaxIterations != 0 || len(s.Children) == 0 || len(s.Otherwise) != 0 || len(s.Inputs) != 0 || s.Tool != "" || s.Question != nil || s.WorkflowRef != "" || s.DurationSeconds != 0 {
				continue
			}
			last := len(s.Children) - 1
			inner := s.Children[last]
			if inner.ID != s.ID || inner.Name != s.Name || inner.Kind != "repeat" || inner.Condition == nil || inner.MaxIterations < 1 || inner.MaxIterations > 10000 || len(inner.Otherwise) != 0 || len(inner.Inputs) != 0 || inner.Tool != "" || inner.Question != nil || inner.WorkflowRef != "" || inner.DurationSeconds != 0 {
				continue
			}
			s.Condition, s.MaxIterations = inner.Condition, inner.MaxIterations
			s.Children = append(s.Children[:last:last], inner.Children...)
			counts[s.ID]--
		}
		return steps
	}
	plan.Steps = walk(plan.Steps)
	return plan
}

// ConfirmBudget resolves explicit budget questions only after the user submits
// the budget form. It cannot infer consent from model-supplied default limits.
func ConfirmBudget(plan Plan, budget Budget, answers map[string]any) (Plan, map[string]any) {
	plan = ClonePlan(plan)
	plan.Budget = budget
	confirmed := make(map[string]any, len(answers))
	for id, value := range answers {
		confirmed[id] = value
	}
	values := map[string]int{"max_actions": budget.MaxActions, "max_tokens": budget.MaxTokens, "max_active_seconds": budget.MaxActiveSeconds}
	// Legacy planners used these exact IDs with input="answer". New planners
	// identify the plan field explicitly with input="budget.max_actions", etc.
	attached := map[string]bool{}
	for _, s := range plan.Steps {
		if s.Kind == "ask" && s.Question != nil && s.Question.Kind == "number" && len(s.Inputs) == 0 && len(s.Children) == 0 && len(s.Otherwise) == 0 && s.Condition == nil && s.Tool == "" {
			attached[s.ID+"/"+s.Question.ID] = true
		}
	}
	for _, q := range plan.Questions {
		field := ""
		for key := range values {
			if q.Input == "budget."+key || q.ID == key && q.Input == "answer" {
				field = key
				break
			}
		}
		configurationQuestion := field != "" && q.Input == "budget."+field && q.StepID == ""
		if field != "" && q.Kind == "number" && q.Purpose != "intent" && (attached[q.StepID+"/"+q.ID] || configurationQuestion) && ValidateAnswer(q, float64(values[field])) == nil {
			confirmed[q.ID] = float64(values[field])
		}
	}
	// Keep legacy ask steps and their IDs: other steps may consume their result.
	// Their saved answers make them immediate, without another human wait.
	return plan, confirmed
}
