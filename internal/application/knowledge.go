package application

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"workflow-optimizer/internal/knowledge"
	"workflow-optimizer/internal/provider/llm"
	"workflow-optimizer/internal/webingest"
	"workflow-optimizer/internal/workspace"
)

// indexTimeout bounds one background indexing.
const indexTimeout = 15 * time.Minute

// KnowledgeService authorizes knowledge base management (Phase D2). Members
// who may edit workflows create bases and add documents; any member lists
// and searches. Documents added here are indexed in the background: the
// request returns at once with the document INDEXING.
type KnowledgeService struct {
	indexer interface {
		SubmitIndex(context.Context, uuid.UUID, uuid.UUID) error
	}
	access *Access
	svc    *knowledge.Service
	logger *slog.Logger
	wg     sync.WaitGroup
	// ctx is cancelled by Close: background indexing stops.
	ctx    context.Context
	cancel context.CancelFunc
}

func (s *KnowledgeService) WithIndexer(indexer interface {
	SubmitIndex(context.Context, uuid.UUID, uuid.UUID) error
}) *KnowledgeService {
	s.indexer = indexer
	return s
}

// NewKnowledgeService wires the service. svc may be nil (not configured).
func NewKnowledgeService(access *Access, svc *knowledge.Service, logger *slog.Logger) *KnowledgeService {
	if logger == nil {
		logger = slog.Default()
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &KnowledgeService{access: access, svc: svc, logger: logger, ctx: ctx, cancel: cancel}
}

// Wait blocks until background indexing finishes (tests).
func (s *KnowledgeService) Wait() { s.wg.Wait() }

// Close stops background indexing and waits for it (shutdown).
func (s *KnowledgeService) Close() {
	s.cancel()
	s.wg.Wait()
}

func knowledgeError(err error, resource string) error {
	var pe *llm.Error
	switch {
	case errors.Is(err, knowledge.ErrNotFound):
		return notFound(resource)
	case errors.Is(err, knowledge.ErrInvalid):
		msg := strings.TrimPrefix(err.Error(), knowledge.ErrInvalid.Error()+": ")
		if i := strings.Index(msg, " ("); i > 0 {
			msg = msg[:i]
		}
		return &InvalidError{Message: msg}
	case errors.As(err, &pe):
		return &InvalidError{Message: knowledge.FailureMessage(err)}
	}
	return err
}

func (s *KnowledgeService) ready() error {
	if s.svc == nil {
		return ErrUnavailable
	}
	return nil
}

// base finds a base and checks the caller may act on it; another
// workspace's base is not found.
func (s *KnowledgeService) base(ctx context.Context, user, id uuid.UUID, action workspace.Action) (knowledge.Base, error) {
	if err := s.ready(); err != nil {
		return knowledge.Base{}, err
	}
	b, err := s.svc.Repository().LocateBase(ctx, id)
	if err != nil {
		return knowledge.Base{}, knowledgeError(err, "knowledge base")
	}
	if _, err := s.access.Require(ctx, user, b.WorkspaceID, action, "knowledge base"); err != nil {
		return knowledge.Base{}, err
	}
	return b, nil
}

// Create stores a new base.
func (s *KnowledgeService) Create(ctx context.Context, user, workspaceID uuid.UUID, name, description string, credentialID uuid.UUID) (knowledge.Base, error) {
	if err := s.ready(); err != nil {
		return knowledge.Base{}, err
	}
	if _, err := s.access.Require(ctx, user, workspaceID, workspace.ActionWrite, "workspace"); err != nil {
		return knowledge.Base{}, err
	}
	b, err := s.svc.CreateBase(ctx, workspaceID, name, description, credentialID)
	return b, knowledgeError(err, "knowledge base")
}

// List returns the workspace's bases.
func (s *KnowledgeService) List(ctx context.Context, user, workspaceID uuid.UUID) ([]knowledge.Base, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	if _, err := s.access.Require(ctx, user, workspaceID, workspace.ActionRead, "workspace"); err != nil {
		return nil, err
	}
	out, err := s.svc.Repository().ListBases(ctx, workspaceID)
	return out, knowledgeError(err, "knowledge base")
}

// Get returns a base with its documents.
func (s *KnowledgeService) Get(ctx context.Context, user, id uuid.UUID) (knowledge.Base, []knowledge.Document, error) {
	b, err := s.base(ctx, user, id, workspace.ActionRead)
	if err != nil {
		return knowledge.Base{}, nil, err
	}
	docs, err := s.svc.Repository().ListDocuments(ctx, b.WorkspaceID, b.ID)
	return b, docs, knowledgeError(err, "knowledge base")
}

// Delete removes a base with everything in it.
func (s *KnowledgeService) Delete(ctx context.Context, user, id uuid.UUID) error {
	b, err := s.base(ctx, user, id, workspace.ActionWrite)
	if err != nil {
		return err
	}
	return knowledgeError(s.svc.Repository().DeleteBase(ctx, b.WorkspaceID, b.ID), "knowledge base")
}

// index runs Index in the background, detached from the request.
func (s *KnowledgeService) index(b knowledge.Base, d knowledge.Document, text string) error {
	if s.indexer != nil {
		ctx, cancel := context.WithTimeout(s.ctx, 15*time.Second)
		defer cancel()
		if err := s.indexer.SubmitIndex(ctx, b.WorkspaceID, d.ID); err != nil {
			_ = s.svc.Repository().FailDocument(ctx, b.WorkspaceID, d.ID, "The indexing service is unavailable. Retry when it is connected.")
			return &InvalidError{Message: "The source was saved, but indexing could not start. Retry when the execution service is connected."}
		}
		return nil
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ctx, cancel := context.WithTimeout(s.ctx, indexTimeout)
		defer cancel()
		if _, err := s.svc.Index(ctx, b, d, text); err != nil {
			s.logger.Warn("knowledge document indexing failed", "event", "knowledge_index_failed",
				"knowledge_base_id", b.ID.String(), "document_id", d.ID.String(), "reason", knowledge.FailureMessage(err))
		}
	}()
	return nil
}

// AddText adds a text document.
func (s *KnowledgeService) AddText(ctx context.Context, user, baseID uuid.UUID, title, text string) (knowledge.Document, error) {
	b, err := s.base(ctx, user, baseID, workspace.ActionWrite)
	if err != nil {
		return knowledge.Document{}, err
	}
	_, d, _, err := s.svc.Begin(ctx, knowledge.AddInput{WorkspaceID: b.WorkspaceID, BaseID: b.ID, Title: title, Text: text, Source: "text"})
	if err != nil {
		return knowledge.Document{}, knowledgeError(err, "knowledge base")
	}
	return d, s.index(b, d, strings.TrimSpace(text))
}

// AddFile adds a workspace file's text.
func (s *KnowledgeService) AddFile(ctx context.Context, user, baseID, fileID uuid.UUID) (knowledge.Document, error) {
	b, err := s.base(ctx, user, baseID, workspace.ActionWrite)
	if err != nil {
		return knowledge.Document{}, err
	}
	f, text, err := s.svc.FileText(ctx, b.WorkspaceID, fileID)
	if err != nil {
		return knowledge.Document{}, knowledgeError(err, "file")
	}
	_, d, _, err := s.svc.Begin(ctx, knowledge.AddInput{WorkspaceID: b.WorkspaceID, BaseID: b.ID, Title: f.Name, Text: text, Source: "file", FileID: &f.ID})
	if err != nil {
		return knowledge.Document{}, knowledgeError(err, "knowledge base")
	}
	return d, s.index(b, d, strings.TrimSpace(text))
}

func (s *KnowledgeService) document(ctx context.Context, user, baseID, docID uuid.UUID) (knowledge.Base, knowledge.Document, error) {
	b, err := s.base(ctx, user, baseID, workspace.ActionWrite)
	if err != nil {
		return knowledge.Base{}, knowledge.Document{}, err
	}
	d, _, err := s.svc.Repository().GetDocument(ctx, b.WorkspaceID, docID)
	if err != nil || d.KnowledgeBaseID != b.ID {
		return knowledge.Base{}, knowledge.Document{}, notFound("document")
	}
	return b, d, nil
}

// DeleteDocument removes a document.
func (s *KnowledgeService) DeleteDocument(ctx context.Context, user, baseID, docID uuid.UUID) error {
	b, d, err := s.document(ctx, user, baseID, docID)
	if err != nil {
		return err
	}
	return knowledgeError(s.svc.Repository().DeleteDocument(ctx, b.WorkspaceID, d.ID), "document")
}

// Reindex indexes a document again (after a failure).
func (s *KnowledgeService) Reindex(ctx context.Context, user, baseID, docID uuid.UUID) (knowledge.Document, error) {
	b, d, err := s.document(ctx, user, baseID, docID)
	if err != nil {
		return knowledge.Document{}, err
	}
	b, d, text, err := s.svc.Reindex(ctx, b.WorkspaceID, d.ID)
	if err != nil {
		return knowledge.Document{}, knowledgeError(err, "document")
	}
	return d, s.index(b, d, text)
}
func (s *KnowledgeService) AddURL(ctx context.Context, user, baseID uuid.UUID, url string) (knowledge.Document, error) {
	b, err := s.base(ctx, user, baseID, workspace.ActionWrite)
	if err != nil {
		return knowledge.Document{}, err
	}
	page, err := webingest.Fetch(ctx, url)
	if err != nil {
		return knowledge.Document{}, &InvalidError{Message: err.Error()}
	}
	id := knowledge.DocumentID(baseID, "url:"+page.URL+":"+page.Text)
	_, doc, created, err := s.svc.Begin(ctx, knowledge.AddInput{WorkspaceID: b.WorkspaceID, BaseID: b.ID, ID: id, Title: page.URL, Text: page.Text, Source: "url", SourceURL: page.URL})
	if err != nil {
		return doc, knowledgeError(err, "knowledge base")
	}
	if !created && doc.Status == knowledge.StatusReady {
		return doc, nil
	}
	return doc, s.index(b, doc, page.Text)
}

// Search tries a query against a base (the Knowledge page's test box).
func (s *KnowledgeService) Search(ctx context.Context, user, baseID uuid.UUID, query string, k int) ([]knowledge.Result, error) {
	b, err := s.base(ctx, user, baseID, workspace.ActionRead)
	if err != nil {
		return nil, err
	}
	out, err := s.svc.Search(ctx, b.WorkspaceID, b.ID, query, k, 0)
	return out, knowledgeError(err, "knowledge base")
}
