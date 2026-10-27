DROP INDEX IF EXISTS knowledge_chunks_lexical;
DROP INDEX IF EXISTS knowledge_chunks_local_hnsw;
ALTER TABLE knowledge_chunks DROP COLUMN embedding_vector;
ALTER TABLE knowledge_documents DROP COLUMN index_generation, DROP COLUMN fetched_at, DROP COLUMN content_hash, DROP COLUMN source_url;
-- URL documents are retained as text so rollback does not discard sources.
UPDATE knowledge_documents SET source='text' WHERE source='url';
ALTER TABLE knowledge_documents DROP CONSTRAINT knowledge_documents_source_check;
ALTER TABLE knowledge_documents ADD CONSTRAINT knowledge_documents_source_check CHECK (source IN ('file','text'));
ALTER TABLE knowledge_bases DROP COLUMN model_revision;
-- Keep credential_id nullable: local bases must survive rollback.
DROP TABLE whatsapp_sessions, automation_triggers, automations, automation_actions, automation_events, automation_runs, assistant_sessions;
