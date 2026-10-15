package phasee

import (
	"strings"
	"testing"
)

// Phase I: the editor saves automatically into its draft version instead of
// creating a version per save; published (or executed) versions never change.
func TestUpdateDraft(t *testing.T) {
	e := newEnv(t)
	ada, grace := e.register("ada"), e.register("grace")
	project := e.must(e.call(ada.token, "POST", "/api/v1/projects", map[string]any{"workspace_id": ada.workspace, "name": "P"}), 201)["id"].(string)
	wf := e.must(e.call(ada.token, "POST", "/api/v1/workflows", map[string]any{"project_id": project, "name": "Orders"}), 201)["id"].(string)
	base := "/api/v1/workflows/" + wf + "/versions"
	v := e.must(e.call(ada.token, "POST", base, map[string]any{"definition": draft()}), 201)
	path := base + "/" + v["id"].(string)

	changed := draft()
	changed["nodes"].([]any)[2].(map[string]any)["config"] = map[string]any{"template": "Large order from {{input.name}}"}
	updated := e.must(e.call(ada.token, "PUT", path, map[string]any{"definition": changed}), 200)
	if updated["id"] != v["id"] || updated["version_number"] != v["version_number"] || updated["status"] != "DRAFT" {
		t.Fatalf("updated: %v", updated)
	}
	got := e.must(e.call(ada.token, "GET", path, nil), 200)
	if !strings.Contains(string(e.call(ada.token, "GET", path, nil).body), "Large order") || got["version_number"] != 1.0 {
		t.Fatalf("stored: %v", got)
	}
	if list := e.must(e.call(ada.token, "GET", base, nil), 200)["items"].([]any); len(list) != 1 {
		t.Fatalf("versions: %d", len(list))
	}

	// Draft validation applies; another workspace cannot see it.
	broken := draft()
	broken["edges"] = append(broken["edges"].([]any), edge("bad", "in", "data", "nope", "x"))
	if r := e.call(ada.token, "PUT", path, map[string]any{"definition": broken}); r.status != 422 {
		t.Fatalf("invalid draft: %d %s", r.status, r.body)
	}
	if r := e.call(grace.token, "PUT", path, map[string]any{"definition": changed}); r.status != 404 {
		t.Fatalf("other workspace: %d", r.status)
	}

	// Once published it cannot change.
	e.must(e.call(ada.token, "POST", path+"/publish", nil), 200)
	if r := e.call(ada.token, "PUT", path, map[string]any{"definition": draft()}); r.status != 409 || !strings.Contains(string(r.body), "VERSION_NOT_DRAFT") {
		t.Fatalf("published: %d %s", r.status, r.body)
	}
	if r := e.call(ada.token, "PUT", base+"/1b4e28ba-2fa1-41d2-883f-0016d3cca427", map[string]any{"definition": draft()}); r.status != 404 {
		t.Fatalf("unknown version: %d", r.status)
	}
}
