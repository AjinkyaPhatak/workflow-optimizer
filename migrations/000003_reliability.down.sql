-- Roll back Phase 10 to the 000002 (Phase 8) schema.
--
-- Rollback semantics: 000002 cannot represent attempts, retry schedules,
-- deadlines, leases, dispatch bookkeeping, dead letters or cancellation
-- requests. Instead of destroying that data it is archived in phase10_rollback_*
-- tables (without foreign keys, so they never block an older DOWN); a later
-- 000003 UP restores it. Leases and dispatch rows are derived state and are
-- rebuilt by UP. History rows that 000002 cannot hold (RUNNING -> PENDING and
-- repeated attempts) are archived and removed; each execution keeps its
-- creation record, its latest claim and its terminal record.

DROP TRIGGER IF EXISTS executions_lifecycle_after_change ON executions;
DROP TRIGGER IF EXISTS execution_leases_guard ON execution_leases;
DROP TRIGGER IF EXISTS execution_dead_letters_guard ON execution_dead_letters;

-- Archive Phase 10 columns (merged with any archive left by an earlier DOWN).
CREATE TABLE IF NOT EXISTS phase10_rollback_executions (
    id UUID PRIMARY KEY, status VARCHAR(32) NOT NULL, attempt INTEGER NOT NULL, max_attempts INTEGER NOT NULL,
    timeout_ms BIGINT, deadline_at TIMESTAMPTZ, next_attempt_at TIMESTAMPTZ, last_error JSONB);
INSERT INTO phase10_rollback_executions (id, status, attempt, max_attempts, timeout_ms, deadline_at, next_attempt_at, last_error)
SELECT id, status, attempt, max_attempts, timeout_ms, deadline_at, next_attempt_at, last_error FROM executions
ON CONFLICT (id) DO UPDATE SET status = EXCLUDED.status, attempt = EXCLUDED.attempt,
    max_attempts = EXCLUDED.max_attempts, timeout_ms = EXCLUDED.timeout_ms, deadline_at = EXCLUDED.deadline_at,
    next_attempt_at = EXCLUDED.next_attempt_at, last_error = EXCLUDED.last_error;

CREATE TABLE IF NOT EXISTS phase10_rollback_node_executions (
    id UUID PRIMARY KEY, execution_attempt INTEGER NOT NULL, output_values JSONB);
INSERT INTO phase10_rollback_node_executions (id, execution_attempt, output_values)
SELECT id, execution_attempt, output_values FROM node_executions
ON CONFLICT (id) DO UPDATE SET execution_attempt = EXCLUDED.execution_attempt, output_values = EXCLUDED.output_values;

CREATE TABLE IF NOT EXISTS phase10_rollback_status_history (
    id UUID PRIMARY KEY, execution_id UUID NOT NULL, from_status VARCHAR(32), to_status VARCHAR(32) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL, metadata JSONB NOT NULL, attempt INTEGER NOT NULL);
INSERT INTO phase10_rollback_status_history (id, execution_id, from_status, to_status, created_at, metadata, attempt)
SELECT id, execution_id, from_status, to_status, created_at, metadata, attempt FROM execution_status_history
ON CONFLICT (id) DO UPDATE SET attempt = EXCLUDED.attempt;

-- An execution that is PENDING again (waiting for a retry) keeps only its
-- creation record: in 000002 a PENDING execution has never been claimed, and
-- its next claim must be able to record PENDING -> RUNNING (each execution
-- enters each status once there).
ALTER TABLE execution_status_history DISABLE TRIGGER execution_status_history_guard;
DELETE FROM execution_status_history h
 WHERE (h.from_status = 'RUNNING' AND h.to_status = 'PENDING')
    OR (h.to_status = 'RUNNING' AND h.attempt < (
            SELECT max(h2.attempt) FROM execution_status_history h2
             WHERE h2.execution_id = h.execution_id AND h2.to_status = 'RUNNING'))
    OR (h.to_status = 'RUNNING' AND EXISTS (
            SELECT 1 FROM executions e WHERE e.id = h.execution_id AND e.status = 'PENDING'));
