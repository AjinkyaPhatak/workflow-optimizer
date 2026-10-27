package automation

import (
	"context"
	"fmt"
	"testing"
)

func literal(name, value string) Binding {
	return Binding{Name: name, Source: "literal", ValueJSON: value}
}
func planOf(steps ...Step) Plan {
	return Plan{SchemaVersion: 2, Name: "A useful workflow", Mode: "once", Steps: steps, Questions: []Question{}}
}

var testTools = []ToolDescriptor{
	{ID: "lookup", Outputs: []Field{{Name: "rows", Type: "array"}, {Name: "amount", Type: "number"}}},
	{ID: "send", Inputs: []Field{{Name: "text", Type: "string", Required: true}}},
	{ID: "value.text", Inputs: []Field{{Name: "value", Type: "json", Required: true}}, Outputs: []Field{{Name: "text", Type: "string"}}},
}

type plannerFunc func(context.Context, PlanRequest) (Plan, error)

func (f plannerFunc) Generate(ctx context.Context, req PlanRequest) (Plan, error) { return f(ctx, req) }

func TestRepairDoesNotAddAnExternalAction(t *testing.T) {
	before := planOf(Step{ID: "value", Kind: "tool", Tool: "value.text", Inputs: []Binding{literal("value", `"Hello"`)}})
	tools := append(append([]ToolDescriptor{}, testTools...), ToolDescriptor{ID: "email", SideEffects: "unsafe"})
	c := Compiler{Tools: tools, Planner: plannerFunc(func(context.Context, PlanRequest) (Plan, error) {
		return planOf(before.Steps[0], Step{ID: "unexpected_email", Kind: "tool", Tool: "email"}), nil
	})}
	out, err := c.Compile(context.Background(), PlanRequest{Repair: true, Previous: &before})
	if err != nil || out.Ready || len(out.Plan.Steps) != 1 {
		t.Fatalf("external action was added: %+v, %v", out, err)
	}
	if len(out.Diagnostics) == 0 || out.Diagnostics[0].Code != "ACTION_CHANGE" {
		t.Fatal("external action change did not block repair")
	}
}

func TestMissingValuesAndAnswers(t *testing.T) {
	compiled := Check(planOf(Step{ID: "send", Name: "Send the message", Kind: "tool", Tool: "send"}), testTools)
	if compiled.Ready || len(compiled.Plan.Questions) != 1 {
		t.Fatalf("missing value did not become a question: %+v", compiled)
	}
	q := compiled.Plan.Questions[0]
	if CheckAnswered(compiled.Plan, testTools, map[string]any{q.ID: "Hello Ajinkya"}).Ready != true {
		t.Fatal("a valid answer did not complete the plan")
	}
	if CheckAnswered(compiled.Plan, testTools, map[string]any{q.ID: "[name]"}).Ready {
		t.Fatal("a placeholder was treated as a real answer")
	}
}
func TestPlaceholdersAndReferences(t *testing.T) {
	compiled := Check(planOf(Step{ID: "send", Kind: "tool", Tool: "send", Inputs: []Binding{literal("text", `"Hello [name]"`)}}), testTools)
	if compiled.Ready || len(compiled.Plan.Questions) != 1 {
		t.Fatal("placeholder was not stopped")
	}
	forward := Check(planOf(Step{ID: "send", Kind: "tool", Tool: "send", Inputs: []Binding{{Name: "text", Source: "step", Ref: "later.text"}}}), testTools)
	if len(forward.Diagnostics) == 0 {
		t.Fatal("forward reference was accepted")
	}
	parallel := Check(planOf(Step{ID: "fork", Kind: "parallel", Children: []Step{
		{ID: "first", Kind: "tool", Tool: "lookup"},
		{ID: "second", Kind: "tool", Tool: "send", Inputs: []Binding{{Name: "text", Source: "step", Ref: "first.amount"}}},
	}}), testTools)
	if len(parallel.Diagnostics) == 0 {
		t.Fatal("parallel sibling dependency was accepted")
	}
}
func TestExplicitConversionAndArtifactField(t *testing.T) {
	plan := planOf(Step{ID: "lookup", Kind: "tool", Tool: "lookup"}, Step{ID: "send", Kind: "tool", Tool: "send", Inputs: []Binding{{Name: "text", Source: "step", Ref: "lookup.amount"}}})
	if Check(plan, testTools).Ready {
		t.Fatal("incompatible data was silently accepted")
	}
	plan = InsertConversions(plan, testTools)
	if len(plan.Steps) != 3 || plan.Steps[1].Tool != "value.text" || !Check(plan, testTools).Ready {
		t.Fatalf("conversion did not produce a valid graph: %+v", Check(plan, testTools))
	}
	tools := append(append([]ToolDescriptor{}, testTools...), ToolDescriptor{ID: "csv", Outputs: []Field{{Name: "artifact", Type: "object"}}})
	artifact := planOf(Step{ID: "csv", Kind: "tool", Tool: "csv"}, Step{ID: "send", Kind: "tool", Tool: "send", Inputs: []Binding{{Name: "text", Source: "step", Ref: "csv.artifact.id"}}})
	if !Check(artifact, tools).Ready {
		t.Fatal("an artifact's string ID was rejected as an object")
	}
}
func TestLimitsAndLiteralTypes(t *testing.T) {
	for _, plan := range []Plan{
		{SchemaVersion: 2, Name: "Unbounded", Mode: "continuous", Steps: []Step{{ID: "a", Kind: "tool", Tool: "lookup"}}},
		planOf(Step{ID: "loop", Kind: "repeat", MaxIterations: 0}),
		planOf(Step{ID: "send", Kind: "tool", Tool: "send", Inputs: []Binding{literal("text", `123`)}}),
	} {
		if Check(plan, testTools).Ready {
			t.Fatalf("unsafe plan accepted: %+v", plan)
		}
	}
}

