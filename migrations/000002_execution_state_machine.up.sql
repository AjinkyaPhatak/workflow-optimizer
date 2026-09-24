-- Phase 8: durable execution state machine.
--
-- Migration 000001 created placeholder executions/node_executions tables. This
-- migration evolves them in place (no parallel tables) and makes PostgreSQL the
-- final authority for the execution lifecycle:
--
--   * rows are normalized before any constraint is added, so every database
--     that satisfied 000001 upgrades cleanly; original values that had to be
--     changed are preserved verbatim in legacy_values (nothing is deleted);
--   * status changes are only possible as legal transitions, lifecycle
--     timestamps are database-generated, and history is written by triggers
--     from the actual OLD/NEW status (it cannot be inserted directly);
--   * node records can only be written while their execution is RUNNING;
--   * published workflow versions are immutable and only PUBLISHED versions
--     can start new executions.
--
-- If a previous 000002 DOWN archived Phase 8 data, it is restored here.

------------------------------------------------------------------------------
-- 1. Columns
------------------------------------------------------------------------------
ALTER TABLE executions RENAME COLUMN completed_at TO finished_at;
ALTER TABLE executions ADD COLUMN updated_at TIMESTAMPTZ NOT NULL DEFAULT now();
-- Identity of the worker claim that moved the execution to RUNNING.
ALTER TABLE executions ADD COLUMN claim_token UUID;
-- Original values of legacy (pre-Phase 8) rows that normalization changed.
ALTER TABLE executions ADD COLUMN legacy_values JSONB;

ALTER TABLE node_executions RENAME COLUMN completed_at TO finished_at;
ALTER TABLE node_executions ADD COLUMN updated_at TIMESTAMPTZ NOT NULL DEFAULT now();
ALTER TABLE node_executions ADD COLUMN legacy_values JSONB;
-- The 000001 status CHECK cannot express SKIPPED; the Phase 8 CHECK is added
-- in section 5, after legacy rows are normalized.
ALTER TABLE node_executions DROP CONSTRAINT node_executions_status_check;

UPDATE executions SET updated_at = GREATEST(created_at, COALESCE(started_at, created_at), COALESCE(finished_at, created_at));
UPDATE node_executions SET updated_at = GREATEST(created_at, COALESCE(started_at, created_at), COALESCE(finished_at, created_at));

------------------------------------------------------------------------------
-- 2. Restore data archived by a previous 000002 DOWN (UP -> DOWN -> UP)
------------------------------------------------------------------------------
DO $$
BEGIN
    IF to_regclass('phase8_rollback_executions') IS NOT NULL THEN
        UPDATE executions e
           SET claim_token = CASE WHEN e.status = 'PENDING' THEN NULL ELSE a.claim_token END,
               updated_at = GREATEST(a.updated_at, e.updated_at),
               legacy_values = a.legacy_values
          FROM phase8_rollback_executions a
         WHERE a.id = e.id;
        DROP TABLE phase8_rollback_executions;
    END IF;
    IF to_regclass('phase8_rollback_node_executions') IS NOT NULL THEN
        UPDATE node_executions n
           SET status = CASE WHEN n.status = 'CANCELLED' AND a.status = 'SKIPPED' THEN 'SKIPPED' ELSE n.status END,
               updated_at = GREATEST(a.updated_at, n.updated_at),
               legacy_values = a.legacy_values
          FROM phase8_rollback_node_executions a
         WHERE a.id = n.id;
        DROP TABLE phase8_rollback_node_executions;
    END IF;
END $$;

