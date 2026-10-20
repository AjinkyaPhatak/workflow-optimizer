package phasee

import (
	"strings"
	"testing"
)

// Phase K: the gallery lists built-in templates by category with what to
// prepare; "Save as template" copies a workflow into the workspace's
// gallery, and a saved template starts new workflows like a built-in one.
func TestTemplateGalleryAndSavedTemplates(t *testing.T) {
	e := newEnv(t)
	ada, grace := e.register("ada"), e.register("grace")
	project := e.must(e.call(ada.token, "POST", "/api/v1/projects", map[string]any{"workspace_id": ada.workspace, "name": "P"}), 201)["id"].(string)

	builtIn := e.must(e.call(ada.token, "GET", "/api/v1/templates", nil), 200)["items"].([]any)
	if len(builtIn) < 25 {
		t.Fatalf("built-in templates: %d", len(builtIn))
	}
	byID := map[string]map[string]any{}
	for _, x := range builtIn {
		m := x.(map[string]any)
		byID[m["id"].(string)] = m
		if m["saved"] != false || m["category"] == "" {
			t.Fatalf("built-in: %v", m)
		}
	}
	if setup := byID["receipts-to-sheet"]["setup"].([]any); len(setup) == 0 || !strings.Contains(setup[0].(string), "Google Sheet") {
		t.Fatalf("setup: %v", setup)
	}

	// A workflow from a gallery template, saved back as a template.
	wf := e.must(e.call(ada.token, "POST", "/api/v1/workflows", map[string]any{"project_id": project, "name": "Posts", "template_id": "social-media-posts"}), 201)
	wfID := wf["id"].(string)
	if r := e.call(ada.token, "POST", "/api/v1/templates", map[string]any{"workflow_id": wfID, "name": " "}); r.status != 400 && r.status != 422 {
		t.Fatalf("no name: %d %s", r.status, r.body)
	}
	saved := e.must(e.call(ada.token, "POST", "/api/v1/templates", map[string]any{"workflow_id": wfID, "name": "Our launch posts", "description": "For product news"}), 201)
	if saved["saved"] != true || saved["category"] != "Saved" || len(saved["definition"].(map[string]any)["nodes"].([]any)) != 3 {
		t.Fatalf("saved: %v", saved)
	}
	savedID := saved["id"].(string)

	// Listed for the workspace, first; not for another workspace.
	list := e.must(e.call(ada.token, "GET", "/api/v1/templates?workspace_id="+ada.workspace, nil), 200)["items"].([]any)
	if first := list[0].(map[string]any); first["id"] != savedID || len(list) != len(builtIn)+1 {
		t.Fatalf("workspace list: %v", first)
	}
	if r := e.call(grace.token, "GET", "/api/v1/templates?workspace_id="+ada.workspace, nil); r.status != 404 && r.status != 403 {
		t.Fatalf("other workspace list: %d", r.status)
	}
	if r := e.call(grace.token, "POST", "/api/v1/templates", map[string]any{"workflow_id": wfID, "name": "Mine"}); r.status != 404 {
		t.Fatalf("other workspace save: %d", r.status)
	}

	// A saved template starts a new workflow with fresh node IDs.
	copied := e.must(e.call(ada.token, "POST", "/api/v1/workflows", map[string]any{"project_id": project, "name": "Posts 2", "template_id": savedID}), 201)
	version := copied["version"].(map[string]any)
	v := e.must(e.call(ada.token, "GET", "/api/v1/workflows/"+copied["id"].(string)+"/versions/"+version["id"].(string), nil), 200)
	nodes := v["definition"].(map[string]any)["nodes"].([]any)
	orig := saved["definition"].(map[string]any)["nodes"].([]any)
	if len(nodes) != len(orig) || nodes[0].(map[string]any)["id"] == orig[0].(map[string]any)["id"] {
		t.Fatalf("instance: %v", nodes)
	}
	// Another workspace cannot use it.
	gp := e.must(e.call(grace.token, "POST", "/api/v1/projects", map[string]any{"workspace_id": grace.workspace, "name": "G"}), 201)["id"].(string)
	if r := e.call(grace.token, "POST", "/api/v1/workflows", map[string]any{"project_id": gp, "name": "X", "template_id": savedID}); r.status == 201 {
		t.Fatalf("another workspace used the template: %s", r.body)
	}

	// Deleting: not by another workspace; afterwards it is gone.
	if r := e.call(grace.token, "DELETE", "/api/v1/templates/"+savedID, nil); r.status != 404 {
		t.Fatalf("other workspace delete: %d", r.status)
	}
	if r := e.call(ada.token, "DELETE", "/api/v1/templates/"+savedID, nil); r.status != 204 {
		t.Fatalf("delete: %d %s", r.status, r.body)
	}
	if r := e.call(ada.token, "DELETE", "/api/v1/templates/"+savedID, nil); r.status != 404 {
		t.Fatalf("deleted twice: %d", r.status)
	}
	if list := e.must(e.call(ada.token, "GET", "/api/v1/templates?workspace_id="+ada.workspace, nil), 200)["items"].([]any); len(list) != len(builtIn) {
		t.Fatalf("after delete: %d", len(list))
	}
}
