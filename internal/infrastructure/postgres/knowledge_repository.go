package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"workflow-optimizer/internal/knowledge"
)

// KnowledgeRepository is the PostgreSQL knowledge.Repository (Phase D2).
type KnowledgeRepository struct {
	pool *pgxpool.Pool
}

var _ knowledge.Repository = (*KnowledgeRepository)(nil)

// NewKnowledgeRepository uses the Store's pool.
func NewKnowledgeRepository(store *Store) *KnowledgeRepository {
	return &KnowledgeRepository{pool: store.Pool}
}

func knowledgeError(err error, what string) error {
	var pgErr *pgconn.PgError
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return fmt.Errorf("%w: %s", knowledge.ErrNotFound, what)
	case errors.As(err, &pgErr) && (pgErr.Code == pgForeignKeyViolation || pgErr.Code == pgCheckViolation):
		return fmt.Errorf("%w: %s (%s)", knowledge.ErrInvalid, what, pgErr.ConstraintName)
	}
	return fmt.Errorf("%s: %w", what, err)
}

const baseColumns = `b.id, b.workspace_id, b.name, b.description, b.credential_id, b.embedding_model, b.dimensions, b.created_at, b.updated_at,
	(SELECT count(*) FROM knowledge_documents d WHERE d.knowledge_base_id = b.id),
	(SELECT count(*) FROM knowledge_chunks c WHERE c.knowledge_base_id = b.id)`

func scanBase(row pgx.Row) (knowledge.Base, error) {
	var b knowledge.Base
	err := row.Scan(&b.ID, &b.WorkspaceID, &b.Name, &b.Description, &b.CredentialID, &b.Model, &b.Dimensions, &b.CreatedAt, &b.UpdatedAt,
		&b.Documents, &b.Chunks)
	return b, err
}

// CreateBase stores a base.
func (r *KnowledgeRepository) CreateBase(ctx context.Context, b knowledge.Base) (knowledge.Base, error) {
	_, err := r.pool.Exec(ctx, `INSERT INTO knowledge_bases (id, workspace_id, name, description, credential_id, embedding_model, dimensions)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`, b.ID, b.WorkspaceID, b.Name, b.Description, b.CredentialID, b.Model, b.Dimensions)
	if err != nil {
		return knowledge.Base{}, knowledgeError(err, "create knowledge base")
	}
	return r.GetBase(ctx, b.WorkspaceID, b.ID)
}

// GetBase returns a base of the workspace.
func (r *KnowledgeRepository) GetBase(ctx context.Context, workspaceID, id uuid.UUID) (knowledge.Base, error) {
	b, err := scanBase(r.pool.QueryRow(ctx, `SELECT `+baseColumns+` FROM knowledge_bases b WHERE b.id = $1 AND b.workspace_id = $2`, id, workspaceID))
	if err != nil {
		return knowledge.Base{}, knowledgeError(err, "get knowledge base")
	}
	return b, nil
}

// LocateBase returns a base by ID alone.
func (r *KnowledgeRepository) LocateBase(ctx context.Context, id uuid.UUID) (knowledge.Base, error) {
	b, err := scanBase(r.pool.QueryRow(ctx, `SELECT `+baseColumns+` FROM knowledge_bases b WHERE b.id = $1`, id))
	if err != nil {
		return knowledge.Base{}, knowledgeError(err, "get knowledge base")
	}
	return b, nil
}

// ListBases returns the workspace's bases by name.
func (r *KnowledgeRepository) ListBases(ctx context.Context, workspaceID uuid.UUID) ([]knowledge.Base, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+baseColumns+` FROM knowledge_bases b WHERE b.workspace_id = $1 ORDER BY b.name, b.id`, workspaceID)
	if err != nil {
		return nil, knowledgeError(err, "list knowledge bases")
	}
	defer rows.Close()
	out := []knowledge.Base{}
	for rows.Next() {
		b, err := scanBase(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// DeleteBase removes a base with its documents and chunks.
func (r *KnowledgeRepository) DeleteBase(ctx context.Context, workspaceID, id uuid.UUID) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `DELETE FROM knowledge_chunks WHERE knowledge_base_id = $1 AND workspace_id = $2`, id, workspaceID); err != nil {
		return knowledgeError(err, "delete knowledge base")
	}
	tag, err := tx.Exec(ctx, `DELETE FROM knowledge_bases WHERE id = $1 AND workspace_id = $2`, id, workspaceID)
	if err != nil {
		return knowledgeError(err, "delete knowledge base")
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: knowledge base", knowledge.ErrNotFound)
	}
	return tx.Commit(ctx)
}

