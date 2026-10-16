package forms_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"workflow-optimizer/internal/app"
	"workflow-optimizer/internal/integration/forms"
	"workflow-optimizer/internal/integration/googleapi/apitest"
	"workflow-optimizer/internal/node"
)

const formID = "1FAIpQLSfFormIdFormIdFormIdFormId"

// mockForms is a tiny Google Forms API: one form with three responses over
// two pages.
type mockForms struct {
	mu     sync.Mutex
	srv    *httptest.Server
	paths  []string
	status int
}

func newMock(t *testing.T) *mockForms {
	m := &mockForms{}
	m.srv = httptest.NewServer(http.HandlerFunc(m.serve))
	t.Cleanup(m.srv.Close)
	return m
}

func (m *mockForms) options() app.Dependencies {
	return app.Dependencies{Forms: forms.Options{BaseURL: m.srv.URL + "/v1", HTTPClient: m.srv.Client()}}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func text(vals ...string) map[string]any {
	answers := []any{}
	for _, v := range vals {
		answers = append(answers, map[string]any{"value": v})
	}
	return map[string]any{"textAnswers": map[string]any{"answers": answers}}
}

func (m *mockForms) serve(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.paths = append(m.paths, r.URL.Path+"?"+r.URL.RawQuery)
	switch {
	case !apitest.Authorized(r):
		writeJSON(w, 401, map[string]any{"error": map[string]any{"code": 401}})
	case m.status != 0:
		writeJSON(w, m.status, map[string]any{"error": map[string]any{"code": m.status}})
	case r.URL.Path == "/v1/forms/"+formID:
		writeJSON(w, 200, map[string]any{"formId": formID, "info": map[string]any{"title": "Signup"}, "items": []any{
			map[string]any{"title": "Name", "questionItem": map[string]any{"question": map[string]any{"questionId": "q1"}}},
			map[string]any{"title": "A picture", "imageItem": map[string]any{}},
			map[string]any{"title": "Topics", "questionItem": map[string]any{"question": map[string]any{"questionId": "q2"}}},
			map[string]any{"title": "Rate", "questionGroupItem": map[string]any{"questions": []any{
				map[string]any{"questionId": "g1", "rowQuestion": map[string]any{"title": "Food"}},
			}}},
			map[string]any{"title": "Name", "questionItem": map[string]any{"question": map[string]any{"questionId": "q3"}}},
		}})
	case r.URL.Path == "/v1/forms/"+formID+"/responses" && r.URL.Query().Get("pageToken") == "":
		writeJSON(w, 200, map[string]any{"nextPageToken": "p2", "responses": []any{
			map[string]any{"responseId": "r1", "lastSubmittedTime": "2026-10-01T10:00:00Z", "respondentEmail": "ada@example.test",
				"answers": map[string]any{"q1": text("Ada"), "q2": text("Go", "Maths"), "g1": text("5")}},
			map[string]any{"responseId": "r3", "lastSubmittedTime": "2026-10-03T10:00:00Z", "answers": map[string]any{"q1": text("Grace"), "q3": text("Hopper")}},
		}})
	case r.URL.Path == "/v1/forms/"+formID+"/responses":
		writeJSON(w, 200, map[string]any{"responses": []any{
			map[string]any{"responseId": "r2", "createTime": "2026-10-02T10:00:00Z", "answers": map[string]any{
				"q1": map[string]any{"fileUploadAnswers": map[string]any{"answers": []any{map[string]any{"fileId": "f", "fileName": "cv.pdf"}}}}}},
		}})
	default:
		writeJSON(w, 404, map[string]any{"error": map[string]any{"code": 404}})
	}
}

func TestFormID(t *testing.T) {
	for in, want := range map[string]string{
		"https://docs.google.com/forms/d/" + formID + "/edit":     formID,
		"https://docs.google.com/forms/u/0/d/" + formID + "/edit": formID,
		formID: formID,
	} {
		if got, err := forms.FormID(in); err != nil || got != want {
			t.Errorf("FormID(%q) = %q, %v", in, got, err)
		}
	}
	// The link people fill the form in at does not name the form.
	for _, bad := range []string{"https://docs.google.com/forms/d/e/1FAIpQLSeResponderResponderResponder/viewform", "https://forms.gle/abc123"} {
		if _, err := forms.FormID(bad); err == nil || !strings.Contains(err.Error(), "/edit") {
			t.Errorf("FormID(%q) = %v", bad, err)
		}
	}
	if _, err := forms.FormID("signup"); err == nil {
		t.Error("a name is not a form")
	}
}

func TestReadResponses(t *testing.T) {
	m := newMock(t)
	h := apitest.New(t, m.options())
	d, err := h.App.NodeRegistry.GetDefinition("google_forms.read_responses")
	if err != nil || d.SideEffects != node.SideEffectsNone || d.Integration == nil || d.Integration.Name != "Google Forms" || d.Auth.Provider != "google" {
		t.Fatalf("%+v %v", d, err)
	}
	out, err := h.Result("google_forms.read_responses", map[string]any{"form": "https://docs.google.com/forms/d/" + formID + "/edit"}, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	qs, _ := json.Marshal(out["questions"])
	if string(qs) != `["Name","Topics","Rate [Food]","Name (2)"]` {
		t.Fatalf("questions: %s", qs)
	}
	rows := out["responses"].([]any)
	if out["count"] != 3.0 || len(rows) != 3 {
		t.Fatalf("responses: %v", out)
	}
	// Newest first, answers by question title.
	newest, second, oldest := rows[0].(map[string]any), rows[1].(map[string]any), rows[2].(map[string]any)
	if newest["response_id"] != "r3" || newest["Name"] != "Grace" || newest["Name (2)"] != "Hopper" || newest["Topics"] != "" {
		t.Fatalf("newest: %v", newest)
	}
	if second["response_id"] != "r2" || second["Name"] != "cv.pdf" || second["submitted_at"] != "2026-10-02T10:00:00Z" {
		t.Fatalf("second: %v", second)
	}
	if oldest["Topics"] != "Go, Maths" || oldest["Rate [Food]"] != "5" || oldest["email"] != "ada@example.test" {
		t.Fatalf("oldest: %v", oldest)
	}
	if latest := out["latest"].(map[string]any); latest["response_id"] != "r3" {
		t.Fatalf("latest: %v", latest)
	}

	// Max responses keeps the newest.
	out, err = h.Result("google_forms.read_responses", map[string]any{"form": formID, "max_responses": 1.0}, map[string]any{})
	if err != nil || out["count"] != 1.0 || out["responses"].([]any)[0].(map[string]any)["response_id"] != "r3" {
		t.Fatalf("max: %v %v", out, err)
	}
}

func TestErrors(t *testing.T) {
	m := newMock(t)
	h := apitest.New(t, m.options())
	_, err := h.Result("google_forms.read_responses", map[string]any{"form": "https://forms.gle/abc123"}, map[string]any{})
	if f := apitest.Failure(t, err); !strings.Contains(f.Message, "/edit") {
		t.Fatalf("responder link: %+v", f)
	}
	m.status = http.StatusForbidden
	_, err = h.Result("google_forms.read_responses", map[string]any{"form": formID}, map[string]any{})
	if f := apitest.Failure(t, err); f.Code != "PERMISSION_DENIED" {
		t.Fatalf("forbidden: %+v", f)
	}
}
