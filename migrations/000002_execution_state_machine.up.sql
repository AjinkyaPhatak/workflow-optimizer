-- Phase 8: durable execution state machine.
--
-- Migration 000001 created placeholder executions/node_executions tables. This
-- migration evolves them in place (no parallel tables) to the Phase 8 lifecycle
-- contract and adds append-only status history. PostgreSQL is the concurrency
-- and consistency boundary: every invariant below holds for any writer, not
-- only for the Go repositories.

------------------------------------------------------------------------------
-- executions
------------------------------------------------------------------------------
ALTER TABLE executions RENAME COLUMN completed_at TO finished_at;
ALTER TABLE executions ADD COLUMN updated_at TIMESTAMPTZ NOT NULL DEFAULT now();

-- An execution may only reference a version of the same workflow. The
-- (id, workflow_id) key is redundant with the primary key but is the target
-- the composite foreign key needs.
ALTER TABLE workflow_versions
    ADD CONSTRAINT workflow_versions_id_workflow_id_key UNIQUE (id, workflow_id);
ALTER TABLE executions
    ADD CONSTRAINT executions_version_belongs_to_workflow_fkey
    FOREIGN KEY (workflow_version_id, workflow_id)
    REFERENCES workflow_versions (id, workflow_id);

-- Timestamp semantics per status.
ALTER TABLE executions ADD CONSTRAINT executions_lifecycle_timestamps_check CHECK (
    (status = 'PENDING' AND started_at IS NULL AND finished_at IS NULL)
 OR (status = 'RUNNING' AND started_at IS NOT NULL AND finished_at IS NULL)
 OR (status IN ('COMPLETED', 'FAILED', 'CANCELLED')
        AND started_at IS NOT NULL AND finished_at IS NOT NULL AND finished_at >= started_at)
);
-- Output belongs only to COMPLETED; a structured error is required for FAILED.
ALTER TABLE executions ADD CONSTRAINT executions_output_error_check CHECK (
    (output IS NULL OR status = 'COMPLETED')
 AND (status <> 'FAILED' OR error IS NOT NULL)
);

------------------------------------------------------------------------------
-- node_executions
------------------------------------------------------------------------------
ALTER TABLE node_executions RENAME COLUMN completed_at TO finished_at;
ALTER TABLE node_executions ADD COLUMN updated_at TIMESTAMPTZ NOT NULL DEFAULT now();

-- Phase 8 node states: CANCELLED (placeholder) is replaced by SKIPPED.
ALTER TABLE node_executions DROP CONSTRAINT node_executions_status_check;
ALTER TABLE node_executions ADD CONSTRAINT node_executions_status_check
    CHECK (status IN ('PENDING', 'RUNNING', 'COMPLETED', 'FAILED', 'SKIPPED'));

ALTER TABLE node_executions ADD CONSTRAINT node_executions_lifecycle_timestamps_check CHECK (
    (status = 'PENDING' AND started_at IS NULL AND finished_at IS NULL)
 OR (status = 'RUNNING' AND started_at IS NOT NULL AND finished_at IS NULL)
 OR (status IN ('COMPLETED', 'FAILED', 'SKIPPED')
        AND started_at IS NOT NULL AND finished_at IS NOT NULL AND finished_at >= started_at)
);
ALTER TABLE node_executions ADD CONSTRAINT node_executions_output_error_check CHECK (
    (output IS NULL OR status = 'COMPLETED')
 AND (status <> 'FAILED' OR error IS NOT NULL)
);
-- One record per node attempt within an execution.
ALTER TABLE node_executions ADD CONSTRAINT node_executions_execution_node_attempt_key
    UNIQUE (execution_id, node_id, attempt);

