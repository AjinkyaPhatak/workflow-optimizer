package toolbox

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"workflow-optimizer/internal/automation"
	"workflow-optimizer/internal/files"
	"workflow-optimizer/internal/integration/whatsapp"
)

type actionStore struct {
	automation.Store
	results map[string]*automation.ToolResult
}

func (s *actionStore) ClaimAction(_ context.Context, c automation.ToolCall) (bool, *automation.ToolResult, error) {
	key := c.RunID.String() + c.OperationID
	if value, ok := s.results[key]; ok {
		return false, value, nil
	}
	s.results[key] = nil
	return true, nil, nil
}
func (s *actionStore) FinishAction(_ context.Context, c automation.ToolCall, result automation.ToolResult, status string) error {
	if status == "COMPLETED" {
		copy := result
		s.results[c.RunID.String()+c.OperationID] = &copy
	}
	return nil
}

type fileStore struct {
	files.Repository
	metadata map[uuid.UUID]files.File
	data     map[uuid.UUID][]byte
}

func (s *fileStore) Get(_ context.Context, ws, id uuid.UUID) (files.File, error) {
	f, ok := s.metadata[id]
	if !ok || f.WorkspaceID != ws {
		return files.File{}, files.ErrNotFound
	}
	return f, nil
}
func (s *fileStore) Create(_ context.Context, f files.File, data []byte) (files.File, error) {
	s.metadata[f.ID] = f
	s.data[f.ID] = append([]byte{}, data...)
	return f, nil
}
func (s *fileStore) Content(ctx context.Context, ws, id uuid.UUID) (files.File, []byte, error) {
	f, err := s.Get(ctx, ws, id)
	return f, s.data[id], err
}
func TestUncertainSendAsksAndNeverBlindlyRetries(t *testing.T) {
	sends := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer internal" {
			t.Error("missing internal authentication")
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/contacts") {
			_ = json.NewEncoder(w).Encode([]whatsapp.Contact{{ID: "phatak", Name: "Phatak"}})
			return
		}
		sends++
		w.WriteHeader(503)
	}))
	defer server.Close()
	store := &actionStore{results: map[string]*automation.ToolResult{}}
	box := &Toolbox{Store: store, WhatsApp: &whatsapp.Client{URL: server.URL, Token: "internal"}}
	call := automation.ToolCall{WorkspaceID: uuid.New(), UserID: uuid.New(), RunID: uuid.New(), OperationID: "send", Tool: "whatsapp.send", Inputs: map[string]any{"recipient": "Phatak", "text": "The requested report"}}
	_, err := box.Execute(context.Background(), call)
	var need *automation.NeedInput
	if !errors.As(err, &need) || need.Question.Input != "delivery_resolution" || sends != 1 {
		t.Fatalf("uncertain send: %v sends=%d", err, sends)
	}
	_, err = box.Execute(context.Background(), call)
	if !errors.As(err, &need) || sends != 1 {
		t.Fatal("uncertain send was repeated")
	}
	call.Inputs["delivery_resolution"] = "skip"
	result, err := box.Execute(context.Background(), call)
	if err != nil || result.Outputs["receipt"] == nil || sends != 1 {
		t.Fatalf("reconciliation: %v sends=%d", err, sends)
	}
}
func TestAmbiguousContactDoesNotClaimOrSend(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/contacts") {
			t.Error("unexpected send")
		}
		_ = json.NewEncoder(w).Encode([]whatsapp.Contact{{ID: "one", Name: "Ajinkya Phatak"}, {ID: "two", Name: "Other Phatak"}})
	}))
	defer server.Close()
	store := &actionStore{results: map[string]*automation.ToolResult{}}
	box := &Toolbox{Store: store, WhatsApp: &whatsapp.Client{URL: server.URL, Token: "internal"}}
	_, err := box.Execute(context.Background(), automation.ToolCall{RunID: uuid.New(), Tool: "whatsapp.send", Inputs: map[string]any{"recipient": "Phatak", "text": "Hello"}})
	var need *automation.NeedInput
	if !errors.As(err, &need) || len(need.Question.Options) != 2 || len(store.results) != 0 {
		t.Fatal("ambiguous contact was not stopped before the send")
	}
}

