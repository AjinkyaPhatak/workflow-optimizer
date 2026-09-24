DROP TABLE IF EXISTS execution_status_history;
DROP FUNCTION IF EXISTS reject_execution_history_mutation();

DROP TRIGGER IF EXISTS node_executions_enforce_transition ON node_executions;
DROP FUNCTION IF EXISTS enforce_node_execution_transition();
DROP TRIGGER IF EXISTS executions_enforce_transition ON executions;
DROP FUNCTION IF EXISTS enforce_execution_transition();

ALTER TABLE node_executions DROP CONSTRAINT IF EXISTS node_executions_execution_node_attempt_key;
ALTER TABLE node_executions DROP CONSTRAINT IF EXISTS node_executions_output_error_check;
ALTER TABLE node_executions DROP CONSTRAINT IF EXISTS node_executions_lifecycle_timestamps_check;
ALTER TABLE node_executions DROP CONSTRAINT IF EXISTS node_executions_status_check;
ALTER TABLE node_executions ADD CONSTRAINT node_executions_status_check
    CHECK (status IN ('PENDING', 'RUNNING', 'COMPLETED', 'FAILED', 'CANCELLED'));
ALTER TABLE node_executions DROP COLUMN IF EXISTS updated_at;
ALTER TABLE node_executions RENAME COLUMN finished_at TO completed_at;

ALTER TABLE executions DROP CONSTRAINT IF EXISTS executions_output_error_check;
ALTER TABLE executions DROP CONSTRAINT IF EXISTS executions_lifecycle_timestamps_check;
ALTER TABLE executions DROP CONSTRAINT IF EXISTS executions_version_belongs_to_workflow_fkey;
ALTER TABLE workflow_versions DROP CONSTRAINT IF EXISTS workflow_versions_id_workflow_id_key;
ALTER TABLE executions DROP COLUMN IF EXISTS updated_at;
ALTER TABLE executions RENAME COLUMN finished_at TO completed_at;