------------------------------------------------------------------------------
-- Transition guards (defence in depth behind the repositories).
------------------------------------------------------------------------------
CREATE FUNCTION enforce_execution_transition() RETURNS trigger AS $$
BEGIN
    IF OLD.status IN ('COMPLETED', 'FAILED', 'CANCELLED') THEN
        RAISE EXCEPTION 'execution % is terminal (%) and immutable', OLD.id, OLD.status
            USING ERRCODE = 'check_violation';
    END IF;
    IF NEW.id IS DISTINCT FROM OLD.id
       OR NEW.workflow_id IS DISTINCT FROM OLD.workflow_id
       OR NEW.workflow_version_id IS DISTINCT FROM OLD.workflow_version_id
       OR NEW.input IS DISTINCT FROM OLD.input
       OR NEW.created_at IS DISTINCT FROM OLD.created_at
       OR (OLD.started_at IS NOT NULL AND NEW.started_at IS DISTINCT FROM OLD.started_at) THEN
        RAISE EXCEPTION 'execution % identity, input and start time are immutable', OLD.id
            USING ERRCODE = 'check_violation';
    END IF;
    IF NEW.status IS DISTINCT FROM OLD.status AND NOT (
           (OLD.status = 'PENDING' AND NEW.status = 'RUNNING')
        OR (OLD.status = 'RUNNING' AND NEW.status IN ('COMPLETED', 'FAILED', 'CANCELLED'))
    ) THEN
        RAISE EXCEPTION 'invalid execution transition % -> %', OLD.status, NEW.status
            USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER executions_enforce_transition
    BEFORE UPDATE ON executions
    FOR EACH ROW EXECUTE FUNCTION enforce_execution_transition();

CREATE FUNCTION enforce_node_execution_transition() RETURNS trigger AS $$
BEGIN
    IF OLD.status IN ('COMPLETED', 'FAILED', 'SKIPPED') THEN
        RAISE EXCEPTION 'node execution % is terminal (%) and immutable', OLD.id, OLD.status
            USING ERRCODE = 'check_violation';
    END IF;
    IF NEW.id IS DISTINCT FROM OLD.id
       OR NEW.execution_id IS DISTINCT FROM OLD.execution_id
       OR NEW.node_id IS DISTINCT FROM OLD.node_id
       OR NEW.node_type IS DISTINCT FROM OLD.node_type
       OR NEW.created_at IS DISTINCT FROM OLD.created_at
       OR (OLD.started_at IS NOT NULL AND NEW.started_at IS DISTINCT FROM OLD.started_at) THEN
        RAISE EXCEPTION 'node execution % identity and start time are immutable', OLD.id
            USING ERRCODE = 'check_violation';
    END IF;
    IF NEW.status IS DISTINCT FROM OLD.status AND NOT (
           (OLD.status = 'PENDING' AND NEW.status = 'RUNNING')
        OR (OLD.status = 'RUNNING' AND NEW.status IN ('COMPLETED', 'FAILED', 'SKIPPED'))
    ) THEN
        RAISE EXCEPTION 'invalid node execution transition % -> %', OLD.status, NEW.status
            USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER node_executions_enforce_transition
    BEFORE UPDATE ON node_executions
    FOR EACH ROW EXECUTE FUNCTION enforce_node_execution_transition();

------------------------------------------------------------------------------
-- execution_status_history (append-only)
------------------------------------------------------------------------------
CREATE TABLE execution_status_history (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    execution_id UUID NOT NULL REFERENCES executions(id),
    from_status VARCHAR(32),
    to_status VARCHAR(32) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    -- NULL -> PENDING records creation; the rest mirror the state machine.
    CONSTRAINT execution_status_history_transition_check CHECK (
           (from_status IS NULL AND to_status = 'PENDING')
        OR (from_status = 'PENDING' AND to_status = 'RUNNING')
        OR (from_status = 'RUNNING' AND to_status IN ('COMPLETED', 'FAILED', 'CANCELLED'))
    )
);

CREATE INDEX execution_status_history_execution_id_idx
    ON execution_status_history (execution_id, created_at);

-- Each execution enters each status at most once; this also makes a second
-- successful claim impossible to record.
CREATE UNIQUE INDEX execution_status_history_execution_to_status_key
    ON execution_status_history (execution_id, to_status);

CREATE FUNCTION reject_execution_history_mutation() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'execution_status_history is append-only'
        USING ERRCODE = 'insufficient_privilege';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER execution_status_history_append_only
    BEFORE UPDATE OR DELETE ON execution_status_history
    FOR EACH ROW EXECUTE FUNCTION reject_execution_history_mutation();

CREATE TRIGGER execution_status_history_no_truncate
    BEFORE TRUNCATE ON execution_status_history
    FOR EACH STATEMENT EXECUTE FUNCTION reject_execution_history_mutation();