------------------------------------------------------------------------------
-- 3. Normalize legacy executions (000001 allowed any status/timestamp mix)
------------------------------------------------------------------------------
UPDATE executions e SET
    legacy_values = COALESCE(e.legacy_values, '{}'::jsonb) || jsonb_build_object(
        'normalized_by_migration', '000002',
        'original', jsonb_build_object(
            'workflow_id', e.workflow_id, 'started_at', e.started_at,
            'finished_at', e.finished_at, 'output', e.output, 'error', e.error)),
    -- The version defines what was executed, so it decides the workflow.
    workflow_id = v.workflow_id,
    started_at = CASE
        WHEN e.status = 'PENDING' THEN NULL
        ELSE COALESCE(e.started_at, LEAST(e.created_at, COALESCE(e.finished_at, e.created_at))) END,
    finished_at = CASE
        WHEN e.status IN ('PENDING', 'RUNNING') THEN NULL
        ELSE GREATEST(COALESCE(e.finished_at, e.started_at, e.created_at),
                      COALESCE(e.started_at, LEAST(e.created_at, COALESCE(e.finished_at, e.created_at)))) END,
    output = CASE WHEN e.status = 'COMPLETED' THEN e.output ELSE NULL END,
    error = CASE
        WHEN e.status = 'FAILED' THEN COALESCE(e.error, jsonb_build_object(
            'code', 'LEGACY_UNKNOWN_FAILURE',
            'message', 'execution was FAILED before Phase 8 without a recorded error',
            'retryable', false))
        WHEN e.status = 'CANCELLED' THEN e.error
        ELSE NULL END
FROM workflow_versions v
WHERE v.id = e.workflow_version_id
  AND (   v.workflow_id <> e.workflow_id
       OR (e.status = 'PENDING' AND (e.started_at IS NOT NULL OR e.finished_at IS NOT NULL))
       OR (e.status = 'RUNNING' AND (e.started_at IS NULL OR e.finished_at IS NOT NULL))
       OR (e.status IN ('COMPLETED', 'FAILED', 'CANCELLED')
           AND (e.started_at IS NULL OR e.finished_at IS NULL OR e.finished_at < e.started_at))
       OR (e.output IS NOT NULL AND e.status <> 'COMPLETED')
       OR (e.error IS NOT NULL AND e.status NOT IN ('FAILED', 'CANCELLED'))
       OR (e.status = 'FAILED' AND e.error IS NULL));

------------------------------------------------------------------------------
-- 4. Normalize legacy node executions
------------------------------------------------------------------------------
-- Legacy CANCELLED nodes were stopped by cancellation: in the Phase 8 model
-- that is a FAILED node with code EXECUTION_CANCELLED.
UPDATE node_executions n SET
    legacy_values = COALESCE(n.legacy_values, '{}'::jsonb) || jsonb_build_object(
        'normalized_by_migration', '000002',
        'original', jsonb_build_object(
            'status', n.status, 'attempt', n.attempt, 'started_at', n.started_at,
            'finished_at', n.finished_at, 'output', n.output, 'error', n.error)),
    status = CASE WHEN n.status = 'CANCELLED' THEN 'FAILED' ELSE n.status END,
    started_at = CASE
        WHEN n.status = 'PENDING' THEN NULL
        ELSE COALESCE(n.started_at, LEAST(n.created_at, COALESCE(n.finished_at, n.created_at))) END,
    finished_at = CASE
        WHEN n.status IN ('PENDING', 'RUNNING') THEN NULL
        ELSE GREATEST(COALESCE(n.finished_at, n.started_at, n.created_at),
                      COALESCE(n.started_at, LEAST(n.created_at, COALESCE(n.finished_at, n.created_at)))) END,
    output = CASE WHEN n.status = 'COMPLETED' THEN n.output ELSE NULL END,
    error = CASE
        WHEN n.status = 'CANCELLED' THEN COALESCE(n.error, jsonb_build_object(
            'code', 'EXECUTION_CANCELLED',
            'message', 'node was CANCELLED before Phase 8 (legacy status)',
            'node_id', n.node_id, 'retryable', false))
        WHEN n.status = 'FAILED' THEN COALESCE(n.error, jsonb_build_object(
            'code', 'LEGACY_UNKNOWN_FAILURE',
            'message', 'node was FAILED before Phase 8 without a recorded error',
            'node_id', n.node_id, 'retryable', false))
        ELSE NULL END
