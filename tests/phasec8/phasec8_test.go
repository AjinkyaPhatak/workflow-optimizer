// Package phasec8 tests the node options API (Phase C8) through the HTTP
// API against PostgreSQL and Redis. The provider-specific choice lists are
// tested in each integration's package against mock APIs.
package phasec8

import (
	"testing"
)

func TestNodeOptionsAPI(t *testing.T) {
	e := newEnv(t)
	u := e.register("ada")
	other := e.register("grace")
	path := "/api/v1/nodes/discord.send_message/options"

	// The catalog advertises the pickers.
	for _, n := range e.must(e.call(u.token, "GET", "/api/v1/nodes", nil), 200)["items"].([]any) {
		d := n.(map[string]any)
		if d["type"] != "discord.send_message" {
			continue
		}
		for _, f := range d["config"].([]any) {
			if f := f.(map[string]any); f["name"] == "channel" && f["options_source"] != "channels" {
				t.Fatalf("channel field: %v", f)
			}
		}
	}

	cases := []struct {
		name   string
		token  string
		path   string
		body   map[string]any
		status int
	}{
		{"no session", "", path, map[string]any{"workspace_id": u.workspace, "source": "channels"}, 401},
		{"not a member", other.token, path, map[string]any{"workspace_id": u.workspace, "source": "channels"}, 404},
		{"bad workspace", u.token, path, map[string]any{"workspace_id": "x", "source": "channels"}, 400},
		{"no source", u.token, path, map[string]any{"workspace_id": u.workspace}, 400},
		{"unknown node", u.token, "/api/v1/nodes/trello.create_card/options", map[string]any{"workspace_id": u.workspace, "source": "boards"}, 404},
		{"undeclared source", u.token, "/api/v1/nodes/gmail.search/options", map[string]any{"workspace_id": u.workspace, "source": "labels"}, 400},
		{"no account", u.token, path, map[string]any{"workspace_id": u.workspace, "source": "channels", "config": map[string]any{}}, 400},
		{"unknown account", u.token, path, map[string]any{"workspace_id": u.workspace, "source": "channels",
			"config": map[string]any{"credential_id": "00000000-0000-0000-0000-000000000001"}}, 400},
	}
	for _, c := range cases {
		if r := e.call(c.token, "POST", c.path, c.body); r.status != c.status {
			t.Fatalf("%s: %d %s", c.name, r.status, r.body)
		}
	}
	r := e.call(u.token, "POST", path, map[string]any{"workspace_id": u.workspace, "source": "channels",
		"config": map[string]any{"credential_id": "00000000-0000-0000-0000-000000000001"}})
	if msg := e.must(r, 400)["error"].(map[string]any)["message"]; msg != "the selected account was not found in this workspace" {
		t.Fatalf("message: %v", msg)
	}
}
