package knowledge

import (
	"context"
	"crypto/sha1"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"workflow-optimizer/internal/credential"
	"workflow-optimizer/internal/files"
	"workflow-optimizer/internal/provider/llm"
)

// embedBatch is how many chunks one embeddings request carries.
const embedBatch = 96

// Service indexes and searches knowledge bases.
type Service struct {
	repo     Repository
	files    files.Reader
	creds    credential.Resolver
	embedder llm.Embedder
	now      func() time.Time
}

// NewService wires the service. files may be nil (no file documents).
func NewService(repo Repository, filesReader files.Reader, creds credential.Resolver, embedder llm.Embedder) *Service {
	return &Service{repo: repo, files: filesReader, creds: creds, embedder: embedder, now: time.Now}
}

// Repository is the underlying store.
func (s *Service) Repository() Repository { return s.repo }

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

// CreateBase validates and stores a base. The credential must be an OpenAI
// API key of the workspace (it is resolved once to check, then dropped).
func (s *Service) CreateBase(ctx context.Context, workspaceID uuid.UUID, name, description string, credentialID uuid.UUID) (Base, error) {
	name, description = strings.TrimSpace(name), strings.TrimSpace(description)
	if name == "" || utf8.RuneCountInString(name) > MaxNameLength {
		return Base{}, invalid("the name is required (at most %d characters)", MaxNameLength)
	}
	if utf8.RuneCountInString(description) > 1000 {
		return Base{}, invalid("the description is too long")
	}
	if _, err := s.credential(ctx, workspaceID, credentialID); err != nil {
		return Base{}, err
	}
	return s.repo.CreateBase(ctx, Base{ID: uuid.New(), WorkspaceID: workspaceID, Name: name, Description: description,
		CredentialID: credentialID, Model: DefaultModel, Dimensions: DefaultDimensions})
}

func (s *Service) credential(ctx context.Context, workspaceID, id uuid.UUID) (credential.ResolvedCredential, error) {
	if s.creds == nil {
		return credential.ResolvedCredential{}, invalid("credentials are not available")
	}
	c, err := s.creds.Resolve(ctx, workspaceID, id, EmbeddingProvider)
	if err != nil {
		if credential.IsCredentialError(err) {
			return credential.ResolvedCredential{}, fmt.Errorf("%w: choose an OpenAI API key credential of this workspace (%w)", ErrInvalid, err)
		}
		return credential.ResolvedCredential{}, err
	}
	if c.Type != credential.TypeAPIKey {
		return credential.ResolvedCredential{}, invalid("the credential must be an OpenAI API key")
	}
	return c, nil
}

// embed turns texts into unit vectors with the base's credential.
func (s *Service) embed(ctx context.Context, b Base, texts []string) ([][]float32, error) {
	if s.embedder == nil {
		return nil, invalid("embeddings are not available")
	}
	cred, err := s.credential(ctx, b.WorkspaceID, b.CredentialID)
	if err != nil {
		return nil, err
	}
	out := make([][]float32, 0, len(texts))
	for start := 0; start < len(texts); start += embedBatch {
		end := min(start+embedBatch, len(texts))
		resp, err := s.embedder.Embed(ctx, llm.EmbedRequest{Model: b.Model, Dimensions: b.Dimensions, Inputs: texts[start:end], Credential: cred})
		if err != nil {
			return nil, err
		}
		for _, v := range resp.Vectors {
			out = append(out, Normalize(v))
		}
	}
	return out, nil
}

// DocumentID is the deterministic ID of a document added under an
// idempotency key, so a repeated add (a retried node) finds the first one.
func DocumentID(baseID uuid.UUID, key string) uuid.UUID {
	return uuid.NewHash(sha1.New(), baseID, []byte("knowledge.add:"+key), 5)
}

// AddInput is a document to add.
type AddInput struct {
	WorkspaceID uuid.UUID
	BaseID      uuid.UUID
	// ID is optional (DocumentID for idempotent adds).
	ID     uuid.UUID
	Title  string
	Text   string
	Source string
	FileID *uuid.UUID
}

// Begin validates and stores a document (INDEXING) without indexing it.
// created is false when a document with in.ID already exists.
func (s *Service) Begin(ctx context.Context, in AddInput) (Base, Document, bool, error) {
	b, err := s.repo.GetBase(ctx, in.WorkspaceID, in.BaseID)
	if err != nil {
		return Base{}, Document{}, false, err
	}
	text := strings.TrimSpace(in.Text)
	if text == "" {
		return Base{}, Document{}, false, invalid("there is no text to add")
	}
	if len(text) > MaxDocumentChars {
		return Base{}, Document{}, false, invalid("the text is longer than 2 MB")
	}
	title := strings.TrimSpace(in.Title)
	if title == "" {
		title = firstLine(text)
	}
	for utf8.RuneCountInString(title) > MaxTitleLength {
		_, size := utf8.DecodeLastRuneInString(title)
		title = title[:len(title)-size]
	}
	if in.ID == uuid.Nil {
		in.ID = uuid.New()
	}
	if in.Source == "" {
		in.Source = "text"
	}
	d, created, err := s.repo.CreateDocument(ctx, Document{ID: in.ID, KnowledgeBaseID: b.ID, WorkspaceID: b.WorkspaceID, Title: title,
		Source: in.Source, FileID: in.FileID, Status: StatusIndexing, Chars: utf8.RuneCountInString(text)}, text)
	return b, d, created, err
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	line = strings.TrimSpace(strings.TrimLeft(line, "#*- "))
	if r := []rune(line); len(r) > 80 {
		line = string(r[:80]) + "…"
	}
	if line == "" {
		line = "Untitled"
	}
	return line
}

