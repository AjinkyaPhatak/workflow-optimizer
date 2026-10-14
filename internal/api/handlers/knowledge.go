package handlers

import (
	"net/http"
	"strings"

	"github.com/google/uuid"

	"workflow-optimizer/internal/api/httpx"
	"workflow-optimizer/internal/api/responses"
)

type createKnowledgeBase struct {
	WorkspaceID  string `json:"workspace_id"`
	Name         string `json:"name"`
	Description  string `json:"description"`
	CredentialID string `json:"credential_id"`
}

type addDocument struct {
	FileID string `json:"file_id"`
	Title  string `json:"title"`
	Text   string `json:"text"`
}

type searchKnowledge struct {
	Query string `json:"query"`
	TopK  int    `json:"top_k"`
}

func parseID(field, raw string) (uuid.UUID, error) {
	id, err := uuid.Parse(strings.TrimSpace(raw))
	if err != nil {
		return uuid.Nil, httpx.BadRequest(field + " must be a UUID")
	}
	return id, nil
}

// CreateKnowledgeBase handles POST /api/v1/knowledge-bases (Phase D2).
func (h *Handlers) CreateKnowledgeBase(w http.ResponseWriter, r *http.Request) {
	var req createKnowledgeBase
	if !h.decode(w, r, &req) {
		return
	}
	ws, err := parseID("workspace_id", req.WorkspaceID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	cred, err := parseID("credential_id", req.CredentialID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	b, err := h.Knowledge.Create(r.Context(), user(r), ws, req.Name, req.Description, cred)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, responses.NewKnowledgeBase(b))
}

// ListKnowledgeBases handles GET /api/v1/knowledge-bases?workspace_id=.
func (h *Handlers) ListKnowledgeBases(w http.ResponseWriter, r *http.Request) {
	ws, ok := h.queryID(w, r, "workspace_id")
	if !ok {
		return
	}
	bs, err := h.Knowledge.List(r.Context(), user(r), ws)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := responses.List[responses.KnowledgeBase]{Items: make([]responses.KnowledgeBase, 0, len(bs))}
	for _, b := range bs {
		out.Items = append(out.Items, responses.NewKnowledgeBase(b))
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// GetKnowledgeBase handles GET /api/v1/knowledge-bases/{baseID} (with its
// documents).
func (h *Handlers) GetKnowledgeBase(w http.ResponseWriter, r *http.Request) {
	id, ok := h.pathID(w, r, "baseID")
	if !ok {
		return
	}
	b, docs, err := h.Knowledge.Get(r.Context(), user(r), id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, responses.NewKnowledgeBaseDetail(b, docs))
}

// DeleteKnowledgeBase handles DELETE /api/v1/knowledge-bases/{baseID}.
func (h *Handlers) DeleteKnowledgeBase(w http.ResponseWriter, r *http.Request) {
	id, ok := h.pathID(w, r, "baseID")
	if !ok {
		return
	}
	if err := h.Knowledge.Delete(r.Context(), user(r), id); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// AddKnowledgeDocument handles POST /api/v1/knowledge-bases/{baseID}/documents:
// {file_id} or {title, text}. The document is indexed in the background
// (202, status INDEXING).
func (h *Handlers) AddKnowledgeDocument(w http.ResponseWriter, r *http.Request) {
	id, ok := h.pathID(w, r, "baseID")
	if !ok {
		return
	}
	var req addDocument
	if !h.decode(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.FileID) != "" {
		fileID, err := parseID("file_id", req.FileID)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		d, err := h.Knowledge.AddFile(r.Context(), user(r), id, fileID)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		httpx.WriteJSON(w, http.StatusAccepted, responses.NewKnowledgeDocument(d))
		return
	}
	d, err := h.Knowledge.AddText(r.Context(), user(r), id, req.Title, req.Text)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusAccepted, responses.NewKnowledgeDocument(d))
}

// DeleteKnowledgeDocument handles DELETE /api/v1/knowledge-bases/{baseID}/documents/{documentID}.
func (h *Handlers) DeleteKnowledgeDocument(w http.ResponseWriter, r *http.Request) {
	id, ok := h.pathID(w, r, "baseID")
	if !ok {
		return
	}
	doc, ok := h.pathID(w, r, "documentID")
	if !ok {
		return
	}
	if err := h.Knowledge.DeleteDocument(r.Context(), user(r), id, doc); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ReindexKnowledgeDocument handles POST .../documents/{documentID}/reindex.
func (h *Handlers) ReindexKnowledgeDocument(w http.ResponseWriter, r *http.Request) {
	id, ok := h.pathID(w, r, "baseID")
	if !ok {
		return
	}
	doc, ok := h.pathID(w, r, "documentID")
	if !ok {
		return
	}
	d, err := h.Knowledge.Reindex(r.Context(), user(r), id, doc)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusAccepted, responses.NewKnowledgeDocument(d))
}

// SearchKnowledgeBase handles POST /api/v1/knowledge-bases/{baseID}/search.
func (h *Handlers) SearchKnowledgeBase(w http.ResponseWriter, r *http.Request) {
	id, ok := h.pathID(w, r, "baseID")
	if !ok {
		return
	}
	var req searchKnowledge
	if !h.decode(w, r, &req) {
		return
	}
	if req.TopK == 0 {
		req.TopK = 5
	}
	results, err := h.Knowledge.Search(r.Context(), user(r), id, req.Query, req.TopK)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := responses.List[responses.KnowledgeResult]{Items: make([]responses.KnowledgeResult, 0, len(results))}
	for _, res := range results {
		out.Items = append(out.Items, responses.KnowledgeResult{DocumentID: res.DocumentID, DocumentTitle: res.DocumentTitle, Text: res.Content, Score: res.Score})
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}