ALTER TABLE execution_status_history ENABLE TRIGGER execution_status_history_guard;
-- The archive keeps every row (also the ones still in the table), so UP can
-- restore each row's attempt number and re-insert the removed ones (for
-- executions whose status did not change while rolled back).

-- Durable records keep their tables under archive names.
ALTER TABLE execution_dead_letters DROP CONSTRAINT IF EXISTS execution_dead_letters_execution_id_fkey;
ALTER TABLE execution_dead_letters RENAME TO phase10_rollback_dead_letters;
ALTER TABLE execution_cancel_requests DROP CONSTRAINT IF EXISTS execution_cancel_requests_execution_id_fkey;
ALTER TABLE execution_cancel_requests RENAME TO phase10_rollback_cancel_requests;
DROP TABLE execution_leases;
DROP TABLE execution_dispatch;

DROP FUNCTION IF EXISTS lifecycle_execution_after_change();
DROP FUNCTION IF EXISTS lifecycle_lease_guard();
DROP FUNCTION IF EXISTS lifecycle_dead_letter_guard();

CREATE OR REPLACE FUNCTION lifecycle_execution_before_insert() RETURNS trigger
LANGUAGE plpgsql SET search_path FROM CURRENT AS $$
DECLARE
    version_status TEXT;
BEGIN
    IF NEW.status IS DISTINCT FROM 'PENDING' OR NEW.started_at IS NOT NULL OR NEW.finished_at IS NOT NULL
       OR NEW.output IS NOT NULL OR NEW.error IS NOT NULL OR NEW.claim_token IS NOT NULL
       OR NEW.legacy_values IS NOT NULL THEN
        RAISE EXCEPTION 'executions must be created PENDING without lifecycle data'
            USING ERRCODE = 'check_violation', CONSTRAINT = 'lifecycle_insert_requires_pending';
    END IF;
    SELECT status INTO version_status FROM workflow_versions
     WHERE id = NEW.workflow_version_id FOR SHARE;
    IF FOUND AND version_status <> 'PUBLISHED' THEN
        RAISE EXCEPTION 'workflow version % is % and cannot start an execution', NEW.workflow_version_id, version_status
            USING ERRCODE = 'check_violation', CONSTRAINT = 'lifecycle_version_not_published';
    END IF;
    NEW.created_at := clock_timestamp();
    NEW.updated_at := clock_timestamp();
    RETURN NEW;
END $$;

CREATE OR REPLACE FUNCTION lifecycle_execution_before_update() RETURNS trigger
LANGUAGE plpgsql SET search_path FROM CURRENT AS $$
BEGIN
    IF OLD.status IN ('COMPLETED', 'FAILED', 'CANCELLED') THEN
        RAISE EXCEPTION 'execution % is terminal (%) and immutable', OLD.id, OLD.status
            USING ERRCODE = 'check_violation', CONSTRAINT = 'lifecycle_terminal_immutable';
    END IF;
    IF NEW.status IS NOT DISTINCT FROM OLD.status THEN
        RAISE EXCEPTION 'execution % may only change through a status transition', OLD.id
            USING ERRCODE = 'check_violation', CONSTRAINT = 'lifecycle_update_requires_transition';
    END IF;
    IF NOT ((OLD.status = 'PENDING' AND NEW.status = 'RUNNING')
         OR (OLD.status = 'RUNNING' AND NEW.status IN ('COMPLETED', 'FAILED', 'CANCELLED'))) THEN
        RAISE EXCEPTION 'invalid execution transition % -> %', OLD.status, NEW.status
            USING ERRCODE = 'check_violation', CONSTRAINT = 'lifecycle_invalid_transition';
    END IF;
    IF NEW.id IS DISTINCT FROM OLD.id
       OR NEW.workflow_id IS DISTINCT FROM OLD.workflow_id
       OR NEW.workflow_version_id IS DISTINCT FROM OLD.workflow_version_id
       OR NEW.input IS DISTINCT FROM OLD.input
       OR NEW.created_at IS DISTINCT FROM OLD.created_at
       OR NEW.legacy_values IS DISTINCT FROM OLD.legacy_values
       OR (NEW.output IS DISTINCT FROM OLD.output AND NEW.status <> 'COMPLETED')
       OR (NEW.error IS DISTINCT FROM OLD.error AND NEW.status NOT IN ('FAILED', 'CANCELLED'))
       OR (NEW.claim_token IS DISTINCT FROM OLD.claim_token AND NEW.status <> 'RUNNING') THEN
        RAISE EXCEPTION 'execution % column cannot change in transition % -> %', OLD.id, OLD.status, NEW.status
            USING ERRCODE = 'check_violation', CONSTRAINT = 'lifecycle_immutable_column';
    END IF;
    IF NEW.status = 'RUNNING' AND NEW.claim_token IS NULL THEN
        RAISE EXCEPTION 'claiming execution % requires a claim token', OLD.id
            USING ERRCODE = 'check_violation', CONSTRAINT = 'lifecycle_claim_requires_token';
    END IF;
    IF NEW.status IN ('COMPLETED', 'FAILED', 'CANCELLED') AND EXISTS (
        SELECT 1 FROM node_executions WHERE execution_id = OLD.id AND status = 'RUNNING') THEN
        RAISE EXCEPTION 'execution % still has RUNNING node executions', OLD.id
            USING ERRCODE = 'check_violation', CONSTRAINT = 'lifecycle_running_nodes';
    END IF;
    NEW.started_at := CASE WHEN NEW.status = 'RUNNING' THEN clock_timestamp() ELSE OLD.started_at END;
    NEW.finished_at := CASE WHEN NEW.status IN ('COMPLETED', 'FAILED', 'CANCELLED')
                            THEN GREATEST(clock_timestamp(), OLD.started_at) ELSE NULL END;
    NEW.updated_at := GREATEST(clock_timestamp(), OLD.updated_at);
    RETURN NEW;
