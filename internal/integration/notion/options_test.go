package notion_test

import (
	"context"
	"testing"

	"workflow-optimizer/internal/integration"
	"workflow-optimizer/internal/integration/googleapi/apitest"
	"workflow-optimizer/internal/node"
)

// Phase C8: database and property pickers through the connected account.
func TestOptions(t *testing.T) {
	m := newMock(t)
	h := apitest.New(t, m.options())
	opts := func(typ, source string, cfg map[string]any) string {
		t.Helper()
		n, _ := h.App.NodeRegistry.Resolve(typ)
		cfg["credential_id"] = h.CredID.String()
		out, err := n.(integration.OptionsProvider).Options(context.Background(), node.Scope{WorkspaceID: h.Creds.Workspace}, source, cfg)
		if err != nil {
			t.Fatalf("%s %s: %v", typ, source, err)
		}
		return mustJSON(out)
	}
	if got := opts("notion.query_database", "databases", map[string]any{}); got != `[{"value":"`+dsID+`","label":"Leads"}]` {
		t.Fatalf("databases: %s", got)
	}
	// Filterable properties include everything but the formula; writable
	// ones too (a formula cannot be set).
	want := `[{"value":"Done","label":"Done (checkbox)"},{"value":"Due","label":"Due (date)"},{"value":"Email","label":"Email (email)"},` +
		`{"value":"Name","label":"Name (title)"},{"value":"Notes","label":"Notes (rich_text)"},{"value":"Score","label":"Score (number)"},` +
		`{"value":"Status","label":"Status (status)"},{"value":"Tags","label":"Tags (multi_select)"}]`
	if got := opts("notion.query_database", "properties", map[string]any{"database": dbLink}); got != want {
		t.Fatalf("properties: %s", got)
	}
	if got := opts("notion.create_page", "writable_properties", map[string]any{"database": dbID}); got != want {
		t.Fatalf("writable properties: %s", got)
	}
	if got := opts("notion.update_page", "page_properties", map[string]any{"page": m.order[0]}); got != want {
		t.Fatalf("page properties: %s", got)
	}
	for _, typ := range []string{"notion.query_database", "notion.create_page", "notion.update_page"} {
		d, _ := h.App.NodeRegistry.GetDefinition(typ)
		f, _ := d.GetConfigField("properties")
		if typ != "notion.query_database" && (!f.KeyValue || f.KeysSource == "") {
			t.Fatalf("%s properties field: %+v", typ, f)
		}
	}
}