func TestMisspelledContactUsesMatchedIDAndRetainsSendDeduplication(t *testing.T) {
	sends := 0
	contactReads := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/contacts") {
			contactReads++
			if contactReads == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			_ = json.NewEncoder(w).Encode([]whatsapp.Contact{{ID: "correct-jid", Name: "Ajinkya Phatak"}, {ID: "unrelated", Name: "Ritwik Sundar"}})
			return
		}
		if !strings.HasSuffix(r.URL.Path, "/send") {
			t.Errorf("unexpected operation %s", r.URL.Path)
		}
		var request whatsapp.SendRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Recipient != "correct-jid" || request.Text != "The requested value" {
			t.Errorf("incorrect resolved send: %+v %v", request, err)
		}
		sends++
		_ = json.NewEncoder(w).Encode(whatsapp.Receipt{ID: "synthetic-receipt", Status: "sent"})
	}))
	defer server.Close()
	store := &actionStore{results: map[string]*automation.ToolResult{}}
	box := &Toolbox{Store: store, WhatsApp: &whatsapp.Client{URL: server.URL, Token: "internal"}}
	call := automation.ToolCall{RunID: uuid.New(), OperationID: "same-send", Tool: "whatsapp.send", Inputs: map[string]any{"recipient": "Ajinkya Fatak", "text": "The requested value"}}
	for i := 0; i < 2; i++ {
		if _, err := box.Execute(context.Background(), call); err != nil {
			t.Fatal(err)
		}
	}
	if sends != 1 || contactReads != 3 {
		t.Fatalf("contact reconnect/deduplication: sends=%d contact reads=%d", sends, contactReads)
	}
}

func TestFuzzyContactTieDoesNotClaimOrSend(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/contacts") {
			t.Error("ambiguous fuzzy match sent a message")
			http.Error(w, "unexpected send", http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode([]whatsapp.Contact{{ID: "one", Name: "Ajinkya Phatak"}, {ID: "two", Name: "Other Phatak"}})
	}))
	defer server.Close()
	store := &actionStore{results: map[string]*automation.ToolResult{}}
	box := &Toolbox{Store: store, WhatsApp: &whatsapp.Client{URL: server.URL, Token: "internal"}}
	_, err := box.Execute(context.Background(), automation.ToolCall{RunID: uuid.New(), Tool: "whatsapp.send", Inputs: map[string]any{"recipient": "Phatac", "text": "The requested value"}})
	var need *automation.NeedInput
	if !errors.As(err, &need) || len(need.Question.Options) != 2 || len(store.results) != 0 {
		t.Fatalf("ambiguous fuzzy contact was not clarified before claiming: %v", err)
	}
}
func TestCSVAndLargeArtifactReferences(t *testing.T) {
	store := &fileStore{metadata: map[uuid.UUID]files.File{}, data: map[uuid.UUID][]byte{}}
	box := &Toolbox{Files: store}
	call := automation.ToolCall{WorkspaceID: uuid.New(), UserID: uuid.New(), RunID: uuid.New(), OperationID: "csv", Tool: "table.csv", Inputs: map[string]any{
		"rows": []any{map[string]any{"name": "=HYPERLINK(\"bad\")", "change": -2.5}, map[string]any{"name": "Phatak", "change": 4.0}},
	}}
	result, err := box.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Outputs["text"].(string), "-2.5") {
		t.Fatal("numeric losses were corrupted")
	}
	if !strings.Contains(result.Outputs["text"].(string), "'=HYPERLINK") {
		t.Fatal("spreadsheet formula was left executable")
	}
	id := uuid.New()
	raw := []byte(`{"nested":{"amount":42}}`)
	store.metadata[id] = files.File{ID: id, WorkspaceID: call.WorkspaceID}
	store.data[id] = raw
	outputs := map[string]map[string]any{"large": {"result": map[string]any{"$artifact": id.String(), "$path": ""}}}
	ref, err := automation.Resolve(automation.Binding{Source: "step", Ref: "large.result.nested.amount"}, outputs, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := box.Hydrate(context.Background(), call, ref)
	if err != nil || actual != 42.0 {
		t.Fatalf("artifact field resolution: %v %v", actual, err)
	}
	call.WorkspaceID = uuid.New()
	if _, err := box.Hydrate(context.Background(), call, ref); !errors.Is(err, files.ErrNotFound) {
		t.Fatal("cross-workspace artifact was readable")
	}
}
