package phase12_test

import (
	"context"
	"encoding/json"
	"testing"

	"workflow-optimizer/internal/automation"
)

func decodeEditor[T any](t *testing.T, r resp, status int) T {
	t.Helper()
	if r.status != status {
		t.Fatalf("status %d, want %d: %s", r.status, status, r.body)
	}
	var out T
	if err := json.Unmarshal(r.body, &out); err != nil {
		t.Fatal(err)
	}
	return out
}
func TestAutomationEditorVersionsLayoutAndTemplates(t *testing.T) {
	e := newEnv(t)
	a := e.register("Canvas")
	other := e.register("OtherCanvas")
	doc := decodeEditor[automation.EditorDocument](t, e.call(a.token, "POST", "/api/v1/assistant/workflows", map[string]any{"workspace_id": a.workspace, "project_id": e.project(a), "name": "Numbers"}), 201)
	id := doc.Workflow.ID.String()
	base := "/api/v1/assistant/workflows/" + id
	sessionPath := "/api/v1/assistant/sessions/" + id
	if doc.Session.ID != doc.Workflow.ID || doc.Layout.Revision != 0 || doc.Session.Compilation.Ready {
		t.Fatal("new workflow identity/layout/empty readiness")
	}
	plan := automation.EmptyPlan("Numbers")
	plan.Steps = []automation.Step{{ID: "format", Name: "Format the number", Kind: "tool", Tool: "value.text", Inputs: []automation.Binding{{Name: "value", Source: "literal", ValueJSON: "42"}}, Children: []automation.Step{}, Otherwise: []automation.Step{}}}
	checked := decodeEditor[automation.Compilation](t, e.call(a.token, "POST", sessionPath+"/check", map[string]any{"revision": 1, "plan": plan}), 200)
	if !checked.Ready {
		t.Fatalf("check: %+v", checked.Diagnostics)
	}
	unchanged := decodeEditor[automation.Session](t, e.call(a.token, "GET", sessionPath, nil), 200)
	if unchanged.Revision != 1 {
		t.Fatal("check persisted a revision")
	}
	saved := decodeEditor[automation.Session](t, e.call(a.token, "PUT", sessionPath+"/plan", map[string]any{"revision": 1, "plan": plan}), 200)
	layout := decodeEditor[automation.EditorLayout](t, e.call(a.token, "PUT", base+"/layout", map[string]any{"revision": 0, "positions": map[string]any{"format": map[string]float64{"x": 120, "y": 80}}}), 200)
	if layout.Revision != 1 {
		t.Fatal("layout not saved")
	}
	e.must(e.call(a.token, "PUT", base+"/layout", map[string]any{"revision": 0, "positions": map[string]any{}}), 409)
	unchanged = decodeEditor[automation.Session](t, e.call(a.token, "GET", sessionPath, nil), 200)
	if unchanged.Revision != saved.Revision {
		t.Fatal("layout altered executable revision")
	}
	v1 := decodeEditor[automation.WorkflowVersion](t, e.call(a.token, "POST", base+"/versions", map[string]any{"revision": saved.Revision}), 201)
	again := decodeEditor[automation.WorkflowVersion](t, e.call(a.token, "POST", base+"/versions", map[string]any{"revision": saved.Revision}), 201)
	if v1.ID != again.ID || v1.VersionNumber != 1 {
		t.Fatal("publish must be idempotent for a revision")
	}
	if _, err := e.raw.Exec(context.Background(), `UPDATE automation_workflow_versions SET body='{}' WHERE id=$1`, v1.ID); err == nil {
		t.Fatal("published snapshot was mutable")
	}
	plan.Steps[0].Inputs[0].ValueJSON = "99"
	plan.Name = "Changed"
	saved = decodeEditor[automation.Session](t, e.call(a.token, "PUT", sessionPath+"/plan", map[string]any{"revision": saved.Revision, "plan": plan}), 200)
	v2 := decodeEditor[automation.WorkflowVersion](t, e.call(a.token, "POST", base+"/versions", map[string]any{"revision": saved.Revision}), 201)
	if v2.VersionNumber != 2 {
		t.Fatal("version number did not advance")
	}
	saved = decodeEditor[automation.Session](t, e.call(a.token, "POST", base+"/restore", map[string]any{"revision": saved.Revision, "version_id": v1.ID}), 200)
	if saved.Compilation.Plan.Steps[0].Inputs[0].ValueJSON != "42" || saved.Compilation.Plan.Name != "Numbers" {
		t.Fatal("restore did not create the expected draft")
	}
	e.must(e.call(a.token, "POST", base+"/restore", map[string]any{"revision": 1, "version_id": v1.ID}), 409)
	template := decodeEditor[automation.WorkflowTemplate](t, e.call(a.token, "POST", base+"/template", map[string]any{"name": "Number template", "description": "A reusable number"}), 201)
	copy := decodeEditor[automation.EditorDocument](t, e.call(a.token, "POST", "/api/v1/assistant/workflows", map[string]any{"workspace_id": a.workspace, "name": "Number copy", "template_id": template.ID}), 201)
	if copy.Workflow.ID == doc.Workflow.ID || copy.Session.Compilation.Plan.Steps[0].Inputs[0].ValueJSON != "42" || copy.Layout.Positions["format"].X != 120 {
		t.Fatal("template copy lost values/layout or reused identity")
	}
	for _, path := range []string{base, base + "/versions", base + "/layout", sessionPath + "/build"} {
		e.must(e.call(other.token, "GET", path, nil), 404)
	}
	e.must(e.call(other.token, "POST", "/api/v1/assistant/workflows", map[string]any{"workspace_id": other.workspace, "template_id": template.ID}), 404)
	e.must(e.call(other.token, "DELETE", "/api/v1/assistant/workflow-templates/"+template.ID, nil), 404)
	e.must(e.call(a.token, "DELETE", "/api/v1/assistant/workflow-templates/"+template.ID, nil), 204)
	list := e.must(e.call(a.token, "GET", "/api/v1/assistant/workflows?workspace_id="+a.workspace, nil), 200)
	if len(list["items"].([]any)) != 2 {
		t.Fatal("workflow list missing draft")
	}
	runs := e.must(e.call(a.token, "GET", "/api/v1/automation-runs?workspace_id="+a.workspace, nil), 200)
	if len(runs["items"].([]any)) != 0 {
		t.Fatal("editing, layout, publish, restore or templates executed actions")
	}
}