WHERE n.status = 'CANCELLED'
   OR (n.status = 'PENDING' AND (n.started_at IS NOT NULL OR n.finished_at IS NOT NULL))
   OR (n.status = 'RUNNING' AND (n.started_at IS NULL OR n.finished_at IS NOT NULL))
   OR (n.status IN ('COMPLETED', 'FAILED')
       AND (n.started_at IS NULL OR n.finished_at IS NULL OR n.finished_at < n.started_at))
   OR (n.output IS NOT NULL AND n.status <> 'COMPLETED')
   OR (n.error IS NOT NULL AND n.status <> 'FAILED')
   OR (n.status = 'FAILED' AND n.error IS NULL);

-- Duplicate (execution, node, attempt) rows get distinct attempt numbers in
-- creation order; the original attempt is kept in legacy_values.
WITH dup_groups AS (
    SELECT DISTINCT execution_id, node_id
      FROM node_executions
     GROUP BY execution_id, node_id, attempt
    HAVING count(*) > 1
), ranked AS (
    SELECT n.id, n.attempt,
           row_number() OVER (PARTITION BY n.execution_id, n.node_id ORDER BY n.attempt, n.created_at, n.id) AS rn
      FROM node_executions n
      JOIN dup_groups g ON g.execution_id = n.execution_id AND g.node_id = n.node_id
)
UPDATE node_executions n SET
    attempt = r.rn,
    legacy_values = COALESCE(n.legacy_values, '{}'::jsonb)
        || jsonb_build_object('normalized_by_migration', '000002', 'original_attempt', r.attempt)
FROM ranked r
WHERE r.id = n.id AND n.attempt <> r.rn;

------------------------------------------------------------------------------
-- 5. Constraints (validated against the normalized data)
------------------------------------------------------------------------------
ALTER TABLE workflow_versions
    ADD CONSTRAINT workflow_versions_id_workflow_id_key UNIQUE (id, workflow_id);
ALTER TABLE executions
    ADD CONSTRAINT executions_version_belongs_to_workflow_fkey
    FOREIGN KEY (workflow_version_id, workflow_id)
    REFERENCES workflow_versions (id, workflow_id);

ALTER TABLE executions ADD CONSTRAINT executions_lifecycle_timestamps_check CHECK (
    (status = 'PENDING' AND started_at IS NULL AND finished_at IS NULL)
 OR (status = 'RUNNING' AND started_at IS NOT NULL AND finished_at IS NULL)
 OR (status IN ('COMPLETED', 'FAILED', 'CANCELLED')
        AND started_at IS NOT NULL AND finished_at IS NOT NULL AND finished_at >= started_at)
);
ALTER TABLE executions ADD CONSTRAINT executions_output_error_check CHECK (
    (output IS NULL OR status = 'COMPLETED')
 AND (error IS NULL OR status IN ('FAILED', 'CANCELLED'))
 AND (status <> 'FAILED' OR error IS NOT NULL)
);
ALTER TABLE executions ADD CONSTRAINT executions_claim_token_check CHECK (
    status <> 'PENDING' OR claim_token IS NULL
);

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
 AND (error IS NULL OR status = 'FAILED')
 AND (status <> 'FAILED' OR error IS NOT NULL)
);
ALTER TABLE node_executions ADD CONSTRAINT node_executions_execution_node_attempt_key
    UNIQUE (execution_id, node_id, attempt);

------------------------------------------------------------------------------
-- 6. execution_status_history (database-generated, append-only)
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
-- Each execution enters each status at most once.
CREATE UNIQUE INDEX execution_status_history_execution_to_status_key
    ON execution_status_history (execution_id, to_status);

-- Restore history archived by a previous DOWN, but only for executions whose
-- status did not change while the schema was rolled back (otherwise the
-- archived rows no longer describe the row and stay in the archive table).
DO $$
BEGIN
    IF to_regclass('phase8_rollback_status_history') IS NOT NULL THEN
        INSERT INTO execution_status_history (id, execution_id, from_status, to_status, created_at, metadata)
        SELECT h.id, h.execution_id, h.from_status, h.to_status, h.created_at, h.metadata
          FROM phase8_rollback_status_history h
          JOIN executions e ON e.id = h.execution_id
         WHERE e.status = (
               SELECT h2.to_status FROM phase8_rollback_status_history h2
                WHERE h2.execution_id = e.id
                ORDER BY CASE h2.to_status WHEN 'PENDING' THEN 0 WHEN 'RUNNING' THEN 1 ELSE 2 END DESC
                LIMIT 1);
        DELETE FROM phase8_rollback_status_history h
         USING execution_status_history x WHERE x.id = h.id;
        IF NOT EXISTS (SELECT 1 FROM phase8_rollback_status_history) THEN
            DROP TABLE phase8_rollback_status_history;
        END IF;
    END IF;