type brokenPlanner struct {
	calls   int
	varying bool
}

func (p *brokenPlanner) Generate(context.Context, PlanRequest) (Plan, error) {
	p.calls++
	id := "bad"
	if p.varying {
		id = fmt.Sprintf("bad_%d", p.calls)
	}
	return planOf(Step{ID: id, Kind: "tool", Tool: "invented"}), nil
}
func TestCompilerRepairIsBounded(t *testing.T) {
	p := &brokenPlanner{varying: true}
	c := Compiler{Planner: p, Tools: testTools}
	out, err := c.Compile(context.Background(), PlanRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if p.calls != 3 || out.Ready || len(out.Diagnostics) == 0 {
		t.Fatalf("repair loop: calls=%d result=%+v", p.calls, out)
	}
}

func TestCompilerStopsWhenRepairRepeatsTheSameFailure(t *testing.T) {
	p := &brokenPlanner{}
	c := Compiler{Planner: p, Tools: testTools}
	out, err := c.Compile(context.Background(), PlanRequest{})
	if err != nil || p.calls != 2 || out.Ready {
		t.Fatalf("repeated repair: calls=%d ready=%v err=%v", p.calls, out.Ready, err)
	}
}

func TestComparisonAliasesAreRepairedWithoutChangingTheOriginalPlan(t *testing.T) {
	for _, op := range []string{"gt", ">", "greater_than", " greater "} {
		original := planOf(Step{ID: "email_if_above", Name: "Email only if the average exceeds 150", Kind: "branch", Condition: &Condition{Left: literal("", "200"), Op: op, Right: literal("", "150")}, Children: []Step{{ID: "send", Kind: "tool", Tool: "send", Inputs: []Binding{literal("text", `"The average is 200"`)}}}})
		out := Check(original, testTools)
		if !out.Ready || out.Plan.Steps[0].Condition.Op != "greater" || original.Steps[0].Condition.Op != op {
			t.Fatalf("alias %q: %+v", op, out)
		}
		for _, tc := range []struct {
			average float64
			send    bool
		}{{200, true}, {150, false}, {100, false}} {
			send, err := CompareValues(op, tc.average, float64(150))
			if err != nil || send != tc.send {
				t.Fatalf("%v %s 150 = %v, %v", tc.average, op, send, err)
			}
		}
	}
	unknown := Check(planOf(Step{ID: "decision", Kind: "branch", Condition: &Condition{Left: literal("", "200"), Op: "approximately", Right: literal("", "150")}}), testTools)
	if unknown.Ready || len(unknown.Diagnostics) == 0 || unknown.Diagnostics[0].RepairHint == "" {
		t.Fatal("an unknown rule was guessed or lacked actionable repair feedback")
	}
}
func TestTypedAnswersAndNestedValues(t *testing.T) {
	min := 1.0
	for _, tc := range []struct {
		q     Question
		value any
		valid bool
	}{
		{Question{Kind: "number", Required: true, Min: &min}, 0.0, false},
		{Question{Kind: "number", Required: true, Min: &min}, 12.0, true},
		{Question{Kind: "boolean", Required: true}, false, true},
		{Question{Kind: "choice", Required: true, Options: []Option{{"A", "a"}}}, "b", false},
		{Question{Kind: "choice", Required: true, AllowOther: true, Options: []Option{{"A", "a"}}}, "b", true},
		{Question{Kind: "date", Required: true}, "2026-02-30", false},
	} {
		if (ValidateAnswer(tc.q, tc.value) == nil) != tc.valid {
			t.Fatalf("answer validation: %+v", tc)
		}
	}
	out := map[string]map[string]any{"a": {"rows": []any{map[string]any{"amount": 12.0}}}}
	v, err := Resolve(Binding{Source: "step", Ref: "a.rows.0.amount"}, out, nil, nil, nil)
	if err != nil || v != 12.0 {
		t.Fatalf("nested value: %v %v", v, err)
	}
}
