package automation

import "testing"

func TestRefreshResourceOptionsDropsDeletedChoices(t *testing.T) {
	plan := EmptyPlan("A file")
	plan.Steps = []Step{{ID: "read", Kind: "tool", Tool: "read"}}
	plan.Questions = []Question{{ID: "file", StepID: "read", Input: "config.file_id", Kind: "choice", Options: []Option{{Label: "Removed file", Value: "deleted"}}}}
	refreshed := RefreshQuestionOptions(plan, []ToolDescriptor{{ID: "read", Inputs: []Field{{Name: "config.file_id", Type: "string"}}}})
	if len(refreshed.Questions[0].Options) != 0 || len(plan.Questions[0].Options) != 1 {
		t.Fatal("deleted choice remained selectable or the source plan was mutated")
	}
	if ValidateAnswer(refreshed.Questions[0], "deleted") == nil {
		t.Fatal("a deleted resource remained a valid answer")
	}
}

func TestGeneratedQuestionsRetainCustomChoicesAndBounds(t *testing.T) {
	plan := EmptyPlan("Configure")
	plan.Steps = []Step{{ID: "write", Kind: "tool", Tool: "write"}}
	min, max := 1.0, 10.0
	checked := Check(plan, []ToolDescriptor{{ID: "write", Inputs: []Field{
		{Name: "model", Type: "string", Required: true, AllowCustom: true, Options: []Option{{Label: "Default", Value: "default"}}},
		{Name: "count", Type: "number", Required: true, Min: &min, Max: &max},
	}}})
	if len(checked.Plan.Questions) != 2 {
		t.Fatalf("questions: %+v", checked.Plan.Questions)
	}
	if err := ValidateAnswer(checked.Plan.Questions[0], "custom"); err != nil {
		t.Fatalf("custom model rejected: %v", err)
	}
	if err := ValidateAnswer(checked.Plan.Questions[1], 11.0); err == nil {
		t.Fatal("out-of-range answer accepted")
	}
}
