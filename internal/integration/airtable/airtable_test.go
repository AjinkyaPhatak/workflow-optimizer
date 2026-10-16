package airtable_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"workflow-optimizer/internal/app"
	"workflow-optimizer/internal/integration"
	"workflow-optimizer/internal/integration/airtable"
	"workflow-optimizer/internal/integration/googleapi/apitest"
	"workflow-optimizer/internal/node"
)

const (
	baseID  = "appAbCdEfGhIjKlMn"
	tableID = "tblAbCdEfGhIjKlMn"
)

type request struct {
	Method, Path string
	Query        map[string][]string
	Body         map[string]any
}

// mockAirtable is a tiny Airtable API: one base with a Leads table.
type mockAirtable struct {
	mu       sync.Mutex
	srv      *httptest.Server
	records  []map[string]any // {"id", "fields"}
	requests []request
	status   int
	errType  string
	nextID   int
}

func newMock(t *testing.T) *mockAirtable {
	m := &mockAirtable{records: []map[string]any{
		{"id": "recAAAAAAAAAAAAA1", "fields": map[string]any{"Name": "Ada", "Email": "ada@example.test", "Tags": []any{"vip"}}},
		{"id": "recAAAAAAAAAAAAA2", "fields": map[string]any{"Name": "Grace"}},
		{"id": "recAAAAAAAAAAAAA3", "fields": map[string]any{"Name": "Linus", "Email": "linus@example.test"}},
	}}
	m.srv = httptest.NewServer(http.HandlerFunc(m.serve))
	t.Cleanup(m.srv.Close)
	return m
}

