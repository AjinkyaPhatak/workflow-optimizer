package notion_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"workflow-optimizer/internal/app"
	"workflow-optimizer/internal/integration/googleapi/apitest"
	"workflow-optimizer/internal/integration/notion"
	"workflow-optimizer/internal/node"
	oauthnotion "workflow-optimizer/internal/oauth/notion"
	"workflow-optimizer/internal/workflow"
)

const (
	dbID   = "11111111-2222-3333-4444-555555555555"
	dsID   = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	dbLink = "https://www.notion.so/acme/Leads-11111111222233334444555555555555?v=99999999999999999999999999999999"
)

type request struct {
	Method, Path, Version string
	Body                  map[string]any
	Authorized            bool
}

var schema = map[string]any{
	"Name": "title", "Status": "status", "Tags": "multi_select", "Due": "date", "Done": "checkbox", "Score": "number",
	"Email": "email", "Notes": "rich_text", "Total": "formula",
}

// mockNotion is a tiny Notion API: one database (one data source) of pages.
type mockNotion struct {
	mu       sync.Mutex
	srv      *httptest.Server
	pages    map[string]map[string]any
	order    []string
	requests []request
	fail     int
	n        int
}

func newMock(t *testing.T) *mockNotion {
	m := &mockNotion{pages: map[string]map[string]any{}}
	m.srv = httptest.NewServer(http.HandlerFunc(m.serve))
	t.Cleanup(m.srv.Close)
	m.add(map[string]any{
		"Name":   map[string]any{"title": []any{map[string]any{"text": map[string]any{"content": "Ada"}}}},
		"Status": map[string]any{"status": map[string]any{"name": "New"}}, "Tags": map[string]any{"multi_select": []any{map[string]any{"name": "vip"}}},
		"Due": map[string]any{"date": map[string]any{"start": "2026-10-20"}}, "Done": map[string]any{"checkbox": false},
		"Score": map[string]any{"number": 7}, "Email": map[string]any{"email": "ada@example.test"},
		"Notes": map[string]any{"rich_text": []any{}}, "Total": map[string]any{"formula": map[string]any{"type": "number", "number": 14}},
	})
	return m
}

func (m *mockNotion) options() app.Dependencies {
	return app.Dependencies{Notion: notion.Options{BaseURL: m.srv.URL + "/v1", HTTPClient: m.srv.Client()}}
}

// add stores a page from request-style properties, answering them in
// response style (with type and plain_text).
func (m *mockNotion) add(props map[string]any) map[string]any {
	m.n++
	id := fmt.Sprintf("00000000-0000-0000-0000-%012d", m.n)
	page := map[string]any{"object": "page", "id": id, "url": "https://www.notion.so/" + strings.ReplaceAll(id, "-", ""),
		"created_time": "2026-10-14T10:00:00.000Z", "last_edited_time": "2026-10-14T10:00:00.000Z", "properties": map[string]any{}}
	m.pages[id] = page
	m.order = append(m.order, id)
	m.merge(page, props)
	return page
}