END $$;

-- Backfill history for every execution that has none, so each row's history
-- describes its actual status.
INSERT INTO execution_status_history (execution_id, from_status, to_status, created_at, metadata)
SELECT e.id, NULL, 'PENDING', e.created_at, '{"backfilled_by_migration": "000002"}'::jsonb
  FROM executions e
 WHERE NOT EXISTS (SELECT 1 FROM execution_status_history h WHERE h.execution_id = e.id);
INSERT INTO execution_status_history (execution_id, from_status, to_status, created_at, metadata)
SELECT e.id, 'PENDING', 'RUNNING', e.started_at, '{"backfilled_by_migration": "000002"}'::jsonb
  FROM executions e
 WHERE e.status <> 'PENDING'
   AND NOT EXISTS (SELECT 1 FROM execution_status_history h WHERE h.execution_id = e.id AND h.to_status = 'RUNNING');
INSERT INTO execution_status_history (execution_id, from_status, to_status, created_at, metadata)
SELECT e.id, 'RUNNING', e.status, e.finished_at, '{"backfilled_by_migration": "000002"}'::jsonb
  FROM executions e
 WHERE e.status IN ('COMPLETED', 'FAILED', 'CANCELLED')
   AND NOT EXISTS (SELECT 1 FROM execution_status_history h WHERE h.execution_id = e.id AND h.to_status = e.status);

------------------------------------------------------------------------------
-- 7. Lifecycle triggers. Errors carry a CONSTRAINT name so the application can
--    map them precisely. Functions pin search_path to this schema. Lifecycle
--    timestamps use clock_timestamp() (the moment of the change), not now()
--    (the transaction start), so a transition that waited on a lock is not
--    stamped earlier than the writes it waited for.
------------------------------------------------------------------------------

-- Executions: creation is PENDING only; the version must be PUBLISHED.
CREATE FUNCTION lifecycle_execution_before_insert() RETURNS trigger
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

CREATE TRIGGER executions_lifecycle_before_insert
    BEFORE INSERT ON executions
    FOR EACH ROW EXECUTE FUNCTION lifecycle_execution_before_insert();

-- Executions: every UPDATE is a legal status transition; timestamps are
-- generated here; immutable columns stay immutable.
CREATE FUNCTION lifecycle_execution_before_update() RETURNS trigger
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

CREATE TRIGGER executions_lifecycle_before_update
    BEFORE UPDATE ON executions
    FOR EACH ROW EXECUTE FUNCTION lifecycle_execution_before_update();

-- History is generated from the committed status change itself. The writer may
-- supply a transition id (commit marker) and metadata through transaction-local
-- settings; it cannot choose the statuses.
CREATE FUNCTION lifecycle_execution_history() RETURNS trigger
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

CREATE TRIGGER executions_lifecycle_history
    AFTER INSERT OR UPDATE ON executions
    FOR EACH ROW EXECUTE FUNCTION lifecycle_execution_history();

-- History rows only come from the trigger above and are never changed.
CREATE FUNCTION lifecycle_history_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path FROM CURRENT AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        IF pg_trigger_depth() < 2 THEN
            RAISE EXCEPTION 'execution_status_history is written only by execution transitions'
                USING ERRCODE = 'insufficient_privilege', CONSTRAINT = 'lifecycle_history_generated';
        END IF;
        RETURN NEW;
    END IF;
    RAISE EXCEPTION 'execution_status_history is append-only'
        USING ERRCODE = 'insufficient_privilege', CONSTRAINT = 'lifecycle_history_append_only';
END $$;

