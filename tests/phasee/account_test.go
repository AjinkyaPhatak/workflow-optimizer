package phasee

import (
	"strings"
	"testing"
)

// Phase M: people change their own name and password; owners and admins
// rename the workspace; every member sees its usage.
func TestAccountSettingsAndUsage(t *testing.T) {
	e := newEnv(t)
	ada, grace := e.register("ada"), e.register("grace")

	// Profile name.
	if r := e.call(ada.token, "PATCH", "/api/v1/auth/me", map[string]any{"name": "  "}); r.status != 400 && r.status != 422 {
		t.Fatalf("empty name: %d %s", r.status, r.body)
	}
	me := e.must(e.call(ada.token, "PATCH", "/api/v1/auth/me", map[string]any{"name": "Ada Lovelace"}), 200)
	if me["name"] != "Ada Lovelace" {
		t.Fatalf("profile: %v", me)
	}
	got := e.must(e.call(ada.token, "GET", "/api/v1/auth/me", nil), 200)
	if got["user"].(map[string]any)["name"] != "Ada Lovelace" {
		t.Fatalf("me: %v", got)
	}

	// Password: the current one is checked; the new one signs in.
	email := got["user"].(map[string]any)["email"].(string)
	if r := e.call(ada.token, "POST", "/api/v1/auth/password", map[string]any{"current_password": "wrong-password", "new_password": "a-new-password"}); r.status != 400 && r.status != 422 {
		t.Fatalf("wrong current: %d %s", r.status, r.body)
	}
	if r := e.call(ada.token, "POST", "/api/v1/auth/password", map[string]any{"current_password": "password-ada", "new_password": "short"}); r.status != 400 && r.status != 422 {
		t.Fatalf("short new: %d %s", r.status, r.body)
	}
	if r := e.call(ada.token, "POST", "/api/v1/auth/password", map[string]any{"current_password": "password-ada", "new_password": "a-new-password"}); r.status != 204 {
		t.Fatalf("change: %d %s", r.status, r.body)
	}
	if r := e.call("", "POST", "/api/v1/auth/login", map[string]any{"email": email, "password": "password-ada"}); r.status != 401 {
		t.Fatalf("old password still works: %d", r.status)
	}
	e.must(e.call("", "POST", "/api/v1/auth/login", map[string]any{"email": email, "password": "a-new-password"}), 200)

	// Workspace name: the owner renames it; another workspace's user cannot.
	ws := e.must(e.call(ada.token, "PATCH", "/api/v1/workspaces/"+ada.workspace, map[string]any{"name": "Ada's Bakery"}), 200)
	if ws["name"] != "Ada's Bakery" || ws["role"] != "owner" {
		t.Fatalf("rename: %v", ws)
	}
	if r := e.call(grace.token, "PATCH", "/api/v1/workspaces/"+ada.workspace, map[string]any{"name": "Mine"}); r.status != 404 {
		t.Fatalf("other workspace rename: %d", r.status)
	}
	if list := e.must(e.call(ada.token, "GET", "/api/v1/auth/me", nil), 200)["workspaces"].([]any); list[0].(map[string]any)["name"] != "Ada's Bakery" {
		t.Fatalf("workspaces: %v", list)
	}

	// Usage: empty at first, then one run and one file.
	u := e.must(e.call(ada.token, "GET", "/api/v1/workspaces/"+ada.workspace+"/usage", nil), 200)
	if u["days"] != 30.0 || u["runs"].(map[string]any)["total"] != 0.0 || len(u["workflows"].([]any)) != 0 {
		t.Fatalf("empty usage: %v", u)
	}
	project := e.must(e.call(ada.token, "POST", "/api/v1/projects", map[string]any{"workspace_id": ada.workspace, "name": "P"}), 201)["id"].(string)
	wf := e.must(e.call(ada.token, "POST", "/api/v1/workflows", map[string]any{"project_id": project, "name": "Orders"}), 201)["id"].(string)
	v := e.must(e.call(ada.token, "POST", "/api/v1/workflows/"+wf+"/versions", map[string]any{"definition": draft()}), 201)["id"].(string)
	e.must(e.call(ada.token, "POST", "/api/v1/workflows/"+wf+"/versions/"+v+"/publish", nil), 200)
	if r := e.call(ada.token, "POST", "/api/v1/workflows/"+wf+"/execute", map[string]any{"input": map[string]any{"n": 3, "name": "Ada"}}); r.status != 202 && r.status != 201 {
		t.Fatalf("execute: %d %s", r.status, r.body)
	}
	e.must(e.upload(ada.token, ada.workspace, "notes.txt", []byte("hello")), 201)

	u = e.must(e.call(ada.token, "GET", "/api/v1/workspaces/"+ada.workspace+"/usage?days=7", nil), 200)
	runs := u["runs"].(map[string]any)
	if u["days"] != 7.0 || runs["total"] != 1.0 || runs["active"].(float64)+runs["completed"].(float64)+runs["failed"].(float64) != 1 {
		t.Fatalf("usage: %v", u)
	}
	top := u["workflows"].([]any)
	if len(top) != 1 || top[0].(map[string]any)["name"] != "Orders" || top[0].(map[string]any)["runs"] != 1.0 {
		t.Fatalf("workflows: %v", top)
	}
	if files := u["files"].(map[string]any); files["count"] != 1.0 || files["bytes"] != 5.0 || u["live_workflows"] != 1.0 {
		t.Fatalf("files: %v", u)
	}
	if daily := u["daily"].([]any); len(daily) != 1 {
		t.Fatalf("daily: %v", daily)
	}

	// Bounds and access.
	if r := e.call(ada.token, "GET", "/api/v1/workspaces/"+ada.workspace+"/usage?days=365", nil); r.status != 400 && r.status != 422 {
		t.Fatalf("days bound: %d", r.status)
	}
	if r := e.call(grace.token, "GET", "/api/v1/workspaces/"+ada.workspace+"/usage", nil); r.status != 404 {
		t.Fatalf("other workspace usage: %d", r.status)
	}
}

// Ten wrong passwords lock the email for a while, even for the right one.
func TestRepeatedWrongPasswordsLockSignIn(t *testing.T) {
	e := newEnv(t)
	e.register("lin")
	me := e.must(e.call(e.register("kim").token, "GET", "/api/v1/auth/me", nil), 200)
	email := me["user"].(map[string]any)["email"].(string)
	for i := 0; i < 10; i++ {
		if r := e.call("", "POST", "/api/v1/auth/login", map[string]any{"email": email, "password": "wrong-password"}); r.status != 401 {
			t.Fatalf("attempt %d: %d", i+1, r.status)
		}
	}
	r := e.call("", "POST", "/api/v1/auth/login", map[string]any{"email": email, "password": "password-kim"})
	if r.status != 429 || !strings.Contains(string(r.body), "15 minutes") {
		t.Fatalf("locked: %d %s", r.status, r.body)
	}
}
