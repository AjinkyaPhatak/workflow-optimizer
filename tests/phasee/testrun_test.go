package phasee

import (
	"strings"
	"testing"

	"workflow-optimizer/internal/config"
)

func node(id, typ string, cfg map[string]any) map[string]any {
	return map[string]any{"id": id, "type": typ, "name": id, "position": map[string]any{"x": 0, "y": 0}, "config": cfg}
}

func edge(id, src, sp, dst, dp string) map[string]any {
	return map[string]any{"id": id, "source": src, "source_port": sp, "target": dst, "target_port": dp}
}

// A draft with a branch: Input -> Condition(n > 10) -> big | small, each a
// prompt, each to its own output.
func draft() map[string]any {
	return map[string]any{
		"version": 1, "settings": map[string]any{},
		"nodes": []any{
			node("in", "input", map[string]any{}),
			node("cond", "condition", map[string]any{"value": "{{input.n}}", "operator": "greater_than", "compare": "10"}),
			node("big", "prompt", map[string]any{"template": "Big order from {{input.name}}"}),
			node("small", "prompt", map[string]any{"template": "Small order from {{input.name}}"}),
			node("out_big", "output", map[string]any{}),
			node("out_small", "output", map[string]any{}),
		},
		"edges": []any{
			edge("e1", "in", "data", "cond", "value"),
			edge("e2", "cond", "true", "big", "variables"),
			edge("e3", "cond", "false", "small", "variables"),
			edge("e4", "big", "prompt", "out_big", "value"),
			edge("e5", "small", "prompt", "out_small", "value"),
		},
	}
}

// Phase E3: the editor's draft runs now, without saving or publishing.
func TestTestRuns(t *testing.T) {
	e := newEnvWith(t, func(c *config.Config) { c.ExposeNodeData = true })
	ada, grace := e.register("ada"), e.register("grace")
	project := e.must(e.call(ada.token, "POST", "/api/v1/projects", map[string]any{"workspace_id": ada.workspace, "name": "P"}), 201)["id"].(string)
	wf := e.must(e.call(ada.token, "POST", "/api/v1/workflows", map[string]any{"project_id": project, "name": "Orders"}), 201)["id"].(string)
	path := "/api/v1/workflows/" + wf + "/test"

	// The whole draft: one branch runs, the other is skipped.
	res := e.must(e.call(ada.token, "POST", path, map[string]any{"definition": draft(), "input": map[string]any{"n": 25, "name": "Ada"}}), 200)
	if res["status"] != "COMPLETED" {
		t.Fatalf("result: %v", res)
	}
	byID := map[string]map[string]any{}
	order := []string{}
	for _, n := range res["nodes"].([]any) {
		m := n.(map[string]any)
		byID[m["node_id"].(string)] = m
		order = append(order, m["node_id"].(string))
	}
	if byID["big"]["status"] != "COMPLETED" || byID["big"]["output"].(map[string]any)["prompt"] != "Big order from Ada" ||
		byID["small"]["status"] != "SKIPPED" || byID["out_small"]["status"] != "SKIPPED" || byID["cond"]["output"].(map[string]any)["result"] != true {
		t.Fatalf("nodes: %v", res["nodes"])
	}
	if out := res["output"].(map[string]any); out["out_big"] == nil || out["out_small"] != nil {
		t.Fatalf("output: %v", res["output"])
	}

	// Up to one node: only what it depends on runs.
	res = e.must(e.call(ada.token, "POST", path, map[string]any{"definition": draft(), "input": map[string]any{"n": 3, "name": "Grace"}, "until_node_id": "small"}), 200)
	ids := []string{}
	for _, n := range res["nodes"].([]any) {
		ids = append(ids, n.(map[string]any)["node_id"].(string))
	}
	if strings.Join(ids, ",") != "in,cond,small" || res["output"] != nil {
		t.Fatalf("until small: %v (%v)", ids, res)
	}

	// A failing node: the run reports where and why.
	bad := draft()
	bad["nodes"].([]any)[2] = node("big", "prompt", map[string]any{"template": "{{input.missing.deep}}"})
	res = e.must(e.call(ada.token, "POST", path, map[string]any{"definition": bad, "input": map[string]any{"n": 25}}), 200)
	if res["status"] != "FAILED" || byIDOf(res)["big"]["status"] != "FAILED" || byIDOf(res)["big"]["error"] == nil {
		t.Fatalf("failure: %v", res)
	}

	// Nothing was saved: the workflow still has no version.
	if versions := e.must(e.call(ada.token, "GET", "/api/v1/workflows/"+wf+"/versions", nil), 200)["items"].([]any); len(versions) != 0 {
		t.Fatalf("a test run saved a version: %v", versions)
	}

	// Errors.
	for name, c := range map[string]struct {
		token  string
		body   map[string]any
		status int
	}{
		"other workspace": {grace.token, map[string]any{"definition": draft()}, 404},
		"no definition":   {ada.token, map[string]any{}, 400},
		"unknown node":    {ada.token, map[string]any{"definition": draft(), "until_node_id": "nope"}, 400},
		"invalid graph":   {ada.token, map[string]any{"definition": map[string]any{"version": 1, "settings": map[string]any{}, "nodes": []any{node("x", "no_such_type", nil)}, "edges": []any{}}}, 422},
	} {
		if r := e.call(c.token, "POST", path, c.body); r.status != c.status {
			t.Fatalf("%s: %d %s", name, r.status, r.body)
		}
	}
}

func byIDOf(res map[string]any) map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, n := range res["nodes"].([]any) {
		m := n.(map[string]any)
		out[m["node_id"].(string)] = m
	}
	return out
}

// With node data hidden (OBSERVABILITY_EXPOSE_NODE_DATA=false), statuses
// still come back but outputs do not.
func TestTestRunsHideNodeData(t *testing.T) {
	e := newEnv(t)
	ada := e.register("ada")
	project := e.must(e.call(ada.token, "POST", "/api/v1/projects", map[string]any{"workspace_id": ada.workspace, "name": "P"}), 201)["id"].(string)
	wf := e.must(e.call(ada.token, "POST", "/api/v1/workflows", map[string]any{"project_id": project, "name": "Orders"}), 201)["id"].(string)
	res := e.must(e.call(ada.token, "POST", "/api/v1/workflows/"+wf+"/test", map[string]any{"definition": draft(), "input": map[string]any{"n": 25, "name": "Ada"}}), 200)
	if byIDOf(res)["big"]["status"] != "COMPLETED" || byIDOf(res)["big"]["output"] != nil || res["output"] != nil {
		t.Fatalf("hidden data leaked: %v", res)
	}
}