func (m *mockNotion) merge(page map[string]any, props map[string]any) {
	out := page["properties"].(map[string]any)
	for name, typ := range schema {
		if _, ok := out[name]; !ok {
			out[name] = map[string]any{"type": typ, typ.(string): nil}
		}
	}
	for name, v := range props {
		typ := schema[name].(string)
		val := v.(map[string]any)[typ]
		if typ == "title" || typ == "rich_text" {
			items := []any{}
			for _, i := range val.([]any) {
				items = append(items, map[string]any{"plain_text": i.(map[string]any)["text"].(map[string]any)["content"]})
			}
			val = items
		}
		out[name] = map[string]any{"type": typ, typ: val}
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func notFound(w http.ResponseWriter) {
	writeJSON(w, 404, map[string]any{"object": "error", "status": 404, "code": "object_not_found", "message": "Could not find " + apitest.Token})
}

func (m *mockNotion) serve(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	req := request{Method: r.Method, Path: r.URL.Path, Version: r.Header.Get("Notion-Version"), Authorized: apitest.Authorized(r)}
	_ = json.Unmarshal(body, &req.Body)
	m.requests = append(m.requests, req)
	if !req.Authorized || req.Version != oauthnotion.APIVersion {
		writeJSON(w, 401, map[string]any{"object": "error", "status": 401, "code": "unauthorized"})
		return
	}
	if m.fail != 0 {
		writeJSON(w, m.fail, map[string]any{"object": "error", "status": m.fail, "code": "validation_error", "message": "bad " + apitest.Token})
		m.fail = 0
		return
	}
	p := strings.TrimPrefix(r.URL.Path, "/v1")
	switch {
	case r.Method == http.MethodGet && p == "/databases/"+dbID:
		writeJSON(w, 200, map[string]any{"object": "database", "id": dbID, "data_sources": []any{map[string]any{"id": dsID, "name": "Leads"}}})
	case r.Method == http.MethodGet && strings.HasPrefix(p, "/databases/"):
		notFound(w)
	case r.Method == http.MethodGet && p == "/data_sources/"+dsID:
		props := map[string]any{}
		for name, typ := range schema {
			props[name] = map[string]any{"id": name, "name": name, "type": typ}
		}
		writeJSON(w, 200, map[string]any{"object": "data_source", "id": dsID, "properties": props})
	case r.Method == http.MethodPost && p == "/data_sources/"+dsID+"/query":
		results := []any{}
		for _, id := range m.order {
			results = append(results, m.pages[id])
		}
		writeJSON(w, 200, map[string]any{"object": "list", "results": results, "has_more": false})
	case r.Method == http.MethodPost && p == "/search":
		writeJSON(w, 200, map[string]any{"object": "list", "results": []any{
			map[string]any{"object": "data_source", "id": dsID, "title": []any{map[string]any{"plain_text": "Leads"}}, "url": "", "last_edited_time": "2026-10-13T00:00:00.000Z"},
			m.pages[m.order[0]],
		}})
	case r.Method == http.MethodPost && p == "/pages":
		parent := req.Body["parent"].(map[string]any)
		if parent["type"] != "data_source_id" || parent["data_source_id"] != dsID {
			notFound(w)
			return
		}
		writeJSON(w, 200, m.add(req.Body["properties"].(map[string]any)))
	case strings.HasPrefix(p, "/pages/"):
		page, ok := m.pages[strings.TrimPrefix(p, "/pages/")]
		if !ok {
			notFound(w)
			return
		}
		if r.Method == http.MethodPatch {
			m.merge(page, req.Body["properties"].(map[string]any))
		}
		writeJSON(w, 200, page)
	default:
		notFound(w)
	}
}

func (m *mockNotion) last() request {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.requests[len(m.requests)-1]
}

func (m *mockNotion) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.requests)
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestObjectID(t *testing.T) {
	for in, want := range map[string]string{
		dbLink:                                                   dbID,
		"111111112222333344445555555555555":                      "", // 33 digits: not an ID
		"11111111222233334444555555555555":                       dbID,
		" " + strings.ToUpper(dbID) + " ":                        dbID,
		"https://www.notion.so/11111111222233334444555555555555": dbID,
		// Title letters that are hex digits ("Face-Cafe") must not shift the ID.
		"https://www.notion.so/acme/Face-Cafe-11111111222233334444555555555555?pvs=4": dbID,
		"https://www.notion.so/acme/Leads":                                            "",
	} {
		got, err := notion.ObjectID("database", in)
		if want == "" {
			if err == nil {
				t.Fatalf("%q accepted as %s", in, got)
			}
			continue
		}
		if err != nil || got != want {
			t.Fatalf("%q: %q %v", in, got, err)
		}
	}
	if _, err := notion.ObjectID("database", "Leads"); err == nil || !strings.Contains(err.Error(), "Notion link or ID") {
		t.Fatal(err)
	}
}

func TestMetadata(t *testing.T) {
	h := apitest.New(t, newMock(t).options())
	for typ, side := range map[string]node.SideEffects{
		"notion.search": node.SideEffectsNone, "notion.query_database": node.SideEffectsNone,
		"notion.create_page": node.SideEffectsUnsafe, "notion.update_page": node.SideEffectsIdempotent,
	} {
		d, err := h.App.NodeRegistry.GetDefinition(typ)
		if err != nil {
			t.Fatal(err)
		}
		if d.SideEffects != side || d.Integration == nil || d.Integration.Name != "Notion" || d.Auth == nil || d.Auth.Provider != "notion" ||
			d.Config[0].Name != "credential_id" || len(d.Inputs) != 1 || d.Inputs[0].Type != node.ValueTypeJSON {
			t.Fatalf("%s: %+v", typ, d)
		}
	}
}

func TestSearch(t *testing.T) {
	m := newMock(t)
	h := apitest.New(t, m.options())
	out, err := h.Result("notion.search", map[string]any{"query": "{{input.q}}", "type": "database", "max_results": 5.0}, map[string]any{"q": "Leads"})
	if err != nil {
		t.Fatal(err)
	}
	if out["count"] != 2.0 || out["id"] != dsID || mustJSON(out["results"].([]any)[0]) != `{"id":"`+dsID+`","last_edited_time":"2026-10-13T00:00:00.000Z","title":"Leads","type":"database","url":""}` ||
		out["results"].([]any)[1].(map[string]any)["title"] != "Ada" {
		t.Fatalf("search: %v", out)
	}
	if b := m.last().Body; b["query"] != "Leads" || mustJSON(b["filter"]) != `{"property":"object","value":"data_source"}` || b["page_size"] != 5.0 {
		t.Fatalf("search request: %v", b)
	}
}

func TestQueryDatabase(t *testing.T) {
	m := newMock(t)
	h := apitest.New(t, m.options())
	out, err := h.Result("notion.query_database", map[string]any{"database": dbLink, "filter_property": "status", "filter_value": "{{input.s}}",
		"sort_property": "Due", "sort_direction": "ascending"}, map[string]any{"s": "New"})
	if err != nil {
		t.Fatal(err)
	}
	row := out["first_row"].(map[string]any)
	if out["count"] != 1.0 || out["page_id"] != row["page_id"] || row["Name"] != "Ada" || row["Status"] != "New" || mustJSON(row["Tags"]) != `["vip"]` ||
		row["Due"] != "2026-10-20" || row["Done"] != false || row["Score"] != 7.0 || row["Email"] != "ada@example.test" || row["Notes"] != "" ||
		row["Total"] != 14.0 || !strings.HasPrefix(row["url"].(string), "https://www.notion.so/") {
		t.Fatalf("query: %v", out)
	}
	b := m.last().Body
	if m.last().Path != "/v1/data_sources/"+dsID+"/query" || mustJSON(b["filter"]) != `{"property":"Status","status":{"equals":"New"}}` ||
		mustJSON(b["sorts"]) != `[{"direction":"ascending","property":"Due"}]` || b["page_size"] != 25.0 {
		t.Fatalf("query request: %v", b)
	}
	// A data source ID (from Search) works too; the advanced filter wins.
	_, err = h.Result("notion.query_database", map[string]any{"database": dsID, "filter_property": "Status", "filter_value": "x",
		"filter": map[string]any{"property": "Done", "checkbox": map[string]any{"equals": true}}}, map[string]any{})
	if err != nil || mustJSON(m.last().Body["filter"]) != `{"checkbox":{"equals":true},"property":"Done"}` {
		t.Fatalf("data source id / advanced filter: %v %v", err, m.last().Body)
	}
	// Filters built per property type.
	for prop, want := range map[string]string{
		"Name":  `{"property":"Name","title":{"contains":"Ad"}}`,
		"Tags":  `{"multi_select":{"contains":"Ad"},"property":"Tags"}`,
		"Email": `{"email":{"contains":"Ad"},"property":"Email"}`,
	} {
		if _, err := h.Result("notion.query_database", map[string]any{"database": dbID, "filter_property": prop, "filter_value": "Ad", "filter_condition": "contains"}, map[string]any{}); err != nil ||
			mustJSON(m.last().Body["filter"]) != want {
			t.Fatalf("%s filter: %v %s", prop, err, mustJSON(m.last().Body["filter"]))
		}
	}
	before := m.count()
	_, err = h.Result("notion.query_database", map[string]any{"database": dbID, "filter_property": "Phone", "filter_value": "1"}, map[string]any{})
	if f := apitest.Failure(t, err); f.Code != "INVALID_REQUEST" || !strings.Contains(f.Message, `no property "Phone"`) || !strings.Contains(f.Message, "Email, Name, Notes") {
		t.Fatalf("unknown property: %+v", f)
	}
	if m.count() != before+2 { // database + schema only, no query
		t.Fatalf("requests: %d", m.count()-before)
	}
	_, err = h.Result("notion.query_database", map[string]any{"database": dbID, "filter_property": "Score", "filter_value": "high"}, map[string]any{})
	if f := apitest.Failure(t, err); f.Code != "INVALID_REQUEST" {
		t.Fatalf("number filter: %+v", f)
	}
}

func TestCreatePage(t *testing.T) {
	m := newMock(t)
	h := apitest.New(t, m.options())
	long := strings.Repeat("x", 2500)
	out, err := h.Result("notion.create_page", map[string]any{"database": dbLink, "content": "First paragraph.\n\nSecond " + long,
		"properties": map[string]any{"name": "{{input.name}}", "Status": "To do", "Tags": "a, b", "Due": "2026-11-01", "Done": "yes",
			"Score": "42", "Email": "", "Notes": long}}, map[string]any{"name": "Grace"})
	if err != nil {
		t.Fatal(err)
	}
	req := m.last()
	props := req.Body["properties"].(map[string]any)
	if out["page_id"] == "" || !strings.HasPrefix(out["url"].(string), "https://www.notion.so/") ||
		mustJSON(props["Name"]) != `{"title":[{"text":{"content":"Grace"},"type":"text"}]}` ||
		mustJSON(props["Status"]) != `{"status":{"name":"To do"}}` || mustJSON(props["Tags"]) != `{"multi_select":[{"name":"a"},{"name":"b"}]}` ||
		mustJSON(props["Due"]) != `{"date":{"start":"2026-11-01"}}` || mustJSON(props["Done"]) != `{"checkbox":true}` ||
		mustJSON(props["Score"]) != `{"number":42}` || mustJSON(props["Email"]) != `{"email":null}` ||
		len(props["Notes"].(map[string]any)["rich_text"].([]any)) != 2 {
		t.Fatalf("create: %v\n%s", out, mustJSON(props))
	}
	children := req.Body["children"].([]any)
	if len(children) != 2 || len(children[1].(map[string]any)["paragraph"].(map[string]any)["rich_text"].([]any)) != 2 {
		t.Fatalf("content blocks: %s", mustJSON(children))
	}
	// Properties from an upstream object (setting empty).
	if _, err := h.Result("notion.create_page", map[string]any{"database": dbID}, map[string]any{"Name": "Linus", "page_id": "ignored"}); err != nil {
		t.Fatal(err)
	}
	before := m.count()
	for name, cfg := range map[string]map[string]any{
		"unknown":   {"database": dbID, "properties": map[string]any{"Phone": "1"}},
		"read-only": {"database": dbID, "properties": map[string]any{"Total": "1"}},
		"number":    {"database": dbID, "properties": map[string]any{"Score": "many"}},
		"checkbox":  {"database": dbID, "properties": map[string]any{"Done": "maybe"}},
	} {
		_, err := h.Result("notion.create_page", cfg, map[string]any{})
		if f := apitest.Failure(t, err); f.Code != "INVALID_REQUEST" || f.Retryable {
			t.Fatalf("%s: %+v", name, f)
		}
	}
	if m.count() != before+8 { // database + schema each, never a create
		t.Fatalf("invalid properties reached Notion's create: %d", m.count()-before)
	}
	if _, err := h.Result("notion.create_page", map[string]any{"database": dbID}, map[string]any{}); apitest.Failure(t, err).Code != "CONFIGURATION_ERROR" {
		t.Fatal("properties are required")
	}
}

func TestUpdatePage(t *testing.T) {
	m := newMock(t)
	h := apitest.New(t, m.options())
	id := m.order[0]
	out, err := h.Result("notion.update_page", map[string]any{"page": "https://www.notion.so/acme/Ada-" + strings.ReplaceAll(id, "-", ""),
		"properties": map[string]any{"Status": "Done", "Done": true}}, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	row := out["row"].(map[string]any)
	if out["page_id"] != id || row["Status"] != "Done" || row["Done"] != true || row["Name"] != "Ada" || m.last().Method != http.MethodPatch ||
		mustJSON(m.last().Body["properties"]) != `{"Done":{"checkbox":true},"Status":{"status":{"name":"Done"}}}` {
		t.Fatalf("update: %v %v", out, m.last().Body)
	}
	_, err = h.Result("notion.update_page", map[string]any{"page": "00000000-0000-0000-0000-000000000999", "properties": map[string]any{"Done": true}}, map[string]any{})
	f := apitest.Failure(t, err)
	if f.Code != "RESOURCE_NOT_FOUND" || !strings.Contains(f.Message, "shared with the integration") {
		t.Fatalf("missing page: %+v", f)
	}
	apitest.NoToken(t, "error", mustJSON(f))
	if _, err := h.Result("notion.update_page", map[string]any{"properties": map[string]any{"Done": true}}, map[string]any{}); apitest.Failure(t, err).Code != "CONFIGURATION_ERROR" {
		t.Fatal("a row is required")
	}
}

// Find Database Rows -> Update Database Row: the row's page_id connects.
func TestQueryThenUpdateChain(t *testing.T) {
	m := newMock(t)
	h := apitest.New(t, m.options())
	cred := h.CredID.String()
	pos := &workflow.Position{}
	def := workflow.Definition{Version: workflow.DefinitionSchemaVersion, Settings: map[string]any{},
		Nodes: []workflow.Node{
			{ID: "in", Type: "input", Name: "Input", Position: pos, Config: map[string]any{}},
			{ID: "find", Type: "notion.query_database", Name: "Find", Position: pos, Config: map[string]any{"credential_id": cred, "database": dbID}},
			{ID: "upd", Type: "notion.update_page", Name: "Update", Position: pos, Config: map[string]any{"credential_id": cred,
				"properties": map[string]any{"Status": "Contacted"}}},
			{ID: "out", Type: "output", Name: "Output", Position: pos, Config: map[string]any{}},
		},
		Edges: []workflow.Edge{
			{ID: "e1", Source: "in", SourcePort: "data", Target: "find", TargetPort: "database"},
			{ID: "e2", Source: "find", SourcePort: "page_id", Target: "upd", TargetPort: "page_id"},
			{ID: "e3", Source: "upd", SourcePort: "page_id", Target: "out", TargetPort: "value"},
		}}
	if err := h.Run(def, map[string]any{}, "op-chain"); err != nil {
		t.Fatal(err)
	}
	if got := m.pages[m.order[0]]["properties"].(map[string]any)["Status"]; mustJSON(got) != `{"status":{"name":"Contacted"},"type":"status"}` {
		t.Fatalf("chained update: %s", mustJSON(got))
	}
}

func TestErrors(t *testing.T) {
	m := newMock(t)
	h := apitest.New(t, m.options())
	m.fail = 400
	_, err := h.Result("notion.search", map[string]any{}, map[string]any{})
	f := apitest.Failure(t, err)
	if f.Code != "INVALID_REQUEST" || f.Retryable || !strings.Contains(f.Message, "Notion rejected a value") {
		t.Fatalf("validation: %+v", f)
	}
	apitest.NoToken(t, "error", mustJSON(f))
	m.fail = 429
	if f := apitest.Failure(t, func() error { _, err := h.Result("notion.search", map[string]any{}, map[string]any{}); return err }()); f.Code != "RATE_LIMITED" || !f.Retryable {
		t.Fatalf("rate limit: %+v", f)
	}
	m.fail = 503
	if f := apitest.Failure(t, func() error { _, err := h.Result("notion.search", map[string]any{}, map[string]any{}); return err }()); f.Code != "PROVIDER_UNAVAILABLE" || !f.Retryable {
		t.Fatalf("unavailable: %+v", f)
	}
}
