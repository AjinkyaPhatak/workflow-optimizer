package automation

import (
	"encoding/json"
	"testing"
)

func repeatedScaffold() Plan {
	stop := &Condition{Left: Binding{Source: "step", Ref: "price.amount"}, Op: "greater", Right: literal("", "0")}
	inner := Step{ID: "price_check_loop", Name: "Check the price repeatedly", Kind: "repeat", MaxIterations: 10, Condition: stop, Children: []Step{{ID: "notify", Kind: "tool", Tool: "send", Inputs: []Binding{{Name: "text", Source: "step", Ref: "price.amount"}}}}}
	return planOf(Step{ID: inner.ID, Name: inner.Name, Kind: "repeat", Children: []Step{{ID: "price", Kind: "tool", Tool: "lookup"}, inner}})
}

func TestIncompleteRepeatScaffoldIsRepairedWithoutChangingItsRule(t *testing.T) {
	before := repeatedScaffold()
	raw, _ := json.Marshal(before)
	repaired := InsertConversions(before, testTools)
	checked := Check(repaired, testTools)
	if !checked.Ready || len(repaired.Steps) != 1 {
		t.Fatalf("scaffold not repaired: %+v", checked.Diagnostics)
	}
	loop := repaired.Steps[0]
	if loop.ID != "price_check_loop" || loop.MaxIterations != 10 || loop.Condition.Left.Ref != "price.amount" || loop.Condition.Op != "greater" || loop.Children[0].ID != "price" || loop.Children[len(loop.Children)-1].ID != "notify" {
		t.Fatalf("repair changed the refresh order, rule or action identity: %+v", loop)
	}
	again, _ := json.Marshal(InsertConversions(repaired, testTools))
	once, _ := json.Marshal(repaired)
	unchanged, _ := json.Marshal(before)
	if string(once) != string(again) || string(raw) != string(unchanged) {
		t.Fatal("repair must be idempotent and leave the source untouched")
	}
}

func TestRepeatRepairDoesNotGuessOtherDuplicateControlStructures(t *testing.T) {
	for _, kind := range []string{"valid_outer", "different_name", "not_last", "missing_inner_rule", "third_occurrence"} {
		t.Run(kind, func(t *testing.T) {
			plan := repeatedScaffold()
			outer := &plan.Steps[0]
			switch kind {
			case "valid_outer":
				outer.Condition = outer.Children[1].Condition
				outer.MaxIterations = 2
			case "different_name":
				outer.Children[1].Name = "A different repeat"
			case "not_last":
				outer.Children = append(outer.Children, Step{ID: "after", Kind: "wait"})
			case "missing_inner_rule":
				outer.Children[1].Condition = nil
			case "third_occurrence":
				plan.Steps = append(plan.Steps, outer.Children[1])
			}
			checked := Check(InsertConversions(plan, testTools), testTools)
			duplicate := false
			for _, d := range checked.Diagnostics {
				duplicate = duplicate || d.Code == "ID"
			}
			if !duplicate || checked.Ready {
				t.Fatal("an ambiguous control structure was silently accepted")
			}
		})
	}
}

func TestBudgetConfirmationAnswersOnlyAttachedNumericBudgetQuestions(t *testing.T) {
	plan := planOf(Step{ID: "data", Kind: "tool", Tool: "lookup"})
	for _, field := range []string{"max_actions", "max_tokens", "max_active_seconds"} {
		q := Question{ID: field, StepID: "ask_" + field, Input: "answer", Kind: "number", Prompt: "Set the budget", Required: true}
		plan.Steps = append(plan.Steps, Step{ID: q.StepID, Kind: "ask", Question: &q})
		plan.Questions = append(plan.Questions, q)
	}
	intent := Question{ID: "parity", StepID: "parity", Purpose: "intent", Input: "answer", Kind: "choice", Prompt: "Round or truncate?", Required: true}
	plan.Steps = append(plan.Steps, Step{ID: "parity", Kind: "ask", Question: &intent})
	plan.Questions = append(plan.Questions, intent)
	plan.Questions = append(plan.Questions, Question{ID: "spending_cap", Input: "budget.max_actions", Kind: "number", Prompt: "How many actions?", Required: true})
	budget := Budget{MaxActions: 25, MaxTokens: 25000, MaxActiveSeconds: 1800}
	answers := map[string]any{"parity": "round"}
	updated, confirmed := ConfirmBudget(plan, budget, answers)
	if updated.Budget != budget || confirmed["max_actions"] != float64(25) || confirmed["max_tokens"] != float64(25000) || confirmed["max_active_seconds"] != float64(1800) || confirmed["parity"] != "round" || confirmed["spending_cap"] != float64(25) || len(answers) != 1 {
		t.Fatalf("confirmation lost limits or answers: %+v", confirmed)
	}
	if CheckAnswered(updated, testTools, confirmed).Ready {
		t.Fatal("confirming budgets must not resolve an unapplied intent")
	}
	// Keeping ask IDs preserves references; confirmed budgets won't wait again.
	updated.Steps = updated.Steps[:len(updated.Steps)-1]
	updated.Questions = append(updated.Questions[:3:3], updated.Questions[4:]...)
	if !CheckAnswered(updated, testTools, confirmed).Ready {
		t.Fatal("budget questions remained unanswered after explicit confirmation")
	}
	updated, confirmed = ConfirmBudget(updated, Budget{MaxActions: 50, MaxTokens: 50000, MaxActiveSeconds: 3600}, confirmed)
	if confirmed["max_actions"] != float64(50) || updated.Budget.MaxActions != 50 {
		t.Fatal("changing limits left stale budget answers")
	}
}

func TestAutomaticDefaultsNeverAskForExecutionBudgets(t *testing.T) {
	plan := EmptyPlan("Fetch current information")
	q := Question{ID: "max_actions", StepID: "ask_limits", Input: "answer", Kind: "number", Prompt: "How many actions?", Required: true}
	plan.Questions = []Question{q}
	plan.Steps = []Step{{ID: "ask_limits", Kind: "ask", Question: &q}, {ID: "lookup", Kind: "tool", Tool: "lookup"}}
	compiled := CheckAnswered(plan, testTools, nil)
	if !compiled.Ready || len(compiled.Plan.Questions) != 0 || len(compiled.Plan.Steps) != 1 || !compiled.Plan.Budget.Automatic {
		t.Fatalf("automatic defaults still require budget input: %+v", compiled)
	}
	plan.Budget.Automatic = false
	manual := CheckAnswered(plan, testTools, nil)
	if manual.Ready || len(manual.Plan.Questions) != 1 {
		t.Fatal("explicit custom limits were silently removed")
	}
}
