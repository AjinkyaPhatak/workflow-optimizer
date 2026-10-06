-- Phase 12: deleting a workflow through the API hides it instead of removing
-- the row, because its versions and executions (history) reference it and
-- published versions can never be deleted (migration 000002).
ALTER TABLE workflows ADD COLUMN deleted_at TIMESTAMPTZ;

CREATE INDEX workflows_project_live_idx ON workflows(project_id, created_at) WHERE deleted_at IS NULL;
