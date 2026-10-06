DROP INDEX IF EXISTS workflows_project_live_idx;
ALTER TABLE workflows DROP COLUMN IF EXISTS deleted_at;
