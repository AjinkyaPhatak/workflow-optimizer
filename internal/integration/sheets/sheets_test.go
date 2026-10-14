package sheets_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"

	"workflow-optimizer/internal/app"
	"workflow-optimizer/internal/integration/googleapi/apitest"
	"workflow-optimizer/internal/integration/sheets"
	"workflow-optimizer/internal/node"
	"workflow-optimizer/internal/workflow"
)

const sheetID = "1AbCdEfGhIjKlMnOpQrStUvWxYz0123456789"

type request struct {
	Method, Path, Range string
	Query               map[string][]string
	Body                map[string]any
	Authorized          bool
}

// mockSheets is a tiny Google Sheets API: one spreadsheet of named tabs.
type mockSheets struct {
	mu       sync.Mutex
	srv      *httptest.Server
	tabs     []string
	grid     map[string][][]any
	requests []request
	fail     int    // answer the next request with this status
	failBody string // and this body
}

var rangeRE = regexp.MustCompile(`^'((?:[^']|'')*)'(?:!(.*))?$`)
var rowRE = regexp.MustCompile(`^A(\d+):[A-Z]+(\d+)$`)

func newMock(t *testing.T) *mockSheets {
	m := &mockSheets{tabs: []string{"Leads", "Other"}, grid: map[string][][]any{
		"Leads": {
			{"Name", "Email", "Status"},
			{"Ada", "ada@example.test", "new"},
			{},
			{"Grace", "grace@example.test"},
		},
		"Other": {},
	}}
	m.srv = httptest.NewServer(http.HandlerFunc(m.serve))
	t.Cleanup(m.srv.Close)
	return m
}

