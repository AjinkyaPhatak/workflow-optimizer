package docs_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"workflow-optimizer/internal/app"
	"workflow-optimizer/internal/integration/docs"
	"workflow-optimizer/internal/integration/googleapi/apitest"
	"workflow-optimizer/internal/node"
)

const docID = "1AbCdEfGhIjKlMnOpQrStUvWxYz0123456789"

type request struct {
	Method, Path string
	Body         map[string]any
	Authorized   bool
}

// mockDocs is a tiny Google Docs API: documents of plain text.
type mockDocs struct {
	mu       sync.Mutex
	srv      *httptest.Server
	docs     map[string]string // id -> body text (ending with "\n", as Docs keeps it)
	titles   map[string]string
	requests []request
	status   int
}

func newMock(t *testing.T) *mockDocs {
	m := &mockDocs{docs: map[string]string{docID: "Hello\n"}, titles: map[string]string{docID: "Notes"}}
	m.srv = httptest.NewServer(http.HandlerFunc(m.serve))
	t.Cleanup(m.srv.Close)
	return m
}

func (m *mockDocs) options() app.Dependencies {
	return app.Dependencies{Docs: docs.Options{BaseURL: m.srv.URL + "/v1", HTTPClient: m.srv.Client()}}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (m *mockDocs) document(id string) map[string]any {
	// The text as one paragraph, plus a one-cell table when it has one.
	return map[string]any{"documentId": id, "title": m.titles[id], "body": map[string]any{"content": []any{
		map[string]any{"sectionBreak": map[string]any{}},
		map[string]any{"paragraph": map[string]any{"elements": []any{map[string]any{"textRun": map[string]any{"content": m.docs[id]}}}}},
	}}}
}

func (m *mockDocs) serve(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	req := request{Method: r.Method, Path: r.URL.Path, Authorized: apitest.Authorized(r)}
	_ = json.Unmarshal(body, &req.Body)
	m.requests = append(m.requests, req)
	id, update := strings.CutSuffix(strings.TrimPrefix(r.URL.Path, "/v1/documents/"), ":batchUpdate")
	switch {
	case !req.Authorized:
		writeJSON(w, 401, map[string]any{"error": map[string]any{"code": 401}})
	case m.status != 0:
		writeJSON(w, m.status, map[string]any{"error": map[string]any{"code": m.status, "status": "PERMISSION_DENIED",
			"details": []any{map[string]any{"reason": "ACCESS_TOKEN_SCOPE_INSUFFICIENT"}}}})
	case r.URL.Path == "/v1/documents" && r.Method == http.MethodPost:
		id := "1NewDocumentIdNewDocumentIdNewDocId"
		m.docs[id], m.titles[id] = "\n", req.Body["title"].(string)
		writeJSON(w, 200, m.document(id))
	case m.docs[id] == "":
		writeJSON(w, 404, map[string]any{"error": map[string]any{"code": 404}})
	case update && r.Method == http.MethodPost:
		ins := req.Body["requests"].([]any)[0].(map[string]any)["insertText"].(map[string]any)
		m.docs[id] = strings.TrimSuffix(m.docs[id], "\n") + ins["text"].(string) + "\n"
		writeJSON(w, 200, map[string]any{"documentId": id})
	case r.Method == http.MethodGet:
		writeJSON(w, 200, m.document(id))
	default:
		writeJSON(w, 404, map[string]any{"error": map[string]any{"code": 404}})
	}
}

func TestDocumentID(t *testing.T) {
	for in, want := range map[string]string{
		"https://docs.google.com/document/d/" + docID + "/edit?tab=t.0": docID,
		"https://docs.google.com/document/u/1/d/" + docID + "/edit":     docID,
		docID: docID,
	} {
		if got, err := docs.DocumentID(in); err != nil || got != want {
			t.Errorf("DocumentID(%q) = %q, %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "notes", "https://docs.google.com/spreadsheets/d/x/edit"} {
		if _, err := docs.DocumentID(bad); err == nil {
			t.Errorf("DocumentID(%q) must fail", bad)
		}
	}
}

func TestText(t *testing.T) {
	var d docs.Document
	_ = json.Unmarshal([]byte(`{"body":{"content":[{"sectionBreak":{}},
		{"paragraph":{"elements":[{"textRun":{"content":"Title\n"}}]}},
		{"table":{"tableRows":[{"tableCells":[{"content":[{"paragraph":{"elements":[{"textRun":{"content":"a\n"}}]}}]},
			{"content":[{"paragraph":{"elements":[{"textRun":{"content":"b\n"}}]}}]}]}]}},
		{"paragraph":{"elements":[{"textRun":{"content":"End"}},{"inlineObjectElement":{}},{"textRun":{"content":".\n"}}]}}]}}`), &d)
	if got := d.Text(); got != "Title\na\nb\nEnd." {
		t.Fatalf("text: %q", got)
	}
}

func TestMetadata(t *testing.T) {
	h := apitest.New(t, newMock(t).options())
	want := map[string]node.SideEffects{"google_docs.read_document": node.SideEffectsNone,
		"google_docs.create_document": node.SideEffectsUnsafe, "google_docs.append_text": node.SideEffectsUnsafe}
	for typ, se := range want {
		d, err := h.App.NodeRegistry.GetDefinition(typ)
		if err != nil {
			t.Fatal(err)
		}
		if d.SideEffects != se || d.Integration == nil || d.Integration.Name != "Google Docs" || d.Auth == nil || d.Auth.Provider != "google" {
			t.Fatalf("%s: %+v", typ, d)
		}
	}
}

func TestReadCreateAppend(t *testing.T) {
	m := newMock(t)
	h := apitest.New(t, m.options())
	link := "https://docs.google.com/document/d/" + docID + "/edit"
	out, err := h.Result("google_docs.read_document", map[string]any{"document": link}, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if out["text"] != "Hello" || out["title"] != "Notes" || out["document_id"] != docID || out["link"] != link {
		t.Fatalf("read: %v", out)
	}

	// Added text goes on a new line.
	if _, err := h.Result("google_docs.append_text", map[string]any{"document": docID, "text": "World"}, map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if m.docs[docID] != "Hello\nWorld\n" {
		t.Fatalf("append: %q", m.docs[docID])
	}

	out, err = h.Result("google_docs.create_document", map[string]any{"title": "Summary", "text": "Line one"}, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	id, _ := out["document_id"].(string)
	if out["title"] != "Summary" || m.docs[id] != "Line one\n" || !strings.HasSuffix(out["link"].(string), id+"/edit") {
		t.Fatalf("create: %v %q", out, m.docs[id])
	}
	// The first text in an empty document is not preceded by a new line.
	if _, err := h.Result("google_docs.append_text", map[string]any{"document": id, "text": "two"}, map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if m.docs[id] != "Line one\ntwo\n" {
		t.Fatalf("append to created: %q", m.docs[id])
	}
	for _, r := range m.requests {
		if !r.Authorized {
			t.Fatalf("unauthorized request %+v", r)
		}
	}
}

func TestErrors(t *testing.T) {
	m := newMock(t)
	h := apitest.New(t, m.options())
	_, err := h.Result("google_docs.read_document", map[string]any{"document": "1MissingMissingMissingMissing"}, map[string]any{})
	if f := apitest.Failure(t, err); f.Code != "RESOURCE_NOT_FOUND" {
		t.Fatalf("missing document: %+v", f)
	}
	_, err = h.Result("google_docs.read_document", map[string]any{"document": "not a link"}, map[string]any{})
	if f := apitest.Failure(t, err); !strings.Contains(f.Message, "Google Docs link") {
		t.Fatalf("bad link: %+v", f)
	}
	// An account connected before Docs was added lacks the scope.
	m.status = http.StatusForbidden
	_, err = h.Result("google_docs.read_document", map[string]any{"document": docID}, map[string]any{})
	f := apitest.Failure(t, err)
	if !strings.Contains(f.Message, "reconnect") {
		t.Fatalf("scope: %+v", f)
	}
	apitest.NoToken(t, "error", f.Message)
}
