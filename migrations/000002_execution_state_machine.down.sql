-- Roll back Phase 8 to the 000001 schema shape.
--
-- Rollback semantics: the 000001 schema cannot represent SKIPPED nodes, claim
-- tokens, updated_at, legacy_values or status history. Instead of destroying
-- that data, it is moved into phase8_rollback_* archive tables that 000001
-- does not know about; a later 000002 UP restores it. SKIPPED nodes are mapped
-- to the closest 000001 status, CANCELLED (the original is archived).
-- Archive tables are intentionally not dropped here; drop them manually once
-- the rollback is permanent.

-- Remove lifecycle enforcement first so the mapping below is allowed.
DROP TRIGGER IF EXISTS workflow_versions_lifecycle_guard ON workflow_versions;
DROP TRIGGER IF EXISTS node_executions_lifecycle_before_update ON node_executions;
DROP TRIGGER IF EXISTS node_executions_lifecycle_before_insert ON node_executions;
DROP TRIGGER IF EXISTS executions_lifecycle_history ON executions;
DROP TRIGGER IF EXISTS executions_lifecycle_before_update ON executions;
DROP TRIGGER IF EXISTS executions_lifecycle_before_insert ON executions;
DROP TRIGGER IF EXISTS execution_status_history_guard ON execution_status_history;
DROP TRIGGER IF EXISTS execution_status_history_no_truncate ON execution_status_history;

-- Archive Phase 8-only data (merged with any archive left by an earlier DOWN).
CREATE TABLE IF NOT EXISTS phase8_rollback_executions (
    id UUID PRIMARY KEY, claim_token UUID, updated_at TIMESTAMPTZ, legacy_values JSONB);
INSERT INTO phase8_rollback_executions (id, claim_token, updated_at, legacy_values)
SELECT id, claim_token, updated_at, legacy_values FROM executions
ON CONFLICT (id) DO UPDATE SET claim_token = EXCLUDED.claim_token,
    updated_at = EXCLUDED.updated_at, legacy_values = EXCLUDED.legacy_values;

CREATE TABLE IF NOT EXISTS phase8_rollback_node_executions (
    id UUID PRIMARY KEY, status VARCHAR(32), updated_at TIMESTAMPTZ, legacy_values JSONB);
INSERT INTO phase8_rollback_node_executions (id, status, updated_at, legacy_values)
SELECT id, status, updated_at, legacy_values FROM node_executions
ON CONFLICT (id) DO UPDATE SET status = EXCLUDED.status,
    updated_at = EXCLUDED.updated_at, legacy_values = EXCLUDED.legacy_values;

CREATE TABLE IF NOT EXISTS phase8_rollback_status_history (
    id UUID PRIMARY KEY, execution_id UUID NOT NULL, from_status VARCHAR(32),
    to_status VARCHAR(32) NOT NULL, created_at TIMESTAMPTZ NOT NULL, metadata JSONB NOT NULL);
INSERT INTO phase8_rollback_status_history (id, execution_id, from_status, to_status, created_at, metadata)
SELECT id, execution_id, from_status, to_status, created_at, metadata FROM execution_status_history
ON CONFLICT (id) DO NOTHING;
-- The archive has no foreign key, so it never blocks 000001 DOWN.
DROP TABLE execution_status_history;

DROP FUNCTION IF EXISTS lifecycle_workflow_version_guard();
DROP FUNCTION IF EXISTS lifecycle_node_before_update();
DROP FUNCTION IF EXISTS lifecycle_node_before_insert();
DROP FUNCTION IF EXISTS lifecycle_require_running_parent(UUID);
DROP FUNCTION IF EXISTS lifecycle_history_guard();
DROP FUNCTION IF EXISTS lifecycle_execution_history();
DROP FUNCTION IF EXISTS lifecycle_execution_before_update();
DROP FUNCTION IF EXISTS lifecycle_execution_before_insert();

ALTER TABLE node_executions DROP CONSTRAINT IF EXISTS node_executions_execution_node_attempt_key;
ALTER TABLE node_executions DROP CONSTRAINT IF EXISTS node_executions_output_error_check;
ALTER TABLE node_executions DROP CONSTRAINT IF EXISTS node_executions_lifecycle_timestamps_check;
ALTER TABLE node_executions DROP CONSTRAINT IF EXISTS node_executions_status_check;
UPDATE node_executions SET status = 'CANCELLED' WHERE status = 'SKIPPED';
ALTER TABLE node_executions ADD CONSTRAINT node_executions_status_check
    CHECK (status IN ('PENDING', 'RUNNING', 'COMPLETED', 'FAILED', 'CANCELLED'));
ALTER TABLE node_executions DROP COLUMN IF EXISTS legacy_values;
ALTER TABLE node_executions DROP COLUMN IF EXISTS updated_at;
ALTER TABLE node_executions RENAME COLUMN finished_at TO completed_at;

ALTER TABLE executions DROP CONSTRAINT IF EXISTS executions_claim_token_check;
ALTER TABLE executions DROP CONSTRAINT IF EXISTS executions_output_error_check;
ALTER TABLE executions DROP CONSTRAINT IF EXISTS executions_lifecycle_timestamps_check;
ALTER TABLE executions DROP CONSTRAINT IF EXISTS executions_version_belongs_to_workflow_fkey;
ALTER TABLE workflow_versions DROP CONSTRAINT IF EXISTS workflow_versions_id_workflow_id_key;
ALTER TABLE executions DROP COLUMN IF EXISTS legacy_values;
ALTER TABLE executions DROP COLUMN IF EXISTS claim_token;
ALTER TABLE executions DROP COLUMN IF EXISTS updated_at;
ALTER TABLE executions RENAME COLUMN finished_at TO completed_at;