func (m *mockAirtable) options() app.Dependencies {
	return app.Dependencies{Airtable: airtable.Options{BaseURL: m.srv.URL + "/v0", HTTPClient: m.srv.Client()}}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// matches evaluates the only formula shape the client writes.
func matches(formula string, fields map[string]any) bool {
	if formula == "" {
		return true
	}
	field := formula[strings.Index(formula, "{")+1 : strings.Index(formula, "}")]
	want := formula[strings.Index(formula, "='")+2 : len(formula)-1]
	got, _ := fields[field].(string)
	return strings.TrimSpace(got) == strings.ReplaceAll(want, `\'`, `'`)
}

func (m *mockAirtable) serve(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	req := request{Method: r.Method, Path: r.URL.Path, Query: r.URL.Query()}
	_ = json.Unmarshal(body, &req.Body)
	m.requests = append(m.requests, req)
	table := "/v0/" + baseID + "/Leads"
	switch {
	case !apitest.Authorized(r):
		writeJSON(w, 401, map[string]any{"error": map[string]any{"type": "AUTHENTICATION_REQUIRED"}})
	case m.status != 0:
		writeJSON(w, m.status, map[string]any{"error": map[string]any{"type": m.errType, "message": "detail " + apitest.Token}})
	case r.URL.Path == "/v0/meta/bases":
		writeJSON(w, 200, map[string]any{"bases": []any{map[string]any{"id": baseID, "name": "CRM"}, map[string]any{"id": "appZZZZZZZZZZZZZZ", "name": "Admin"}}})
	case r.URL.Path == "/v0/meta/bases/"+baseID+"/tables":
		writeJSON(w, 200, map[string]any{"tables": []any{map[string]any{"id": tableID, "name": "Leads", "fields": []any{
			map[string]any{"id": "fld1", "name": "Name", "type": "singleLineText"}, map[string]any{"id": "fld2", "name": "Email", "type": "email"}}}}})
	case (r.URL.Path == table || r.URL.Path == "/v0/"+baseID+"/"+tableID) && r.Method == http.MethodGet:
		var recs []any
		for _, rec := range m.records {
			if matches(r.URL.Query().Get("filterByFormula"), rec["fields"].(map[string]any)) {
				recs = append(recs, rec)
			}
		}
		// Pages of two, honouring maxRecords.
		start := 0
		if r.URL.Query().Get("offset") == "page2" {
			start = 2
		}
		end := min(start+2, len(recs))
		page := map[string]any{"records": recs[start:end]}
		if end < len(recs) {
			page["offset"] = "page2"
		}
		writeJSON(w, 200, page)
	case r.URL.Path == table && r.Method == http.MethodPost:
		m.nextID++
		rec := map[string]any{"id": "recNEWNEWNEWNEW0" + string(rune('0'+m.nextID)), "fields": req.Body["fields"]}
		m.records = append(m.records, rec)
		writeJSON(w, 200, rec)
	case strings.HasPrefix(r.URL.Path, table+"/") && r.Method == http.MethodPatch:
		id := strings.TrimPrefix(r.URL.Path, table+"/")
		for _, rec := range m.records {
			if rec["id"] == id {
				f := rec["fields"].(map[string]any)
				for k, v := range req.Body["fields"].(map[string]any) {
					f[k] = v
				}
				writeJSON(w, 200, rec)
				return
			}
		}
		writeJSON(w, 404, map[string]any{"error": "NOT_FOUND"})
	default:
		writeJSON(w, 403, map[string]any{"error": map[string]any{"type": "INVALID_PERMISSIONS_OR_MODEL_NOT_FOUND"}})
	}
}

func (m *mockAirtable) last() request {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.requests[len(m.requests)-1]
}

func TestHelpers(t *testing.T) {
	base, table, err := airtable.BaseID("https://airtable.com/" + baseID + "/" + tableID + "/viwAbCdEfGhIjKlMn?blocks=hide")
	if err != nil || base != baseID || table != tableID {
		t.Fatalf("link: %s %s %v", base, table, err)
	}
	if base, table, err := airtable.BaseID(baseID); err != nil || base != baseID || table != "" {
		t.Fatalf("id: %s %s %v", base, table, err)
	}
	if _, _, err := airtable.BaseID("CRM"); err == nil {
		t.Fatal("a name is not a base")
	}
	if _, err := airtable.RecordID("rec123"); err == nil {
		t.Fatal("short record ID")
	}
	f, err := airtable.MatchFormula(" Email ", ` o'neil\x `)
	if err != nil || f != `TRIM({Email}&'')='o\'neil\\x'` {
		t.Fatalf("formula: %s %v", f, err)
	}
	if _, err := airtable.MatchFormula("a}b", "x"); err == nil {
		t.Fatal("braces in a field name")
	}
}

func TestMetadata(t *testing.T) {
	h := apitest.New(t, newMock(t).options())
	want := map[string]node.SideEffects{"airtable.list_records": node.SideEffectsNone,
		"airtable.create_record": node.SideEffectsUnsafe, "airtable.update_record": node.SideEffectsIdempotent}
	for typ, se := range want {
		d, err := h.App.NodeRegistry.GetDefinition(typ)
		if err != nil {
			t.Fatal(err)
		}
		if d.SideEffects != se || d.Integration == nil || d.Integration.Name != "Airtable" || d.Auth == nil || d.Auth.Provider != "airtable" {
			t.Fatalf("%s: %+v", typ, d)
		}
	}
}

func TestListRecords(t *testing.T) {
	m := newMock(t)
	h := apitest.New(t, m.options())
	out, err := h.Result("airtable.list_records", map[string]any{"base": baseID, "table": "Leads"}, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	recs := out["records"].([]any)
	if out["count"] != 3.0 || len(recs) != 3 || out["record_id"] != "recAAAAAAAAAAAAA1" {
		t.Fatalf("list: %v", out)
	}
	// Every record has every field seen (empty ones as "").
	grace := recs[1].(map[string]any)
	if grace["Name"] != "Grace" || grace["Email"] != "" || grace["Tags"] != "" || grace["record_id"] != "recAAAAAAAAAAAAA2" {
		t.Fatalf("grace: %v", grace)
	}
	if first := out["first_record"].(map[string]any); first["Email"] != "ada@example.test" {
		t.Fatalf("first: %v", first)
	}

	// The table can come from the base's link; a field filter and a limit.
	out, err = h.Result("airtable.list_records", map[string]any{"base": "https://airtable.com/" + baseID + "/" + tableID,
		"match_field": "Email", "match_value": " linus@example.test ", "max_records": 1.0}, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if out["count"] != 1.0 || out["record_id"] != "recAAAAAAAAAAAAA3" {
		t.Fatalf("filtered: %v", out)
	}
	if q := m.last().Query; q["maxRecords"][0] != "1" || q["filterByFormula"][0] != "TRIM({Email}&'')='linus@example.test'" {
		t.Fatalf("query: %v", q)
	}

	// No table: a configuration error before any call.
	before := len(m.requests)
	_, err = h.Result("airtable.list_records", map[string]any{"base": baseID}, map[string]any{})
	if f := apitest.Failure(t, err); f.Code != "CONFIGURATION_ERROR" || len(m.requests) != before {
		t.Fatalf("no table: %+v", f)
	}
}

func TestCreateAndUpdate(t *testing.T) {
	m := newMock(t)
	h := apitest.New(t, m.options())
	out, err := h.Result("airtable.create_record", map[string]any{"base": baseID, "table": "Leads",
		"fields": map[string]any{"Name": "Margaret", "Email": "m@example.test"}}, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	id, _ := out["record_id"].(string)
	if !strings.HasPrefix(id, "rec") || out["record"].(map[string]any)["Name"] != "Margaret" {
		t.Fatalf("create: %v", out)
	}
	if b := m.last().Body; b["typecast"] != true {
		t.Fatalf("create body: %v", b)
	}

	// Update by field value.
	out, err = h.Result("airtable.update_record", map[string]any{"base": baseID, "table": "Leads",
		"match_field": "Name", "match_value": "Grace", "fields": map[string]any{"Email": "grace@example.test"}}, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if out["record_id"] != "recAAAAAAAAAAAAA2" || out["record"].(map[string]any)["Email"] != "grace@example.test" ||
		out["record"].(map[string]any)["Name"] != "Grace" {
		t.Fatalf("update: %v", out)
	}
	if req := m.last(); req.Method != http.MethodPatch || req.Path != "/v0/"+baseID+"/Leads/recAAAAAAAAAAAAA2" {
		t.Fatalf("update request: %+v", req)
	}

	// Update by record ID, fields from the input (its record_id dropped).
	if _, err := h.Result("airtable.update_record", map[string]any{"base": baseID, "table": "Leads", "record_id": id},
		map[string]any{"Name": "Maggie", "record_id": "recIGNOREDIGNORED"}); err != nil {
		t.Fatal(err)
	}
	if b, _ := json.Marshal(m.last().Body["fields"]); string(b) != `{"Name":"Maggie"}` {
		t.Fatalf("fields from input: %s", b)
	}

	// Nothing matches: not found.
	_, err = h.Result("airtable.update_record", map[string]any{"base": baseID, "table": "Leads",
		"match_field": "Name", "match_value": "Nobody", "fields": map[string]any{"Name": "x"}}, map[string]any{})
	if f := apitest.Failure(t, err); f.Code != "RESOURCE_NOT_FOUND" || !strings.Contains(f.Message, "Nobody") {
		t.Fatalf("no match: %+v", f)
	}
}

func TestErrors(t *testing.T) {
	m := newMock(t)
	h := apitest.New(t, m.options())
	// A base not shared with the connection.
	_, err := h.Result("airtable.list_records", map[string]any{"base": "appZZZZZZZZZZZZZZ", "table": "Leads"}, map[string]any{})
	if f := apitest.Failure(t, err); f.Code != "RESOURCE_NOT_FOUND" || !strings.Contains(f.Message, "reconnect Airtable") {
		t.Fatalf("not shared: %+v", f)
	}
	m.status, m.errType = 422, "UNKNOWN_FIELD_NAME"
	_, err = h.Result("airtable.create_record", map[string]any{"base": baseID, "table": "Leads", "fields": map[string]any{"Phone": "1"}}, map[string]any{})
	f := apitest.Failure(t, err)
	if f.Code != "INVALID_REQUEST" || !strings.Contains(f.Message, "field names") {
		t.Fatalf("unknown field: %+v", f)
	}
	apitest.NoToken(t, "error", f.Message)
	m.status = 429
	_, err = h.Result("airtable.list_records", map[string]any{"base": baseID, "table": "Leads"}, map[string]any{})
	if f := apitest.Failure(t, err); f.Code != "RATE_LIMITED" {
		t.Fatalf("rate limited: %+v", f)
	}
}

func TestOptions(t *testing.T) {
	m := newMock(t)
	h := apitest.New(t, m.options())
	n, ok := h.App.NodeRegistry.Resolve("airtable.update_record")
	if !ok {
		t.Fatal("not registered")
	}
	opts := n.(integration.OptionsProvider)
	ctx := context.Background()
	call := func(source string, cfg map[string]any) []node.ConfigOption {
		t.Helper()
		cfg["credential_id"] = h.CredID.String()
		out, err := opts.Options(ctx, node.Scope{WorkspaceID: h.Creds.Workspace}, source, cfg)
		if err != nil {
			t.Fatalf("%s: %v", source, err)
		}
		return out
	}
	if b := call("bases", map[string]any{}); len(b) != 2 || b[0].Label != "Admin" || b[1].Value != baseID {
		t.Fatalf("bases: %+v", b)
	}
	if tb := call("tables", map[string]any{"base": baseID}); len(tb) != 1 || tb[0].Value != "Leads" {
		t.Fatalf("tables: %+v", tb)
	}
	if f := call("fields", map[string]any{"base": baseID, "table": "leads"}); len(f) != 2 || f[1].Value != "Email" {
		t.Fatalf("fields: %+v", f)
	}
}
