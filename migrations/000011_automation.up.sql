CREATE EXTENSION IF NOT EXISTS vector WITH SCHEMA public;

CREATE TABLE assistant_sessions (
 id UUID PRIMARY KEY,
 workspace_id UUID NOT NULL REFERENCES workspaces(id),
 user_id UUID NOT NULL REFERENCES users(id),
 revision INT NOT NULL DEFAULT 1,
 body JSONB NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX assistant_sessions_workspace ON assistant_sessions(workspace_id, updated_at DESC);

-- Separate rows ensure legacy claims/reapers can never acquire a Temporal run.
CREATE TABLE automation_runs (
 id UUID PRIMARY KEY,
 workspace_id UUID NOT NULL REFERENCES workspaces(id),
 user_id UUID NOT NULL REFERENCES users(id),
 execution_backend TEXT NOT NULL DEFAULT 'temporal' CHECK (execution_backend = 'temporal'),
 body JSONB NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX automation_runs_workspace ON automation_runs(workspace_id, created_at DESC);
CREATE TABLE automation_events (
 id BIGSERIAL PRIMARY KEY,
 run_id UUID NOT NULL REFERENCES automation_runs(id) ON DELETE CASCADE,
 kind TEXT NOT NULL, step_id TEXT NOT NULL DEFAULT '', message TEXT NOT NULL DEFAULT '',
 created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX automation_events_run ON automation_events(run_id,id);
CREATE TABLE automation_actions (
 run_id UUID NOT NULL REFERENCES automation_runs(id) ON DELETE CASCADE,
 operation_id TEXT NOT NULL,
 tool TEXT NOT NULL,
 status TEXT NOT NULL CHECK (status IN ('STARTED','COMPLETED','UNCERTAIN')),
 result JSONB,
 provider_receipt TEXT NOT NULL DEFAULT '',
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 PRIMARY KEY(run_id,operation_id)
);
CREATE TABLE automations (
 id UUID PRIMARY KEY,
 workspace_id UUID NOT NULL REFERENCES workspaces(id),
 user_id UUID NOT NULL REFERENCES users(id),
 body JSONB NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX automations_workspace ON automations(workspace_id);
CREATE TABLE automation_triggers (
 automation_id UUID NOT NULL REFERENCES automations(id) ON DELETE CASCADE,
 event_id TEXT NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 PRIMARY KEY(automation_id,event_id)
);
CREATE TABLE whatsapp_sessions (
 workspace_id UUID NOT NULL REFERENCES workspaces(id),
 user_id UUID NOT NULL REFERENCES users(id),
 encrypted_data BYTEA NOT NULL,
 updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 PRIMARY KEY(workspace_id,user_id)
);

ALTER TABLE knowledge_bases ALTER COLUMN credential_id DROP NOT NULL;
ALTER TABLE knowledge_bases ADD COLUMN model_revision TEXT NOT NULL DEFAULT '';
ALTER TABLE knowledge_documents DROP CONSTRAINT knowledge_documents_source_check;
ALTER TABLE knowledge_documents ADD CONSTRAINT knowledge_documents_source_check CHECK (source IN ('file','text','url'));
ALTER TABLE knowledge_documents ADD COLUMN source_url TEXT NOT NULL DEFAULT '';
ALTER TABLE knowledge_documents ADD COLUMN content_hash TEXT NOT NULL DEFAULT '';
ALTER TABLE knowledge_documents ADD COLUMN fetched_at TIMESTAMPTZ;
ALTER TABLE knowledge_documents ADD COLUMN index_generation INT NOT NULL DEFAULT 1;
ALTER TABLE knowledge_chunks ADD COLUMN embedding_vector public.vector;
CREATE INDEX knowledge_chunks_local_hnsw ON knowledge_chunks USING hnsw
 ((embedding_vector::public.vector(384)) public.vector_cosine_ops) WHERE public.vector_dims(embedding_vector)=384;
CREATE INDEX knowledge_chunks_lexical ON knowledge_chunks USING gin(to_tsvector('simple',content));