CREATE TRIGGER execution_status_history_guard
    BEFORE INSERT OR UPDATE OR DELETE ON execution_status_history
    FOR EACH ROW EXECUTE FUNCTION lifecycle_history_guard();
CREATE TRIGGER execution_status_history_no_truncate
    BEFORE TRUNCATE ON execution_status_history
    FOR EACH STATEMENT EXECUTE FUNCTION lifecycle_history_guard();

-- Node executions: written only while the parent execution is RUNNING. The
-- parent row is share-locked, so a concurrent terminal transition of the
-- execution and a node write serialize instead of interleaving.
CREATE FUNCTION lifecycle_require_running_parent(execution UUID) RETURNS void
LANGUAGE plpgsql SET search_path FROM CURRENT AS $$
DECLARE
    parent_status TEXT;
BEGIN
    SELECT status INTO parent_status FROM executions WHERE id = execution FOR SHARE;
    IF NOT FOUND THEN
        RETURN; -- the foreign key reports the missing execution
    END IF;
    IF parent_status <> 'RUNNING' THEN
        RAISE EXCEPTION 'execution % is % and does not accept node records', execution, parent_status
            USING ERRCODE = 'check_violation', CONSTRAINT = 'lifecycle_parent_not_running';
    END IF;
END $$;

CREATE FUNCTION lifecycle_node_before_insert() RETURNS trigger
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

CREATE TRIGGER node_executions_lifecycle_before_insert
    BEFORE INSERT ON node_executions
    FOR EACH ROW EXECUTE FUNCTION lifecycle_node_before_insert();

CREATE FUNCTION lifecycle_node_before_update() RETURNS trigger
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

CREATE TRIGGER node_executions_lifecycle_before_update
    BEFORE UPDATE ON node_executions
    FOR EACH ROW EXECUTE FUNCTION lifecycle_node_before_update();

-- Workflow versions: a version is an immutable snapshot once it is no longer a
-- DRAFT or once any execution references it. Drafts stay editable.
CREATE FUNCTION lifecycle_workflow_version_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path FROM CURRENT AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        IF OLD.status <> 'DRAFT' THEN
            RAISE EXCEPTION 'workflow version % is % and cannot be deleted', OLD.id, OLD.status
                USING ERRCODE = 'check_violation', CONSTRAINT = 'lifecycle_version_immutable';
        END IF;
        RETURN OLD;
    END IF;
    IF (OLD.status <> 'DRAFT' OR EXISTS (SELECT 1 FROM executions WHERE workflow_version_id = OLD.id))
       AND (NEW.definition IS DISTINCT FROM OLD.definition
            OR NEW.workflow_id IS DISTINCT FROM OLD.workflow_id
            OR NEW.version_number IS DISTINCT FROM OLD.version_number
            OR NEW.created_by IS DISTINCT FROM OLD.created_by
            OR NEW.created_at IS DISTINCT FROM OLD.created_at) THEN
        RAISE EXCEPTION 'workflow version % is immutable (status %)', OLD.id, OLD.status
            USING ERRCODE = 'check_violation', CONSTRAINT = 'lifecycle_version_immutable';
    END IF;
    IF OLD.status <> 'DRAFT' AND NEW.status = 'DRAFT' THEN
        RAISE EXCEPTION 'workflow version % cannot return to DRAFT', OLD.id
            USING ERRCODE = 'check_violation', CONSTRAINT = 'lifecycle_version_immutable';
    END IF;
    IF OLD.published_at IS NOT NULL AND NEW.published_at IS DISTINCT FROM OLD.published_at THEN
        RAISE EXCEPTION 'workflow version % publication time is immutable', OLD.id
            USING ERRCODE = 'check_violation', CONSTRAINT = 'lifecycle_version_immutable';
    END IF;
    IF NEW.status = 'PUBLISHED' AND NEW.published_at IS NULL THEN
        NEW.published_at := clock_timestamp();
    END IF;
    RETURN NEW;
END $$;

CREATE TRIGGER workflow_versions_lifecycle_guard
    BEFORE UPDATE OR DELETE ON workflow_versions
    FOR EACH ROW EXECUTE FUNCTION lifecycle_workflow_version_guard();