// Index chunks, embeds and stores a document's text; a failure marks the
// document FAILED (with a message without provider details) and is
// returned.
func (s *Service) Index(ctx context.Context, b Base, d Document, text string) (Document, error) {
	chunks := Split(text)
	vectors, err := s.embed(ctx, b, chunks)
	if err == nil {
		stored := make([]Chunk, len(chunks))
		for i := range chunks {
			stored[i] = Chunk{DocumentID: d.ID, Ordinal: i, Content: chunks[i], Vector: vectors[i]}
		}
		err = s.repo.FinishDocument(ctx, b.WorkspaceID, d.ID, stored)
	}
	if err != nil {
		// Recorded even when ctx is done (the run was cancelled).
		fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		_ = s.repo.FailDocument(fctx, b.WorkspaceID, d.ID, FailureMessage(err))
		return d, err
	}
	d.Status, d.Chunks, d.Error = StatusReady, len(chunks), ""
	return d, nil
}

// FailureMessage is a short, safe reason for a failed document.
func FailureMessage(err error) string {
	var pe *llm.Error
	switch {
	case errors.As(err, &pe):
		switch pe.Code {
		case llm.CodeAuthenticationFailed:
			return "OpenAI rejected the API key"
		case llm.CodeRateLimited:
			return "OpenAI rate limit or quota reached; retry later"
		case llm.CodeModelNotSupported:
			return "the embedding model is not available for this API key"
		}
		return "OpenAI could not create the embeddings; retry later"
	case errors.Is(err, ErrInvalid):
		return strings.TrimPrefix(err.Error(), ErrInvalid.Error()+": ")
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "indexing was interrupted; retry"
	}
	return "indexing failed; retry"
}

// FileText extracts a workspace file's text for a document.
func (s *Service) FileText(ctx context.Context, workspaceID, fileID uuid.UUID) (files.File, string, error) {
	if s.files == nil {
		return files.File{}, "", invalid("file storage is not available")
	}
	f, data, err := s.files.Content(ctx, workspaceID, fileID)
	if errors.Is(err, files.ErrNotFound) {
		return files.File{}, "", invalid("the file was not found in this workspace")
	}
	if err != nil {
		return files.File{}, "", err
	}
	x, err := files.ExtractText(f, data)
	if err != nil {
		return files.File{}, "", invalid("text cannot be extracted from %s", f.Name)
	}
	return f, x.Text, nil
}

// Reindex reads a document's stored text and indexes it again.
func (s *Service) Reindex(ctx context.Context, workspaceID, docID uuid.UUID) (Base, Document, string, error) {
	d, text, err := s.repo.GetDocument(ctx, workspaceID, docID)
	if err != nil {
		return Base{}, Document{}, "", err
	}
	b, err := s.repo.GetBase(ctx, workspaceID, d.KnowledgeBaseID)
	if err != nil {
		return Base{}, Document{}, "", err
	}
	if err := s.repo.SetIndexing(ctx, workspaceID, docID); err != nil {
		return Base{}, Document{}, "", err
	}
	d.Status, d.Error = StatusIndexing, ""
	return b, d, text, nil
}

// Search returns the passages of a base closest in meaning to query.
func (s *Service) Search(ctx context.Context, workspaceID, baseID uuid.UUID, query string, k int, minScore float64) ([]Result, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, invalid("the query is empty")
	}
	if k < 1 || k > MaxChunksPerSearch {
		return nil, invalid("top_k must be from 1 to %d", MaxChunksPerSearch)
	}
	b, err := s.repo.GetBase(ctx, workspaceID, baseID)
	if err != nil {
		return nil, err
	}
	vectors, err := s.repo.Vectors(ctx, workspaceID, baseID)
	if err != nil {
		return nil, err
	}
	if len(vectors) == 0 {
		return []Result{}, nil
	}
	q, err := s.embed(ctx, b, []string{query})
	if err != nil {
		return nil, err
	}
	best := Top(q[0], vectors, k, minScore)
	ids := make([]int64, len(best))
	for i, h := range best {
		ids[i] = h.id
	}
	passages, err := s.repo.Passages(ctx, workspaceID, ids)
	if err != nil {
		return nil, err
	}
	out := make([]Result, 0, len(best))
	for _, h := range best {
		if p, ok := passages[h.id]; ok {
			p.Score = h.score
			out = append(out, p)
		}
	}
	return out, nil
}

// Context renders results as numbered passages for an LLM prompt.
func Context(results []Result) string {
	var b strings.Builder
	for i, r := range results {
		fmt.Fprintf(&b, "[%d] %s\n%s\n\n", i+1, r.DocumentTitle, r.Content)
	}
	return strings.TrimSpace(b.String())
}
