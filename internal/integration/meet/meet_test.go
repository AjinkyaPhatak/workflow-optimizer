package meet_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"workflow-optimizer/internal/app"
	"workflow-optimizer/internal/integration/googleapi/apitest"
	"workflow-optimizer/internal/integration/meet"
	"workflow-optimizer/internal/node"
	"workflow-optimizer/internal/workflow"
)

type request struct {
	Method, Path string
	Body         map[string]any
	Authorized   bool
}

// mockMeet is a tiny Google Meet API that creates spaces.
type mockMeet struct {
	mu       sync.Mutex
	srv      *httptest.Server
	requests []request
	status   int // answer with this status instead (0: success)
}

func newMock(t *testing.T) *mockMeet {
	m := &mockMeet{}
	m.srv = httptest.NewServer(http.HandlerFunc(m.serve))
	t.Cleanup(m.srv.Close)
	return m
}

func (m *mockMeet) options() app.Dependencies {
	return app.Dependencies{Meet: meet.Options{BaseURL: m.srv.URL + "/v2", HTTPClient: m.srv.Client()}}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (m *mockMeet) serve(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	req := request{Method: r.Method, Path: r.URL.Path, Authorized: apitest.Authorized(r)}
	_ = json.Unmarshal(body, &req.Body)
	m.requests = append(m.requests, req)
	switch {
	case !req.Authorized:
		writeJSON(w, 401, map[string]any{"error": map[string]any{"code": 401}})
	case m.status != 0:
		writeJSON(w, m.status, map[string]any{"error": map[string]any{"code": m.status, "status": "PERMISSION_DENIED",
			"details": []any{map[string]any{"reason": "ACCESS_TOKEN_SCOPE_INSUFFICIENT"}}}})
	case r.URL.Path == "/v2/spaces" && r.Method == http.MethodPost:
		writeJSON(w, 200, map[string]any{"name": "spaces/jQCFfuBOdN5z", "meetingUri": "https://meet.google.com/abc-mnop-xyz",
			"meetingCode": "abc-mnop-xyz", "config": req.Body["config"]})
	default:
		writeJSON(w, 404, map[string]any{"error": map[string]any{"code": 404}})
	}
}

func (m *mockMeet) last() request {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.requests[len(m.requests)-1]
}

func TestMetadata(t *testing.T) {
	h := apitest.New(t, newMock(t).options())
	d, err := h.App.NodeRegistry.GetDefinition("google_meet.create_meeting")
	if err != nil {
		t.Fatal(err)
	}
	if d.SideEffects != node.SideEffectsUnsafe || d.Integration == nil || d.Integration.Name != "Google Meet" || d.Auth == nil || d.Auth.Provider != "google" {
		t.Fatalf("%+v", d)
	}
}

func TestCreateMeeting(t *testing.T) {
	m := newMock(t)
	h := apitest.New(t, m.options())
	out, err := h.Result("google_meet.create_meeting", map[string]any{}, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if out["meeting_link"] != "https://meet.google.com/abc-mnop-xyz" || out["meeting_code"] != "abc-mnop-xyz" || out["space"] != "spaces/jQCFfuBOdN5z" {
		t.Fatalf("create: %v", out)
	}
	if req := m.last(); req.Method != http.MethodPost || req.Path != "/v2/spaces" || req.Body["config"] != nil || !req.Authorized {
		t.Fatalf("request: %+v", req)
	}
	// Who can join is sent as the space's access type.
	if _, err := h.Result("google_meet.create_meeting", map[string]any{"access_type": "RESTRICTED"}, map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if b, _ := json.Marshal(m.last().Body); string(b) != `{"config":{"accessType":"RESTRICTED"}}` {
		t.Fatalf("body: %s", b)
	}
	// An unknown access type is rejected before the run.
	def := h.Definition("google_meet.create_meeting", map[string]any{"access_type": "EVERYONE"})
	if res := workflow.NewValidator(h.App.NodeRegistry).Validate(def); res.Valid {
		t.Fatal("an unknown access type must not validate")
	}
}

// An account connected before Meet was added lacks the scope.
func TestScopeErrorAsksToReconnect(t *testing.T) {
	m := newMock(t)
	m.status = http.StatusForbidden
	h := apitest.New(t, m.options())
	_, err := h.Result("google_meet.create_meeting", map[string]any{}, map[string]any{})
	f := apitest.Failure(t, err)
	if f.Code != "PERMISSION_DENIED" || f.Retryable || !strings.Contains(f.Message, "reconnect it on the Connected accounts page") {
		t.Fatalf("%+v", f)
	}
	b, _ := json.Marshal(f)
	apitest.NoToken(t, "error", string(b))
}