END $$;

CREATE OR REPLACE FUNCTION lifecycle_execution_history() RETURNS trigger
LANGUAGE plpgsql SET search_path FROM CURRENT AS $$
DECLARE
    tid TEXT := NULLIF(current_setting('workflow.transition_id', true), '');
    meta TEXT := NULLIF(current_setting('workflow.transition_metadata', true), '');
BEGIN
    IF TG_OP = 'UPDATE' AND NEW.status IS NOT DISTINCT FROM OLD.status THEN
        RETURN NULL;
    END IF;
    INSERT INTO execution_status_history (id, execution_id, from_status, to_status, created_at, metadata)
    VALUES (COALESCE(tid::uuid, gen_random_uuid()), NEW.id,
            CASE WHEN TG_OP = 'INSERT' THEN NULL ELSE OLD.status END,
            NEW.status, clock_timestamp(), COALESCE(meta::jsonb, '{}'::jsonb));
    RETURN NULL;
END $$;

CREATE OR REPLACE FUNCTION lifecycle_node_before_insert() RETURNS trigger
LANGUAGE plpgsql SET search_path FROM CURRENT AS $$
BEGIN
    PERFORM lifecycle_require_running_parent(NEW.execution_id);
    IF NEW.status IS DISTINCT FROM 'PENDING' OR NEW.started_at IS NOT NULL OR NEW.finished_at IS NOT NULL
       OR NEW.output IS NOT NULL OR NEW.error IS NOT NULL OR NEW.legacy_values IS NOT NULL THEN
        RAISE EXCEPTION 'node executions must be created PENDING without lifecycle data'
            USING ERRCODE = 'check_violation', CONSTRAINT = 'lifecycle_insert_requires_pending';
    END IF;
    NEW.created_at := clock_timestamp();
    NEW.updated_at := clock_timestamp();
    RETURN NEW;
END $$;