const documentColumns = `id, knowledge_base_id, workspace_id, title, source, file_id, status, error, chunk_count, char_count, created_at, updated_at`

// scanDocument reports an INDEXING document untouched for StaleAfter as
// FAILED: its indexing process stopped.
func scanDocument(row pgx.Row, extra ...any) (knowledge.Document, error) {
	var (
		d      knowledge.Document
		status string
	)
	err := row.Scan(append([]any{&d.ID, &d.KnowledgeBaseID, &d.WorkspaceID, &d.Title, &d.Source, &d.FileID, &status, &d.Error, &d.Chunks, &d.Chars,
		&d.CreatedAt, &d.UpdatedAt}, extra...)...)
	d.Status = knowledge.Status(status)
	if d.Status == knowledge.StatusIndexing && time.Since(d.UpdatedAt) > knowledge.StaleAfter {
		d.Status, d.Error = knowledge.StatusFailed, "indexing was interrupted; retry"
	}
	return d, err
}

// CreateDocument inserts a document (INDEXING); an existing ID is returned
// as is with created=false.
func (r *KnowledgeRepository) CreateDocument(ctx context.Context, d knowledge.Document, text string) (knowledge.Document, bool, error) {
	tag, err := r.pool.Exec(ctx, `INSERT INTO knowledge_documents (id, knowledge_base_id, workspace_id, title, source, file_id, status, content, char_count)
		VALUES ($1, $2, $3, $4, $5, $6, 'INDEXING', $7, $8) ON CONFLICT (id) DO NOTHING`,
		d.ID, d.KnowledgeBaseID, d.WorkspaceID, d.Title, d.Source, d.FileID, text, d.Chars)
	if err != nil {
		return knowledge.Document{}, false, knowledgeError(err, "create document")
	}
	got, _, err := r.GetDocument(ctx, d.WorkspaceID, d.ID)
	if err != nil {
		return knowledge.Document{}, false, err
	}
	if got.KnowledgeBaseID != d.KnowledgeBaseID {
		return knowledge.Document{}, false, fmt.Errorf("%w: document belongs to another knowledge base", knowledge.ErrInvalid)
	}
	return got, tag.RowsAffected() == 1, nil
}

// GetDocument returns a document of the workspace with its text.
func (r *KnowledgeRepository) GetDocument(ctx context.Context, workspaceID, id uuid.UUID) (knowledge.Document, string, error) {
	var text string
	d, err := scanDocument(r.pool.QueryRow(ctx, `SELECT `+documentColumns+`, content FROM knowledge_documents WHERE id = $1 AND workspace_id = $2`,
		id, workspaceID), &text)
	if err != nil {
		return knowledge.Document{}, "", knowledgeError(err, "get document")
	}
	return d, text, nil
}

