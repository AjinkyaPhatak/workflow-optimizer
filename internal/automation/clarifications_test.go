package automation

import (
	"context"
	"strings"
	"testing"
)

func clarificationTools() []ToolDescriptor {
	return append(append([]ToolDescriptor{}, testTools...), ToolDescriptor{ID: "value.bind", Inputs: []Field{{Name: "template", Type: "json", Required: true}, {Name: "name", Type: "string", Required: true}, {Name: "value", Type: "json", Required: true}}, Outputs: []Field{{Name: "result", Type: "json"}}}, ToolDescriptor{ID: "node.gmail.send", Name: "Send email", SideEffects: "unsafe", Inputs: []Field{{Name: "config.to", Type: "string", Required: true}, {Name: "config.body", Label: "Email body", Type: "string", Required: true}}})
}
func TestInternalReferenceRepairsExistingDraftWithoutHumanAnswers(t *testing.T) {
	plan := planOf(Step{ID: "research", Kind: "tool", Tool: "value.text", Inputs: []Binding{literal("value", `"Nifty gainers"`)}}, Step{ID: "send", Name: "Send market report", Kind: "tool", Tool: "send", Inputs: []Binding{{Name: "text", Source: "template", Ref: "send_text", ValueJSON: `"Research: {{research.text}}"`}}})
	plan.Questions = []Question{{ID: "send_text_0", Prompt: "What should replace {{research.text}}?", Example: "{{research.text}}", Kind: "text", Required: true, StepID: "send", Input: "text"}}
	original := ClonePlan(plan)
	checked := CheckAnswered(plan, clarificationTools(), map[string]any{"send_text_0": "what does this mean?"})
	if !checked.Ready || len(checked.Plan.Questions) != 0 || len(checked.Plan.Steps) != 3 {
		t.Fatalf("not repaired: %+v", checked)
	}
	fill := checked.Plan.Steps[1]
	if fill.Tool != "value.bind" || fill.Inputs[2].Source != "step" || fill.Inputs[2].Ref != "research.text" {
		t.Fatalf("result was not wired: %+v", fill)
	}
	for _, s := range checked.Plan.Steps {
		for _, b := range s.Inputs {
			if b.Source == "answer" || strings.Contains(b.ValueJSON, "what does this mean") {
				t.Fatal("technical answer became data")
			}
		}
	}
	if plan.Steps[1].Inputs[0] != original.Steps[1].Inputs[0] || len(plan.Questions) != 1 {
		t.Fatal("input draft mutated")
	}
	if again := CheckAnswered(checked.Plan, clarificationTools(), nil); !again.Ready || len(again.Plan.Steps) != 3 {
		t.Fatalf("repair was not stable: %+v", again)
	}
}
func TestInternalReferencesAreScopeCheckedAndNeverQuestions(t *testing.T) {
	for _, text := range []string{`"{{missing.text}}"`, `"prefix {{research.missing}}"`, `"{{research.text}}"`, `"{{recipient}}"`} {
		plan := planOf(Step{ID: "send", Kind: "tool", Tool: "send", Inputs: []Binding{literal("text", text)}}, Step{ID: "research", Kind: "tool", Tool: "lookup"})
		out := Check(plan, clarificationTools())
		if out.Ready || len(out.Plan.Questions) != 0 || len(out.Diagnostics) == 0 || out.Diagnostics[0].Code != "REFERENCE" {
			t.Fatalf("%s: %+v", text, out)
		}
	}
	parallel := planOf(Step{ID: "parallel", Kind: "parallel", Children: []Step{{ID: "research", Kind: "tool", Tool: "lookup"}, {ID: "send", Kind: "tool", Tool: "send", Inputs: []Binding{literal("text", `"{{research.amount}}"`)}}}})
	if out := Check(parallel, clarificationTools()); out.Ready || len(out.Plan.Questions) != 0 {
		t.Fatalf("parallel sibling escaped scope: %+v", out)
	}
}
func TestWholeReferenceAndMixedHumanFacts(t *testing.T) {
	out := Check(planOf(Step{ID: "research", Kind: "tool", Tool: "value.text", Inputs: []Binding{literal("value", `"market data"`)}}, Step{ID: "send", Name: "Send market report", Kind: "tool", Tool: "send", Inputs: []Binding{literal("text", `"Hello [name], {{research.text}}"`)}}), clarificationTools())
	if len(out.Plan.Questions) != 1 {
		t.Fatalf("questions: %+v", out)
	}
	q := out.Plan.Questions[0]
	if !strings.Contains(q.Prompt, "name") || !strings.Contains(q.Prompt, "Send market report") || q.StepName != "Send market report" || q.FieldLabel != "text" || q.Context == "" {
		t.Fatalf("context missing: %+v", q)
	}
	if !CheckAnswered(out.Plan, clarificationTools(), map[string]any{q.ID: "Ada"}).Ready {
		t.Fatal("genuine name fact did not complete plan")
	}
	whole := Check(planOf(Step{ID: "research", Kind: "tool", Tool: "value.text", Inputs: []Binding{literal("value", `"market data"`)}}, Step{ID: "send", Kind: "tool", Tool: "send", Inputs: []Binding{literal("text", `"{{ research.text }}"`)}}), clarificationTools())
	if !whole.Ready || whole.Plan.Steps[1].Inputs[0].Source != "step" {
		t.Fatalf("whole result not wired: %+v", whole)
	}
}
func TestEmailContentQuestionIsIntentUntilReplanned(t *testing.T) {
	plan := planOf(Step{ID: "email", Name: "Send Nifty report", Kind: "tool", Tool: "node.gmail.send", Inputs: []Binding{literal("config.to", `"me@example.com"`)}})
	checked := Check(plan, clarificationTools())
	if len(checked.Plan.Questions) != 1 || checked.Plan.Questions[0].Purpose != "intent" {
		t.Fatalf("missing body is not intent: %+v", checked)
	}
	q := checked.Plan.Questions[0]
	if CheckAnswered(checked.Plan, clarificationTools(), map[string]any{q.ID: "include qualifying gainers and their changes"}).Ready {
		t.Fatal("intent answer could be sent verbatim")
	}
	checked.Plan.Questions[0].Purpose = "value"
	if !CheckAnswered(checked.Plan, clarificationTools(), map[string]any{q.ID: "The exact message I requested"}).Ready {
		t.Fatal("explicit exact copy could not be supplied")
	}
}
func TestIntentReplanRejectsVerbatimCopyAndMovingEmailOutsideThreshold(t *testing.T) {
	tools := clarificationTools()
	send := Step{ID: "email", Kind: "tool", Tool: "node.gmail.send", Inputs: []Binding{literal("config.to", `"me@example.com"`), {Name: "config.body", Source: "answer", Ref: "content"}}}
	before := planOf(Step{ID: "threshold", Kind: "branch", Condition: &Condition{Left: literal("", "1"), Op: "greater", Right: literal("", "0.7")}, Children: []Step{send}})
	q := Question{ID: "content", Prompt: "Which details?", Kind: "text", Purpose: "intent", Required: true, StepID: "email", Input: "config.body"}
	before.Questions = []Question{q}
	for _, variant := range []string{"answer", "literal", "wrapped literal", "moved", "threshold", "recipient"} {
		t.Run(variant, func(t *testing.T) {
			next := ClonePlan(before)
			next.Questions = nil
			body := &next.Steps[0].Children[0].Inputs[1]
			*body = literal("config.body", `"A report from the actual results"`)
			switch variant {
			case "answer":
				*body = Binding{Name: "config.body", Source: "answer", Ref: q.ID}
			case "literal":
				*body = literal("config.body", `"summarize the gainers"`)
			case "wrapped literal":
				*body = literal("config.body", `"Hello, SUMMARIZE the gainers. Regards."`)
			case "moved":
				next.Steps = append(next.Steps, next.Steps[0].Children[0])
				next.Steps[0].Children = nil
			case "threshold":
				next.Steps[0].Condition.Right = literal("", "0")
			case "recipient":
				next.Steps[0].Children[0].Inputs[0] = literal("config.to", `"someone@example.com"`)
			}
			c := Compiler{Tools: tools, Planner: plannerFunc(func(context.Context, PlanRequest) (Plan, error) { return next, nil })}
			out, err := c.Compile(context.Background(), PlanRequest{Repair: true, Previous: &before, IntentQuestion: &q, Answers: map[string]any{q.ID: "summarize the gainers"}})
			if err != nil || out.Ready || len(out.Diagnostics) == 0 {
				t.Fatalf("unsafe replan accepted: %+v %v", out, err)
			}
		})
	}
}