func TestAutomationEditorCatalogAndDefaultSessions(t *testing.T) {
	e := newEnv(t)
	a := e.register("CatalogCanvas")
	session := decodeEditor[automation.Session](t, e.call(a.token, "POST", "/api/v1/assistant/sessions", map[string]any{"workspace_id": a.workspace}), 201)
	doc := decodeEditor[automation.EditorDocument](t, e.call(a.token, "GET", "/api/v1/assistant/workflows/"+session.ID.String(), nil), 200)
	if doc.Workflow.ID != session.ID {
		t.Fatal("existing assistant creation did not expose workflow identity")
	}
	list := e.must(e.call(a.token, "GET", "/api/v1/assistant/workflow-templates?workspace_id="+a.workspace, nil), 200)
	if len(list["items"].([]any)) != 28 {
		t.Fatal("bundled templates were dropped")
	}
	catalog := e.must(e.call(a.token, "GET", "/api/v1/assistant/tools?workspace_id="+a.workspace, nil), 200)
	if len(catalog["controls"].([]any)) != 7 || len(catalog["conversions"].([]any)) == 0 {
		t.Fatal("missing compiler capabilities")
	}
	progress := decodeEditor[automation.BuildProgress](t, e.call(a.token, "GET", "/api/v1/assistant/sessions/"+session.ID.String()+"/build", nil), 200)
	if progress.Status != "idle" {
		t.Fatal("new draft pretended to be building")
	}
}