// ListDocuments returns a base's documents, newest first.
func (r *KnowledgeRepository) ListDocuments(ctx context.Context, workspaceID, baseID uuid.UUID) ([]knowledge.Document, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+documentColumns+` FROM knowledge_documents WHERE knowledge_base_id = $1 AND workspace_id = $2
		ORDER BY created_at DESC, id`, baseID, workspaceID)
	if err != nil {
		return nil, knowledgeError(err, "list documents")
	}
	defer rows.Close()
	out := []knowledge.Document{}
	for rows.Next() {
		d, err := scanDocument(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// DeleteDocument removes a document and its chunks.
func (r *KnowledgeRepository) DeleteDocument(ctx context.Context, workspaceID, id uuid.UUID) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM knowledge_documents WHERE id = $1 AND workspace_id = $2`, id, workspaceID)
	if err != nil {
		return knowledgeError(err, "delete document")
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: document", knowledge.ErrNotFound)
	}
	return nil
}

// SetIndexing marks a document INDEXING again.
func (r *KnowledgeRepository) SetIndexing(ctx context.Context, workspaceID, id uuid.UUID) error {
	tag, err := r.pool.Exec(ctx, `UPDATE knowledge_documents SET status = 'INDEXING', error = '', updated_at = now()
		WHERE id = $1 AND workspace_id = $2`, id, workspaceID)
	if err != nil {
		return knowledgeError(err, "update document")
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: document", knowledge.ErrNotFound)
	}
	return nil
}

// FinishDocument replaces a document's chunks and marks it READY, in one
// transaction (a search never sees half a document).
func (r *KnowledgeRepository) FinishDocument(ctx context.Context, workspaceID, id uuid.UUID, chunks []knowledge.Chunk) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var baseID uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT knowledge_base_id FROM knowledge_documents WHERE id = $1 AND workspace_id = $2 FOR UPDATE`,
		id, workspaceID).Scan(&baseID); err != nil {
		return knowledgeError(err, "finish document")
	}
	if _, err := tx.Exec(ctx, `DELETE FROM knowledge_chunks WHERE document_id = $1`, id); err != nil {
		return knowledgeError(err, "finish document")
	}
	rows := make([][]any, len(chunks))
	for i, c := range chunks {
		rows[i] = []any{id, baseID, workspaceID, c.Ordinal, c.Content, knowledge.Encode(c.Vector)}
	}
	if _, err := tx.CopyFrom(ctx, pgx.Identifier{"knowledge_chunks"},
		[]string{"document_id", "knowledge_base_id", "workspace_id", "ordinal", "content", "embedding"}, pgx.CopyFromRows(rows)); err != nil {
		return knowledgeError(err, "store chunks")
	}
	if _, err := tx.Exec(ctx, `UPDATE knowledge_documents SET status = 'READY', error = '', chunk_count = $2, updated_at = now() WHERE id = $1`,
		id, len(chunks)); err != nil {
		return knowledgeError(err, "finish document")
	}
	return tx.Commit(ctx)
}

// FailDocument marks a document FAILED.
func (r *KnowledgeRepository) FailDocument(ctx context.Context, workspaceID, id uuid.UUID, message string) error {
	_, err := r.pool.Exec(ctx, `UPDATE knowledge_documents SET status = 'FAILED', error = $3, updated_at = now() WHERE id = $1 AND workspace_id = $2`,
		id, workspaceID, message)
	return err
}

// Vectors returns every chunk vector of a base.
func (r *KnowledgeRepository) Vectors(ctx context.Context, workspaceID, baseID uuid.UUID) ([]knowledge.ScoredID, error) {
	rows, err := r.pool.Query(ctx, `SELECT id, embedding FROM knowledge_chunks WHERE knowledge_base_id = $1 AND workspace_id = $2`, baseID, workspaceID)
	if err != nil {
		return nil, knowledgeError(err, "load vectors")
	}
	defer rows.Close()
	var out []knowledge.ScoredID
	for rows.Next() {
		var (
			id  int64
			raw []byte
		)
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		out = append(out, knowledge.ScoredID{ID: id, Vector: knowledge.Decode(raw)})
	}
	return out, rows.Err()
}

// Passages returns chunks by ID with their document titles.
func (r *KnowledgeRepository) Passages(ctx context.Context, workspaceID uuid.UUID, ids []int64) (map[int64]knowledge.Result, error) {
	out := map[int64]knowledge.Result{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := r.pool.Query(ctx, `SELECT c.id, c.document_id, d.title, c.ordinal, c.content FROM knowledge_chunks c
		JOIN knowledge_documents d ON d.id = c.document_id WHERE c.id = ANY($1) AND c.workspace_id = $2`, ids, workspaceID)
	if err != nil {
		return nil, knowledgeError(err, "load passages")
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id int64
			p  knowledge.Result
		)
		if err := rows.Scan(&id, &p.DocumentID, &p.DocumentTitle, &p.Ordinal, &p.Content); err != nil {
			return nil, err
		}
		out[id] = p
	}
	return out, rows.Err()
}