func TestRepairRetainsExistingHumanFactIDsAndAnswers(t *testing.T) {
	plan := planOf(Step{ID: "research", Kind: "tool", Tool: "value.text", Inputs: []Binding{literal("value", `"market data"`)}}, Step{ID: "send", Kind: "tool", Tool: "send", Inputs: []Binding{{Name: "text", Source: "template", Ref: "send_text", ValueJSON: `"{{research.text}} for [name]"`}}})
	plan.Questions = []Question{{ID: "send_text_0", Prompt: "What should replace {{research.text}}?", Example: "{{research.text}}", Kind: "text", Required: true, StepID: "send", Input: "text"}, {ID: "send_text_1", Prompt: "Whose report?", Example: "[name]", Kind: "text", Required: true, StepID: "send", Input: "text"}}
	out := CheckAnswered(plan, clarificationTools(), map[string]any{"send_text_0": "bad technical answer", "send_text_1": "Ada"})
	if !out.Ready || len(out.Plan.Questions) != 1 || out.Plan.Questions[0].ID != "send_text_1" {
		t.Fatalf("human answer lost: %+v", out)
	}
	answers := map[string]any{"send_text_0": "what does this mean?", "send_text_1": "Ada"}
	clean := DiscardReferenceAnswers(plan, answers)
	if _, stale := clean["send_text_0"]; stale || clean["send_text_1"] != "Ada" || len(answers) != 2 {
		t.Fatalf("technical guidance retained or genuine facts mutated: %#v", clean)
	}
}

