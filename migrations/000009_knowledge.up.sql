-- Phase D2: knowledge bases (retrieval for workflows).
--
-- A base belongs to a workspace and embeds with one of its OpenAI API key
-- credentials (that credential cannot be deleted while a base uses it).
-- Documents keep their source text (to index again); chunks hold passages
-- with their unit vectors as little-endian float32 bytes, compared in the
-- application (no vector extension needed).
CREATE TABLE knowledge_bases (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspaces(id),
    name VARCHAR(120) NOT NULL CHECK (name <> ''),
    description TEXT NOT NULL DEFAULT '',
    credential_id UUID NOT NULL,
    embedding_model VARCHAR(64) NOT NULL,
    dimensions INT NOT NULL CHECK (dimensions > 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT knowledge_bases_id_workspace_key UNIQUE (id, workspace_id),
    CONSTRAINT knowledge_bases_credential_fkey FOREIGN KEY (credential_id, workspace_id)
        REFERENCES credentials(id, workspace_id) ON DELETE RESTRICT
);
CREATE INDEX knowledge_bases_workspace_idx ON knowledge_bases(workspace_id, name);

CREATE TABLE knowledge_documents (
    id UUID PRIMARY KEY,
    knowledge_base_id UUID NOT NULL,
    workspace_id UUID NOT NULL,
    title VARCHAR(255) NOT NULL,
    source VARCHAR(16) NOT NULL CHECK (source IN ('file', 'text')),
    -- The file it came from; the document keeps its text if the file is deleted.
    file_id UUID,
    status VARCHAR(16) NOT NULL CHECK (status IN ('INDEXING', 'READY', 'FAILED')),
    error TEXT NOT NULL DEFAULT '',
    content TEXT NOT NULL,
    chunk_count INT NOT NULL DEFAULT 0,
    char_count INT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT knowledge_documents_base_fkey FOREIGN KEY (knowledge_base_id, workspace_id)
        REFERENCES knowledge_bases(id, workspace_id) ON DELETE CASCADE
);
CREATE INDEX knowledge_documents_base_idx ON knowledge_documents(knowledge_base_id, created_at DESC);

CREATE TABLE knowledge_chunks (
    id BIGSERIAL PRIMARY KEY,
    document_id UUID NOT NULL REFERENCES knowledge_documents(id) ON DELETE CASCADE,
    knowledge_base_id UUID NOT NULL,
    workspace_id UUID NOT NULL,
    ordinal INT NOT NULL,
    content TEXT NOT NULL,
    embedding BYTEA NOT NULL
);
CREATE INDEX knowledge_chunks_base_idx ON knowledge_chunks(knowledge_base_id);
CREATE INDEX knowledge_chunks_document_idx ON knowledge_chunks(document_id);
