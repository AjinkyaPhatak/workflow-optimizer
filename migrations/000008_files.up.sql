-- Phase D1: workspace files (documents and images uploaded for workflows).
--
-- The content is stored in the database (bytea) next to its metadata, so a
-- file and its bytes are written and deleted together; files are bounded
-- (20 MB, enforced by the application).
CREATE TABLE files (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspaces(id),
    name VARCHAR(255) NOT NULL CHECK (name <> ''),
    content_type VARCHAR(255) NOT NULL,
    size_bytes BIGINT NOT NULL CHECK (size_bytes > 0),
    sha256 CHAR(64) NOT NULL,
    data BYTEA NOT NULL,
    created_by UUID NOT NULL REFERENCES users(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT files_id_workspace_key UNIQUE (id, workspace_id)
);

CREATE INDEX files_workspace_idx ON files(workspace_id, created_at DESC);
