package phased

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"hash/fnv"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"workflow-optimizer/internal/app"
	"workflow-optimizer/internal/config"
	"workflow-optimizer/internal/credential"
	"workflow-optimizer/internal/infrastructure/encryption"
	"workflow-optimizer/internal/infrastructure/postgres"
	"workflow-optimizer/internal/node"
)

const goodKey, badKey = "sk-good-key", "sk-rejected-key"

// fakeOpenAI answers /v1/embeddings with deterministic bag-of-words
// vectors (one dimension per hashed word), so texts sharing words are
// similar. A request with badKey is rejected (401).
func fakeOpenAI(t *testing.T) (*httptest.Server, *atomic.Int64) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/v1/embeddings" {
			w.WriteHeader(404)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+goodKey {
			w.WriteHeader(401)
			_, _ = io.WriteString(w, `{"error":{"type":"invalid_request_error","code":"invalid_api_key"}}`)
			return
		}
		var req struct {
			Input      []string `json:"input"`
			Dimensions int      `json:"dimensions"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		type item struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		}
		out := struct {
			Data []item `json:"data"`
		}{}
		for i, text := range req.Input {
			v := make([]float32, req.Dimensions)
			for _, word := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !('a' <= r && r <= 'z') }) {
				h := fnv.New32a()
				_, _ = h.Write([]byte(word))
				v[int(h.Sum32())%req.Dimensions]++
			}
			out.Data = append(out.Data, item{Index: i, Embedding: v})
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

// waitReady polls a base until no document is INDEXING; returns the documents.
func (e *env) waitReady(token, base string) []any {
	e.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		kb := e.must(e.call(token, "GET", "/api/v1/knowledge-bases/"+base, nil), 200)
		docs := kb["items"].([]any)
		busy := false
		for _, d := range docs {
			busy = busy || d.(map[string]any)["status"] == "INDEXING"
		}
		if !busy {
			return docs
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("documents still indexing: %v", docs)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestKnowledgeBases(t *testing.T) {
	openai, calls := fakeOpenAI(t)
	e := newEnvWith(t, func(c *config.Config) { c.OpenAIBaseURL = openai.URL + "/v1" })
	ada, grace := e.register("ada"), e.register("grace")
	cred := e.must(e.call(ada.token, "POST", "/api/v1/credentials", map[string]any{"workspace_id": ada.workspace, "name": "OpenAI",
		"provider": "openai", "credential_type": "api_key", "secret": goodKey}), 201)["id"].(string)

	kb := e.must(e.call(ada.token, "POST", "/api/v1/knowledge-bases", map[string]any{"workspace_id": ada.workspace, "name": "Support",
		"description": "Policies", "credential_id": cred}), 201)
	base := kb["id"].(string)
	if kb["embedding_model"] != "text-embedding-3-small" || kb["documents"] != 0.0 {
		t.Fatalf("created: %v", kb)
	}

	// A text document and a file document, indexed in the background.
	doc := e.must(e.call(ada.token, "POST", "/api/v1/knowledge-bases/"+base+"/documents", map[string]any{
		"title": "Refund policy", "text": "Refunds are processed within five business days after we receive the returned item."}), 202)
	if doc["status"] != "INDEXING" {
		t.Fatalf("added: %v", doc)
	}
	file := e.must(e.upload(ada.token, ada.workspace, "shipping.md", []byte("# Shipping\nOrders ship from our warehouse in two days. Express shipping is available.")), 201)
	e.must(e.call(ada.token, "POST", "/api/v1/knowledge-bases/"+base+"/documents", map[string]any{"file_id": file["id"]}), 202)
	docs := e.waitReady(ada.token, base)
	if len(docs) != 2 {
		t.Fatalf("documents: %v", docs)
	}
	for _, d := range docs {
		if d := d.(map[string]any); d["status"] != "READY" || d["chunks"] != 1.0 {
			t.Fatalf("document: %v", d)
		}
	}
	if docs[0].(map[string]any)["title"] != "shipping.md" || docs[0].(map[string]any)["source"] != "file" {
		t.Fatalf("file document: %v", docs[0])
	}

	hits := e.must(e.call(ada.token, "POST", "/api/v1/knowledge-bases/"+base+"/search", map[string]any{"query": "how many days do refunds take", "top_k": 2}), 200)["items"].([]any)
	if len(hits) != 2 || hits[0].(map[string]any)["document_title"] != "Refund policy" || hits[0].(map[string]any)["score"].(float64) <= hits[1].(map[string]any)["score"].(float64) {
		t.Fatalf("search: %v", hits)
	}

	// The nodes, through the same composition the worker uses.
	reg := e.engine(t)
	searchNode, _ := reg.Get("knowledge_search")
	wsID := uuid.MustParse(ada.workspace)
	out, err := searchNode.Execute(context.Background(), node.NodeInput{Scope: node.Scope{WorkspaceID: wsID},
		Config: map[string]any{"knowledge_base_id": base, "top_k": 1.0}, Ports: map[string]node.Value{"query": node.NewStringValue("express shipping options")}})
	if err != nil {
		t.Fatal(err)
	}
	if ctx, _ := out.GetPort("context"); !strings.HasPrefix(ctx.Data.(string), "[1] shipping.md\n") {
		t.Fatalf("context: %v", ctx.Data)
	}
	addNode, _ := reg.Get("knowledge_add")
	add := node.NodeInput{Scope: node.Scope{WorkspaceID: wsID}, IdempotencyKey: "exec-1/add",
		Config: map[string]any{"knowledge_base_id": base, "title": "Ticket 42", "text": "Customer asked about warranty coverage for two years."}}
	first, err := addNode.Execute(context.Background(), add)
	if err != nil {
		t.Fatal(err)
	}
	before := calls.Load()
	again, err := addNode.Execute(context.Background(), add) // a retried attempt
	if err != nil {
		t.Fatal(err)
	}
	id1, _ := first.GetPort("document_id")
	id2, _ := again.GetPort("document_id")
	if id1.Data != id2.Data || calls.Load() != before {
		t.Fatalf("idempotent add: %v %v (embedding calls %d)", id1.Data, id2.Data, calls.Load()-before)
	}
	if n := len(e.must(e.call(ada.token, "GET", "/api/v1/knowledge-bases/"+base, nil), 200)["items"].([]any)); n != 3 {
		t.Fatalf("documents after node add: %d", n)
	}
	// Another workspace cannot search through the node.
	if _, err := searchNode.Execute(context.Background(), node.NodeInput{Scope: node.Scope{WorkspaceID: uuid.MustParse(grace.workspace)},
		Config: map[string]any{"knowledge_base_id": base, "query": "refunds"}}); err == nil {
		t.Fatal("another workspace searched the base")
	}

	// Isolation through the API.
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/v1/knowledge-bases/" + base}, {"DELETE", "/api/v1/knowledge-bases/" + base},
		{"POST", "/api/v1/knowledge-bases/" + base + "/search"},
	} {
		if r := e.call(grace.token, c.method, c.path, map[string]any{"query": "x"}); r.status != 404 {
			t.Fatalf("%s %s by another workspace: %d", c.method, c.path, r.status)
		}
	}
	if r := e.call(grace.token, "POST", "/api/v1/knowledge-bases", map[string]any{"workspace_id": grace.workspace, "name": "Steal",
		"credential_id": cred}); r.status != 400 {
		t.Fatalf("another workspace's credential: %d %s", r.status, r.body)
	}

	// The credential cannot be deleted while the base uses it.
	if r := e.call(ada.token, "DELETE", "/api/v1/credentials/"+cred, nil); r.status != 409 || !strings.Contains(string(r.body), "knowledge base") {
		t.Fatalf("delete used credential: %d %s", r.status, r.body)
	}

	// A rejected key fails the document with a clear reason; it can be retried.
	bad := e.must(e.call(ada.token, "POST", "/api/v1/credentials", map[string]any{"workspace_id": ada.workspace, "name": "Old key",
		"provider": "openai", "credential_type": "api_key", "secret": badKey}), 201)["id"].(string)
	badBase := e.must(e.call(ada.token, "POST", "/api/v1/knowledge-bases", map[string]any{"workspace_id": ada.workspace, "name": "Broken", "credential_id": bad}), 201)["id"].(string)
	failed := e.must(e.call(ada.token, "POST", "/api/v1/knowledge-bases/"+badBase+"/documents", map[string]any{"text": "Some notes"}), 202)["id"].(string)
	d := e.waitReady(ada.token, badBase)[0].(map[string]any)
	if d["status"] != "FAILED" || d["error"] != "OpenAI rejected the API key" || d["title"] != "Some notes" {
		t.Fatalf("failed document: %v", d)
	}
	if r := e.call(ada.token, "POST", "/api/v1/knowledge-bases/"+badBase+"/documents/"+failed+"/reindex", nil); r.status != 202 {
		t.Fatalf("reindex: %d %s", r.status, r.body)
	}
	e.waitReady(ada.token, badBase)
	if r := e.call(ada.token, "POST", "/api/v1/knowledge-bases/"+badBase+"/search", map[string]any{"query": "notes"}); r.status != 200 {
		t.Fatalf("search an empty base: %d %s", r.status, r.body) // no ready chunks: no embedding call needed
	}

	// Delete a document, then the base; the credential is then free.
	if r := e.call(ada.token, "DELETE", "/api/v1/knowledge-bases/"+base+"/documents/"+doc["id"].(string), nil); r.status != 204 {
		t.Fatalf("delete document: %d", r.status)
	}
	if r := e.call(ada.token, "DELETE", "/api/v1/knowledge-bases/"+base, nil); r.status != 204 {
		t.Fatalf("delete base: %d", r.status)
	}
	if r := e.call(ada.token, "DELETE", "/api/v1/credentials/"+cred, nil); r.status != 204 {
		t.Fatalf("delete credential after its base: %d %s", r.status, r.body)
	}
	if bases := e.must(e.call(ada.token, "GET", "/api/v1/knowledge-bases?workspace_id="+ada.workspace, nil), 200)["items"].([]any); len(bases) != 1 {
		t.Fatalf("bases left: %v", bases)
	}
}

// engine composes the node registry as the worker does, on the test schema.
func (e *env) engine(t *testing.T) node.Registry {
	t.Helper()
	store, err := postgres.Open(context.Background(), e.cfg.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	key, _ := base64.StdEncoding.DecodeString(e.cfg.CredentialEncryptionKey)
	enc, _ := encryption.NewAESGCM(key)
	creds, _ := credential.NewService(postgres.NewCredentialRepository(store), enc)
	a, err := app.BootstrapWith(e.cfg, app.Dependencies{Credentials: creds, Files: postgres.NewFileRepository(store),
		KnowledgeRepository: postgres.NewKnowledgeRepository(store)})
	if err != nil {
		t.Fatal(err)
	}
	return a.NodeRegistry
}