func TestOptionalEmailBodyStillNeedsContentIntent(t *testing.T) {
	tools := []ToolDescriptor{{ID: "node.gmail.send", Name: "Send email", SideEffects: "unsafe", Inputs: []Field{{Name: "config.subject", Type: "string"}, {Name: "config.body", Type: "string"}, {Name: "port.body", Type: "json"}}}, {ID: "lookup", Outputs: []Field{{Name: "text", Type: "string"}}}}
	for _, tc := range []struct {
		name       string
		body       []Binding
		wantIntent bool
	}{
		{"missing", nil, true},
		{"blank_fallback", []Binding{literal("config.body", `"  "`)}, true},
		{"blank_port_and_fallback", []Binding{literal("config.body", `""`), literal("port.body", `""`)}, true},
		{"bound_result_overrides_blank", []Binding{literal("config.body", `""`), {Name: "port.body", Source: "step", Ref: "research.text"}}, false},
		{"quoted_copy", []Binding{literal("config.body", `"Meeting moved to 10 AM."`)}, false},
		{"fallback_with_blank_port", []Binding{literal("config.body", `"The requested exact copy."`), literal("port.body", `""`)}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := planOf(Step{ID: "research", Kind: "tool", Tool: "lookup"}, Step{ID: "email", Name: "Email the daily report", Kind: "tool", Tool: "node.gmail.send", Inputs: tc.body})
			plan.Summary = "Report the qualifying Nifty gainers."
			out := Check(plan, tools)
			if tc.wantIntent {
				if out.Ready || len(out.Plan.Questions) != 1 || out.Plan.Questions[0].Purpose != "intent" || !strings.Contains(out.Plan.Questions[0].Context, "Nifty") {
					t.Fatalf("missing contextual content intent: %+v", out)
				}
				q := out.Plan.Questions[0]
				if CheckAnswered(out.Plan, tools, map[string]any{q.ID: "include qualifying gainers"}).Ready {
					t.Fatal("content intent became runnable without replanning")
				}
			} else if !out.Ready || len(out.Plan.Questions) != 0 {
				t.Fatalf("valid body changed: %+v", out)
			}
		})
	}
}

func TestOptionalEmailBodyRetainsExactCopyQuestion(t *testing.T) {
	tools := []ToolDescriptor{{ID: "node.gmail.send", Inputs: []Field{{Name: "config.body", Type: "string"}, {Name: "port.body", Type: "json"}}}}
	plan := planOf(Step{ID: "email", Kind: "tool", Tool: "node.gmail.send"})
	plan.Questions = []Question{{ID: "exact_copy", Prompt: "What exact message should I send?", Kind: "text", Purpose: "value", Required: true, StepID: "email", Input: "config.body"}}
	out := CheckAnswered(plan, tools, map[string]any{"exact_copy": "See you tomorrow."})
	if !out.Ready || len(out.Plan.Questions) != 1 || out.Plan.Questions[0].ID != "exact_copy" || out.Plan.Questions[0].Purpose != "value" || out.Plan.Steps[0].Inputs[0].Ref != "exact_copy" {
		t.Fatalf("exact copy question was lost: %+v", out)
	}
}
