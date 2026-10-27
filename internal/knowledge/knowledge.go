// Package knowledge is workspace knowledge bases (Phase D2): documents
// split into chunks, embedded with an AI API key of the workspace
// (Gemini, free, or OpenAI), and searched by meaning (retrieval for LLM
// prompts).
//
//	add:    text (or a file's extracted text) -> chunks -> embeddings -> knowledge_chunks
//	search: query -> embedding -> cosine similarity over the base's chunks -> top passages
//
// Vectors are stored in PostgreSQL as float32 bytes and compared in Go
// (pgvector is not required); they are normalized when stored, so the
// similarity is a dot product.
package knowledge

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

// EmbeddingSpec is how a base of one provider embeds: the model and the
// stored vector length.
type EmbeddingSpec struct {
	Provider   string
	Model      string
	Dimensions int
}

// EmbeddingSpecs are the supported embedding setups, chosen by the
// provider of the base's credential (Phase G added Gemini, whose free tier
// covers embeddings). Both models may be shortened: Gemini's 3072-value
// vectors to 768, OpenAI's to 512 (a fraction of the storage, nearly the
// same quality). A base keeps the model it was created with.
var EmbeddingSpecs = []EmbeddingSpec{
	{Provider: "local", Model: "intfloat/multilingual-e5-small", Dimensions: 384},
	{Provider: "gemini", Model: "gemini-embedding-001", Dimensions: 768},
	{Provider: "openai", Model: "text-embedding-3-small", Dimensions: 512},
}

// SpecForModel is the spec of a base's model.
func SpecForModel(model string) (EmbeddingSpec, bool) {
	for _, s := range EmbeddingSpecs {
		if s.Model == model {
			return s, true
		}
	}
	return EmbeddingSpec{}, false
}

// Limits.
const (
	MaxNameLength      = 120
	MaxTitleLength     = 255
	MaxDocumentChars   = 2 << 20
	MaxChunksPerSearch = 20
)

// Status is a document's indexing state.
type Status string

const (
	StatusIndexing Status = "INDEXING"
	StatusReady    Status = "READY"
	StatusFailed   Status = "FAILED"
)

// StaleAfter: a document still INDEXING this long after its last update
// was interrupted (its process stopped); it is reported as FAILED.
const StaleAfter = 30 * time.Minute

var (
	ErrNotFound = errors.New("knowledge: not found")
	ErrInvalid  = errors.New("knowledge: invalid")
	// ErrCredentialInUse is never returned here; see the credential
	// repository's delete (a base's credential cannot be deleted).
)

// Base is a knowledge base.
type Base struct {
	ID            uuid.UUID
	WorkspaceID   uuid.UUID
	Name          string
	Description   string
	CredentialID  uuid.UUID
	Model         string
	Dimensions    int
	ModelRevision string
	CreatedAt     time.Time
	UpdatedAt     time.Time
	// Counts (filled by List and Get).
	Documents int
	Chunks    int
}

// Document is one indexed text in a base.
type Document struct {
	ID              uuid.UUID
	KnowledgeBaseID uuid.UUID
	WorkspaceID     uuid.UUID
	Title           string
	Source          string // "file" or "text"
	FileID          *uuid.UUID
	Status          Status
	Error           string
	Chunks          int
	Chars           int
	CreatedAt       time.Time
	UpdatedAt       time.Time
	SourceURL       string
	ContentHash     string
	FetchedAt       *time.Time
	Generation      int
}

// Chunk is a stored passage with its vector.
type Chunk struct {
	DocumentID uuid.UUID
	Ordinal    int
	Content    string
	Vector     []float32
}

// Result is one search hit.
type Result struct {
	DocumentID    uuid.UUID
	DocumentTitle string
	Ordinal       int
	Content       string
	Score         float64
}

// ScoredID is a chunk's identity and vector, for scoring.
type ScoredID struct {
	ID     int64
	Vector []float32
}

// Repository stores bases, documents and chunks. Every method is scoped to
// a workspace.
type Repository interface {
	CreateBase(ctx context.Context, b Base) (Base, error)
	GetBase(ctx context.Context, workspaceID, id uuid.UUID) (Base, error)
	// LocateBase finds a base by ID alone (to authorize by its workspace).
	LocateBase(ctx context.Context, id uuid.UUID) (Base, error)
	ListBases(ctx context.Context, workspaceID uuid.UUID) ([]Base, error)
	DeleteBase(ctx context.Context, workspaceID, id uuid.UUID) error

	// CreateDocument inserts a document in INDEXING state with its source
	// text; an existing ID is reported as created=false (idempotent adds).
	CreateDocument(ctx context.Context, d Document, text string) (Document, bool, error)
	GetDocument(ctx context.Context, workspaceID, id uuid.UUID) (Document, string, error)
	ListDocuments(ctx context.Context, workspaceID, baseID uuid.UUID) ([]Document, error)
	DeleteDocument(ctx context.Context, workspaceID, id uuid.UUID) error
	// SetIndexing marks a document INDEXING again (reindex).
	SetIndexing(ctx context.Context, workspaceID, id uuid.UUID) error
	// FinishDocument replaces the document's chunks and marks it READY.
	FinishDocument(ctx context.Context, workspaceID, id uuid.UUID, chunks []Chunk) error
	// FailDocument marks it FAILED with a message.
	FailDocument(ctx context.Context, workspaceID, id uuid.UUID, message string) error

	// Vectors returns every chunk vector of a base.
	Vectors(ctx context.Context, workspaceID, baseID uuid.UUID) ([]ScoredID, error)
	// Passages returns the chunks with these IDs (any order).
	Passages(ctx context.Context, workspaceID uuid.UUID, ids []int64) (map[int64]Result, error)
}

// VectorRepository provides tenant-scoped database retrieval for local embeddings.
type VectorRepository interface {
	SearchVectors(context.Context, uuid.UUID, uuid.UUID, []float32, string, int, float64) ([]Result, error)
}
type GenerationRepository interface {
	FinishGeneration(context.Context, uuid.UUID, uuid.UUID, int, []Chunk) error
}