func (m *mockSheets) options() app.Dependencies {
	return app.Dependencies{Sheets: sheets.Options{BaseURL: m.srv.URL + "/v4", HTTPClient: m.srv.Client()}}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (m *mockSheets) serve(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	req := request{Method: r.Method, Path: r.URL.Path, Query: r.URL.Query(), Authorized: apitest.Authorized(r)}
	_ = json.Unmarshal(body, &req.Body)
	prefix := "/v4/spreadsheets/" + sheetID
	if rest, ok := strings.CutPrefix(r.URL.Path, prefix+"/values/"); ok {
		req.Range = rest
	}
	m.requests = append(m.requests, req)
	if !req.Authorized {
		writeJSON(w, 401, map[string]any{"error": map[string]any{"code": 401, "status": "UNAUTHENTICATED"}})
		return
	}
	if m.fail != 0 {
		w.WriteHeader(m.fail)
		_, _ = io.WriteString(w, m.failBody)
		m.fail = 0
		return
	}
	if !strings.HasPrefix(r.URL.Path, prefix) {
		writeJSON(w, 404, map[string]any{"error": map[string]any{"code": 404, "status": "NOT_FOUND"}})
		return
	}
	if r.URL.Path == prefix && r.Method == http.MethodGet {
		sheetsList := []any{}
		for _, t := range m.tabs {
			sheetsList = append(sheetsList, map[string]any{"properties": map[string]any{"title": t}})
		}
		writeJSON(w, 200, map[string]any{"sheets": sheetsList})
		return
	}
	rng, op, _ := strings.Cut(req.Range, ":append")
	_ = op
	parts := rangeRE.FindStringSubmatch(rng)
	if parts == nil {
		writeJSON(w, 400, map[string]any{"error": map[string]any{"code": 400, "status": "INVALID_ARGUMENT"}})
		return
	}
	tab, a1 := strings.ReplaceAll(parts[1], "''", "'"), parts[2]
	rows, ok := m.grid[tab]
	if !ok {
		writeJSON(w, 400, map[string]any{"error": map[string]any{"code": 400, "status": "INVALID_ARGUMENT"}})
		return
	}
	switch {
	case r.Method == http.MethodGet && a1 == "1:1":
		vals := [][]any{}
		if len(rows) > 0 {
			vals = append(vals, rows[0])
		}
		writeJSON(w, 200, map[string]any{"range": tab + "!A1:Z1", "values": vals})
	case r.Method == http.MethodGet:
		writeJSON(w, 200, map[string]any{"range": fmt.Sprintf("%s!A1:Z%d", tab, len(rows)), "values": rows})
	case r.Method == http.MethodPost && strings.HasSuffix(req.Range, ":append"):
		row := req.Body["values"].([]any)[0].([]any)
		m.grid[tab] = append(rows, row)
		n := len(m.grid[tab])
		writeJSON(w, 200, map[string]any{"updates": map[string]any{"updatedRange": fmt.Sprintf("%s!A%d:%s%d", tab, n, sheets.ColumnLetter(len(row)), n)}})
	case r.Method == http.MethodPut:
		mm := rowRE.FindStringSubmatch(a1)
		n, _ := strconv.Atoi(mm[1])
		for len(m.grid[tab]) < n {
			m.grid[tab] = append(m.grid[tab], []any{})
		}
		m.grid[tab][n-1] = req.Body["values"].([]any)[0].([]any)
		writeJSON(w, 200, map[string]any{"updatedRange": tab + "!" + a1})
	default:
		writeJSON(w, 400, map[string]any{"error": map[string]any{"code": 400}})
	}
}

func (m *mockSheets) reqs() []request {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]request(nil), m.requests...)
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestHelpers(t *testing.T) {
	for in, want := range map[string]string{
		"https://docs.google.com/spreadsheets/d/" + sheetID + "/edit#gid=0": sheetID,
		"  " + sheetID + " ": sheetID,
	} {
		if got, err := sheets.SpreadsheetID(in); err != nil || got != want {
			t.Fatalf("%q: %q %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "https://example.test/x", "../etc", "a b"} {
		if _, err := sheets.SpreadsheetID(bad); err == nil {
			t.Fatalf("%q accepted", bad)
		}
	}
	for n, want := range map[int]string{1: "A", 26: "Z", 27: "AA", 52: "AZ", 703: "AAA"} {
		if got := sheets.ColumnLetter(n); got != want {
			t.Fatalf("%d: %s", n, got)
		}
	}
	if got := sheets.SheetRange("Bob's list", "A1"); got != "'Bob''s list'!A1" {
		t.Fatal(got)
	}
	if sheets.StartRow("Leads!A7:C7") != 7 || sheets.StartRow("'My tab'!B12:D") != 12 || sheets.StartRow("Leads") != 1 {
		t.Fatal("start row")
	}
}

func TestMetadata(t *testing.T) {
	m := newMock(t)
	h := apitest.New(t, m.options())
	for typ, side := range map[string]node.SideEffects{
		"google_sheets.read_rows": node.SideEffectsNone, "google_sheets.append_row": node.SideEffectsUnsafe, "google_sheets.update_row": node.SideEffectsIdempotent,
	} {
		d, err := h.App.NodeRegistry.GetDefinition(typ)
		if err != nil {
			t.Fatal(err)
		}
		if d.SideEffects != side || d.Integration == nil || d.Integration.Category != "Google" || d.Integration.Name != "Google Sheets" ||
			d.Auth == nil || d.Auth.Provider != "google" || d.Config[0].Name != "credential_id" {
			t.Fatalf("%s: %+v", typ, d)
		}
	}
}

func TestReadRows(t *testing.T) {
	m := newMock(t)
	h := apitest.New(t, m.options())
	out, err := h.Result("google_sheets.read_rows", map[string]any{"spreadsheet": "https://docs.google.com/spreadsheets/d/" + sheetID + "/edit"}, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	// The empty row 3 is skipped; Grace's missing Status is "".
	wantRows := `[{"Email":"ada@example.test","Name":"Ada","Status":"new","row_number":2},{"Email":"grace@example.test","Name":"Grace","Status":"","row_number":4}]`
	if mustJSON(out["rows"]) != wantRows || out["count"] != 2.0 || mustJSON(out["headers"]) != `["Name","Email","Status"]` ||
		mustJSON(out["first_row"]) != `{"Email":"ada@example.test","Name":"Ada","Status":"new","row_number":2}` {
		t.Fatalf("read: %v", out)
	}
	reqs := m.reqs()
	// No sheet set: the first tab's title is looked up first.
	if len(reqs) != 2 || reqs[0].Query["fields"][0] != "sheets.properties.title" || reqs[1].Range != "'Leads'" ||
		reqs[1].Query["valueRenderOption"][0] != "FORMATTED_VALUE" || !reqs[1].Authorized {
		t.Fatalf("requests: %+v", reqs)
	}
	out, err = h.Result("google_sheets.read_rows", map[string]any{"spreadsheet": sheetID, "sheet": "Leads", "max_rows": 1.0}, map[string]any{})
	if err != nil || out["count"] != 1.0 || len(m.reqs()) != 3 {
		t.Fatalf("max rows / named sheet: %v %v", out, err)
	}
	out, err = h.Result("google_sheets.read_rows", map[string]any{"spreadsheet": sheetID, "sheet": "Other"}, map[string]any{})
	if err != nil || out["count"] != 0.0 || mustJSON(out["first_row"]) != `{}` || mustJSON(out["headers"]) != `[]` {
		t.Fatalf("empty sheet: %v %v", out, err)
	}
	if res := workflow.NewValidator(h.App.NodeRegistry).Validate(h.Definition("google_sheets.read_rows", map[string]any{"spreadsheet": sheetID, "max_rows": 1001.0})); res.Valid {
		t.Fatal("max_rows is bounded by the validator")
	}
}

func TestAppendRow(t *testing.T) {
	m := newMock(t)
	h := apitest.New(t, m.options())
	out, err := h.Result("google_sheets.append_row", map[string]any{"spreadsheet": sheetID, "sheet": "Leads",
		"values": map[string]any{"name": "{{input.name}}", "Status": "new", "Email": "{{input.email}}"}},
		map[string]any{"name": "Linus", "email": "linus@example.test"})
	if err != nil {
		t.Fatal(err)
	}
	if out["row_number"] != 5.0 || out["updated_range"] != "Leads!A5:C5" {
		t.Fatalf("append: %v", out)
	}
	last := m.reqs()[len(m.reqs())-1]
	if last.Method != http.MethodPost || last.Query["valueInputOption"][0] != "USER_ENTERED" || last.Query["insertDataOption"][0] != "INSERT_ROWS" ||
		mustJSON(last.Body["values"]) != `[["Linus","linus@example.test","new"]]` {
		t.Fatalf("append request: %+v", last)
	}
	// A values object connected from upstream, stored as exact text.
	_, err = h.Result("google_sheets.append_row", map[string]any{"spreadsheet": sheetID, "sheet": "Leads", "input_mode": "RAW"},
		map[string]any{"Name": "=HYPERLINK(\"x\")", "row_number": 9.0})
	if err != nil {
		t.Fatal(err)
	}
	last = m.reqs()[len(m.reqs())-1]
	if last.Query["valueInputOption"][0] != "RAW" || mustJSON(last.Body["values"]) != `[["=HYPERLINK(\"x\")","",""]]` {
		t.Fatalf("raw append: %+v", last)
	}
	// Unknown columns and missing values fail before anything is written.
	before := len(m.reqs())
	_, err = h.Result("google_sheets.append_row", map[string]any{"spreadsheet": sheetID, "sheet": "Leads", "values": map[string]any{"Phone": "1"}}, map[string]any{"x": 1.0})
	if f := apitest.Failure(t, err); f.Code != "INVALID_REQUEST" || !strings.Contains(f.Message, `no column "Phone"`) || !strings.Contains(f.Message, "Name, Email, Status") {
		t.Fatalf("unknown column: %+v", f)
	}
	if reqs := m.reqs(); len(reqs) != before+1 || reqs[len(reqs)-1].Method != http.MethodGet {
		t.Fatal("only the header row may be read")
	}
	_, err = h.Result("google_sheets.append_row", map[string]any{"spreadsheet": sheetID, "sheet": "Other", "values": map[string]any{"A": "1"}}, map[string]any{"x": 1.0})
	if f := apitest.Failure(t, err); f.Code != "INVALID_REQUEST" || !strings.Contains(f.Message, "row 1") {
		t.Fatalf("no header row: %+v", f)
	}
	before = len(m.reqs())
	_, err = h.Result("google_sheets.append_row", map[string]any{"spreadsheet": sheetID, "values": map[string]any{}}, map[string]any{})
	if f := apitest.Failure(t, err); f.Code != "CONFIGURATION_ERROR" || len(m.reqs()) != before {
		t.Fatalf("no values: %+v", f)
	}
}

func TestUpdateRow(t *testing.T) {
	m := newMock(t)
	h := apitest.New(t, m.options())
	out, err := h.Result("google_sheets.update_row", map[string]any{"spreadsheet": sheetID, "sheet": "Leads",
		"match_column": "email", "match_value": "{{input.email}}", "values": map[string]any{"Status": "contacted"}},
		map[string]any{"email": "grace@example.test"})
	if err != nil {
		t.Fatal(err)
	}
	if out["row_number"] != 4.0 || out["updated_range"] != "Leads!A4:C4" ||
		mustJSON(out["row"]) != `{"Email":"grace@example.test","Name":"Grace","Status":"contacted","row_number":4}` {
		t.Fatalf("update: %v", out)
	}
	if mustJSON(m.grid["Leads"][3]) != `["Grace","grace@example.test","contacted"]` {
		t.Fatalf("sheet: %v", m.grid["Leads"])
	}
	out, err = h.Result("google_sheets.update_row", map[string]any{"spreadsheet": sheetID, "sheet": "Leads", "row_number": 2.0,
		"values": map[string]any{"Name": "Ada L."}}, map[string]any{"x": 1.0})
	if err != nil || mustJSON(m.grid["Leads"][1]) != `["Ada L.","ada@example.test","new"]` {
		t.Fatalf("by number: %v %v", out, err)
	}
	_, err = h.Result("google_sheets.update_row", map[string]any{"spreadsheet": sheetID, "sheet": "Leads",
		"match_column": "Email", "match_value": "nobody@example.test", "values": map[string]any{"Status": "x"}}, map[string]any{"x": 1.0})
	if f := apitest.Failure(t, err); f.Code != "RESOURCE_NOT_FOUND" || f.Retryable {
		t.Fatalf("no match: %+v", f)
	}
	_, err = h.Result("google_sheets.update_row", map[string]any{"spreadsheet": sheetID, "values": map[string]any{"Status": "x"}}, map[string]any{"x": 1.0})
	if f := apitest.Failure(t, err); f.Code != "CONFIGURATION_ERROR" {
		t.Fatalf("no row selector: %+v", f)
	}
}

func TestErrors(t *testing.T) {
	m := newMock(t)
	h := apitest.New(t, m.options())
	cases := []struct {
		status        int
		body, code    string
		retryable     bool
		messageSubstr string
	}{
		{403, `{"error":{"code":403,"status":"PERMISSION_DENIED","details":[{"reason":"ACCESS_TOKEN_SCOPE_INSUFFICIENT"}]}}`, "PERMISSION_DENIED", false, "reconnect it"},
		{403, `{"error":{"code":403,"status":"PERMISSION_DENIED"}}`, "PERMISSION_DENIED", false, "no access"},
		{429, `{}`, "RATE_LIMITED", true, ""},
		{404, `{}`, "RESOURCE_NOT_FOUND", false, ""},
		{503, `{}`, "PROVIDER_UNAVAILABLE", true, ""},
	}
	for _, c := range cases {
		m.mu.Lock()
		m.fail, m.failBody = c.status, c.body
		m.mu.Unlock()
		_, err := h.Result("google_sheets.read_rows", map[string]any{"spreadsheet": sheetID, "sheet": "Leads"}, map[string]any{})
		f := apitest.Failure(t, err)
		if f.Code != c.code || f.Retryable != c.retryable || !strings.Contains(f.Message, c.messageSubstr) {
			t.Fatalf("%d: %+v", c.status, f)
		}
		apitest.NoToken(t, "error", mustJSON(f))
	}
}

// Any upstream node connects to a Sheets or Calendar action, so its outputs
// can be used in {{variables}} (e.g. log each email to a sheet).
func TestChainsFromOtherNodesValidate(t *testing.T) {
	h := apitest.New(t, newMock(t).options())
	cred := h.CredID.String()
	pos := &workflow.Position{}
	def := workflow.Definition{Version: workflow.DefinitionSchemaVersion, Settings: map[string]any{},
		Nodes: []workflow.Node{
			{ID: "in", Type: "input", Name: "Input", Position: pos, Config: map[string]any{}},
			{ID: "search", Type: "gmail.search", Name: "Search", Position: pos, Config: map[string]any{"credential_id": cred}},
			{ID: "read", Type: "gmail.read", Name: "Read", Position: pos, Config: map[string]any{"credential_id": cred}},
			{ID: "log", Type: "google_sheets.append_row", Name: "Log", Position: pos, Config: map[string]any{"credential_id": cred,
				"spreadsheet": sheetID, "values": map[string]any{"Name": "{{read.sender}}", "Status": "{{read.subject}}"}}},
			{ID: "rows", Type: "google_sheets.read_rows", Name: "Rows", Position: pos, Config: map[string]any{"credential_id": cred, "spreadsheet": sheetID}},
			{ID: "cal", Type: "google_calendar.create_event", Name: "Event", Position: pos, Config: map[string]any{"credential_id": cred,
				"summary": "Follow up {{read.subject}}", "start": "2026-10-20"}},
			{ID: "out", Type: "output", Name: "Output", Position: pos, Config: map[string]any{}},
		},
		Edges: []workflow.Edge{
			{ID: "e1", Source: "in", SourcePort: "data", Target: "search", TargetPort: "query"},
			{ID: "e2", Source: "search", SourcePort: "message_id", Target: "read", TargetPort: "message_id"},
			{ID: "e3", Source: "read", SourcePort: "body", Target: "log", TargetPort: "values"},
			{ID: "e4", Source: "log", SourcePort: "row_number", Target: "rows", TargetPort: "data"},
			{ID: "e5", Source: "rows", SourcePort: "rows", Target: "cal", TargetPort: "description"},
			{ID: "e6", Source: "cal", SourcePort: "event_id", Target: "out", TargetPort: "value"},
		}}
	if res := workflow.NewValidator(h.App.NodeRegistry).Validate(def); !res.Valid {
		t.Fatalf("chain rejected: %+v", res.Errors)
	}
}