CREATE OR REPLACE FUNCTION lifecycle_node_before_update() RETURNS trigger
LANGUAGE plpgsql SET search_path FROM CURRENT AS $$
BEGIN
    IF OLD.status IN ('COMPLETED', 'FAILED', 'SKIPPED') THEN
        RAISE EXCEPTION 'node execution % is terminal (%) and immutable', OLD.id, OLD.status
            USING ERRCODE = 'check_violation', CONSTRAINT = 'lifecycle_terminal_immutable';
    END IF;
    PERFORM lifecycle_require_running_parent(OLD.execution_id);
    IF NEW.status IS NOT DISTINCT FROM OLD.status THEN
        RAISE EXCEPTION 'node execution % may only change through a status transition', OLD.id
            USING ERRCODE = 'check_violation', CONSTRAINT = 'lifecycle_update_requires_transition';
    END IF;
    IF NOT ((OLD.status = 'PENDING' AND NEW.status = 'RUNNING')
         OR (OLD.status = 'RUNNING' AND NEW.status IN ('COMPLETED', 'FAILED', 'SKIPPED'))) THEN
        RAISE EXCEPTION 'invalid node execution transition % -> %', OLD.status, NEW.status
            USING ERRCODE = 'check_violation', CONSTRAINT = 'lifecycle_invalid_transition';
    END IF;
    IF NEW.id IS DISTINCT FROM OLD.id
       OR NEW.execution_id IS DISTINCT FROM OLD.execution_id
       OR NEW.node_id IS DISTINCT FROM OLD.node_id
       OR NEW.node_type IS DISTINCT FROM OLD.node_type
       OR NEW.attempt IS DISTINCT FROM OLD.attempt
       OR NEW.created_at IS DISTINCT FROM OLD.created_at
       OR NEW.legacy_values IS DISTINCT FROM OLD.legacy_values
       OR (NEW.input IS DISTINCT FROM OLD.input AND NEW.status <> 'RUNNING')
       OR (NEW.output IS DISTINCT FROM OLD.output AND NEW.status <> 'COMPLETED')
       OR (NEW.error IS DISTINCT FROM OLD.error AND NEW.status <> 'FAILED') THEN
        RAISE EXCEPTION 'node execution % column cannot change in transition % -> %', OLD.id, OLD.status, NEW.status
            USING ERRCODE = 'check_violation', CONSTRAINT = 'lifecycle_immutable_column';
    END IF;
    NEW.started_at := CASE WHEN NEW.status = 'RUNNING' THEN clock_timestamp() ELSE OLD.started_at END;
    NEW.finished_at := CASE WHEN NEW.status IN ('COMPLETED', 'FAILED', 'SKIPPED')
                            THEN GREATEST(clock_timestamp(), OLD.started_at) ELSE NULL END;
    NEW.updated_at := GREATEST(clock_timestamp(), OLD.updated_at);
    RETURN NEW;
END $$;

DROP FUNCTION IF EXISTS lifecycle_require_current_attempt(UUID, INTEGER);

DROP INDEX IF EXISTS executions_pending_next_attempt_idx;
DROP INDEX IF EXISTS execution_status_history_execution_attempt_to_status_key;
CREATE UNIQUE INDEX execution_status_history_execution_to_status_key
    ON execution_status_history (execution_id, to_status);
ALTER TABLE execution_status_history DROP CONSTRAINT execution_status_history_transition_check;
ALTER TABLE execution_status_history ADD CONSTRAINT execution_status_history_transition_check CHECK (
       (from_status IS NULL AND to_status = 'PENDING')
    OR (from_status = 'PENDING' AND to_status = 'RUNNING')
    OR (from_status = 'RUNNING' AND to_status IN ('COMPLETED', 'FAILED', 'CANCELLED'))
);
ALTER TABLE execution_status_history DROP COLUMN attempt;

ALTER TABLE node_executions DROP CONSTRAINT IF EXISTS node_executions_output_values_check;
ALTER TABLE node_executions DROP CONSTRAINT IF EXISTS node_executions_execution_attempt_check;
ALTER TABLE node_executions DROP COLUMN output_values;
ALTER TABLE node_executions DROP COLUMN execution_attempt;

ALTER TABLE executions DROP CONSTRAINT IF EXISTS executions_next_attempt_check;
ALTER TABLE executions DROP CONSTRAINT IF EXISTS executions_timeout_check;
ALTER TABLE executions DROP CONSTRAINT IF EXISTS executions_attempt_check;
ALTER TABLE executions
    DROP COLUMN last_error,
    DROP COLUMN next_attempt_at,
    DROP COLUMN deadline_at,
    DROP COLUMN timeout_ms,
    DROP COLUMN max_attempts,
    DROP COLUMN attempt;
