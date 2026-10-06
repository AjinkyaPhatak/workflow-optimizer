-- Phase 10: retry, timeout & reliability.
--
-- PostgreSQL stays the only authority. This migration extends (never
-- bypasses) the Phase 8 lifecycle:
--
--   * executions gain a persisted attempt counter, an attempt limit, a
--     timeout/deadline and a retry schedule. The single new transition is
--     RUNNING -> PENDING ("retry scheduled" / "recovered"); PENDING keeps a
--     unique predecessor (creation is not a transition), so every change is
--     still one compare-and-set. The database, not the caller, increments the
--     attempt, stamps the deadline, refuses claims of retries that are not due
--     or have no attempts left, and refuses retries that are cancel-requested
--     or would start after the deadline.
--   * execution_leases records which worker owns the current RUNNING attempt
--     until when; heartbeats extend it; a lease can only exist for the
--     execution's current claim and disappears when the execution leaves
--     RUNNING.
--   * execution_dispatch tracks, per PENDING execution, whether the current
--     attempt has been handed to the queue, so schedulers can claim due work
--     atomically (FOR UPDATE SKIP LOCKED) and re-dispatch work whose delivery
--     was lost.
--   * execution_dead_letters is the authoritative record of why an execution
--     stopped retrying (written in the same transaction as FAILED).
--   * execution_cancel_requests records durable cancellation intent, so a
--     cancellation also wins against executions that are waiting for a retry.
--   * node_executions are fenced by the execution attempt that wrote them and
--     keep their typed output values, so a retried execution can resume from
--     completed nodes instead of re-running them.
--
-- Rows written before this migration keep their meaning: they get attempt 1
-- if they were ever claimed, a limit of 1 (no retries) and no deadline.
-- If a previous 000003 DOWN archived Phase 10 data, it is restored here.

------------------------------------------------------------------------------
-- 1. Columns and backfill (lifecycle triggers are suspended only for the
--    backfill of the new columns; no status changes happen here)
------------------------------------------------------------------------------
ALTER TABLE executions
    ADD COLUMN attempt INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN max_attempts INTEGER NOT NULL DEFAULT 1,
    ADD COLUMN timeout_ms BIGINT,
    ADD COLUMN deadline_at TIMESTAMPTZ,
    ADD COLUMN next_attempt_at TIMESTAMPTZ,
    ADD COLUMN last_error JSONB;

ALTER TABLE execution_status_history ADD COLUMN attempt INTEGER NOT NULL DEFAULT 0;

ALTER TABLE node_executions
    ADD COLUMN execution_attempt INTEGER,
    ADD COLUMN output_values JSONB;

ALTER TABLE executions DISABLE TRIGGER USER;
ALTER TABLE execution_status_history DISABLE TRIGGER USER;
ALTER TABLE node_executions DISABLE TRIGGER USER;

UPDATE executions SET attempt = 1 WHERE status <> 'PENDING';
UPDATE execution_status_history SET attempt = CASE WHEN to_status = 'PENDING' THEN 0 ELSE 1 END;
UPDATE node_executions SET execution_attempt = 1;

-- Restore the columns archived by a previous 000003 DOWN (UP -> DOWN -> UP),
-- but only for rows whose status did not change while rolled back.
DO $$
BEGIN
    IF to_regclass('phase10_rollback_executions') IS NOT NULL THEN
        UPDATE executions e SET
            attempt = a.attempt, max_attempts = a.max_attempts, timeout_ms = a.timeout_ms,
            deadline_at = a.deadline_at, last_error = a.last_error,
            next_attempt_at = CASE WHEN e.status = 'PENDING' THEN a.next_attempt_at ELSE NULL END
          FROM phase10_rollback_executions a
         WHERE a.id = e.id AND a.status = e.status;
        -- Still needed by the history restore in section 2.
    END IF;
    IF to_regclass('phase10_rollback_node_executions') IS NOT NULL THEN
        UPDATE node_executions n SET execution_attempt = a.execution_attempt, output_values = a.output_values
          FROM phase10_rollback_node_executions a
         WHERE a.id = n.id;
        DROP TABLE phase10_rollback_node_executions;
    END IF;
    IF to_regclass('phase10_rollback_status_history') IS NOT NULL THEN
        UPDATE execution_status_history h SET attempt = a.attempt
          FROM phase10_rollback_status_history a
         WHERE a.id = h.id;
    END IF;
END $$;

ALTER TABLE executions ENABLE TRIGGER USER;
ALTER TABLE execution_status_history ENABLE TRIGGER USER;
ALTER TABLE node_executions ENABLE TRIGGER USER;

ALTER TABLE node_executions ALTER COLUMN execution_attempt SET NOT NULL;

------------------------------------------------------------------------------
-- 2. Constraints
------------------------------------------------------------------------------
ALTER TABLE executions ADD CONSTRAINT executions_attempt_check CHECK (
    attempt >= 0 AND max_attempts >= 1 AND attempt <= max_attempts
    -- a PENDING execution can always be claimed again
    AND (status <> 'PENDING' OR attempt < max_attempts)
    AND (status <> 'RUNNING' OR attempt >= 1)
);
ALTER TABLE executions ADD CONSTRAINT executions_timeout_check CHECK (
    (timeout_ms IS NULL OR timeout_ms > 0)
    AND (deadline_at IS NULL OR timeout_ms IS NOT NULL)
);
ALTER TABLE executions ADD CONSTRAINT executions_next_attempt_check CHECK (
    next_attempt_at IS NULL OR status = 'PENDING'
);
ALTER TABLE node_executions ADD CONSTRAINT node_executions_execution_attempt_check CHECK (execution_attempt >= 1);
ALTER TABLE node_executions ADD CONSTRAINT node_executions_output_values_check CHECK (
    output_values IS NULL OR status = 'COMPLETED'
);

-- History: an execution enters PENDING and RUNNING once per attempt.
DROP INDEX execution_status_history_execution_to_status_key;
CREATE UNIQUE INDEX execution_status_history_execution_attempt_to_status_key
    ON execution_status_history (execution_id, attempt, to_status);
ALTER TABLE execution_status_history DROP CONSTRAINT execution_status_history_transition_check;
ALTER TABLE execution_status_history ADD CONSTRAINT execution_status_history_transition_check CHECK (
       (from_status IS NULL AND to_status = 'PENDING')
    OR (from_status = 'PENDING' AND to_status = 'RUNNING')
    OR (from_status = 'RUNNING' AND to_status IN ('COMPLETED', 'FAILED', 'CANCELLED', 'PENDING'))
);

-- Archived history rows (RUNNING -> PENDING, earlier attempts, the claims of
-- executions waiting for a retry) come back once the constraints above accept
-- them, but only for executions whose status did not change while rolled
-- back: otherwise the 000002 history written meanwhile describes the row and
-- the archived rows describe a superseded state.
DO $$
BEGIN
    IF to_regclass('phase10_rollback_status_history') IS NOT NULL THEN
        IF to_regclass('phase10_rollback_executions') IS NOT NULL THEN
            ALTER TABLE execution_status_history DISABLE TRIGGER execution_status_history_guard;
            INSERT INTO execution_status_history (id, execution_id, from_status, to_status, created_at, metadata, attempt)
            SELECT a.id, a.execution_id, a.from_status, a.to_status, a.created_at, a.metadata, a.attempt
              FROM phase10_rollback_status_history a
              JOIN executions e ON e.id = a.execution_id
              JOIN phase10_rollback_executions x ON x.id = e.id AND x.status = e.status
             WHERE NOT EXISTS (SELECT 1 FROM execution_status_history h WHERE h.id = a.id)
            ON CONFLICT DO NOTHING;
            ALTER TABLE execution_status_history ENABLE TRIGGER execution_status_history_guard;
        END IF;
        DROP TABLE phase10_rollback_status_history;
    END IF;
    IF to_regclass('phase10_rollback_executions') IS NOT NULL THEN
        DROP TABLE phase10_rollback_executions;
    END IF;
END $$;

CREATE INDEX executions_pending_next_attempt_idx
    ON executions (next_attempt_at) WHERE status = 'PENDING';

------------------------------------------------------------------------------
-- 3. New tables
------------------------------------------------------------------------------

-- Ownership of the current RUNNING attempt. Exactly the current claim may
-- hold it; it is deleted when the execution leaves RUNNING.
CREATE TABLE execution_leases (
    execution_id UUID PRIMARY KEY REFERENCES executions(id),
    claim_token UUID NOT NULL,
    attempt INTEGER NOT NULL CHECK (attempt >= 1),
    owner TEXT NOT NULL CHECK (owner <> ''),
    acquired_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    heartbeat_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    expires_at TIMESTAMPTZ NOT NULL,
    CONSTRAINT execution_leases_expiry_check CHECK (expires_at > acquired_at)
);
CREATE INDEX execution_leases_expires_at_idx ON execution_leases (expires_at);

-- Queue hand-off bookkeeping for PENDING executions (one row per PENDING
-- execution, maintained by the lifecycle triggers).
CREATE TABLE execution_dispatch (
    execution_id UUID PRIMARY KEY REFERENCES executions(id),
    attempt INTEGER NOT NULL,
    dispatched_at TIMESTAMPTZ,
    dispatch_count INTEGER NOT NULL DEFAULT 0
);

-- Durable cancellation intent.
DO $$
BEGIN
    IF to_regclass('phase10_rollback_cancel_requests') IS NOT NULL THEN
        ALTER TABLE phase10_rollback_cancel_requests RENAME TO execution_cancel_requests;
        DELETE FROM execution_cancel_requests c WHERE NOT EXISTS (SELECT 1 FROM executions e WHERE e.id = c.execution_id);
        ALTER TABLE execution_cancel_requests ADD CONSTRAINT execution_cancel_requests_execution_id_fkey
            FOREIGN KEY (execution_id) REFERENCES executions(id);
    ELSE
        CREATE TABLE execution_cancel_requests (
            execution_id UUID PRIMARY KEY REFERENCES executions(id),
            requested_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
        );
    END IF;
END $$;

-- Authoritative dead-letter record: why an execution stopped retrying.
DO $$
BEGIN
    IF to_regclass('phase10_rollback_dead_letters') IS NOT NULL THEN
        ALTER TABLE phase10_rollback_dead_letters RENAME TO execution_dead_letters;
        DELETE FROM execution_dead_letters d WHERE NOT EXISTS (SELECT 1 FROM executions e WHERE e.id = d.execution_id);
        ALTER TABLE execution_dead_letters ADD CONSTRAINT execution_dead_letters_execution_id_fkey
            FOREIGN KEY (execution_id) REFERENCES executions(id);
    ELSE
        CREATE TABLE execution_dead_letters (
            id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
            execution_id UUID NOT NULL UNIQUE REFERENCES executions(id),
            attempt INTEGER NOT NULL CHECK (attempt >= 1),
            error JSONB NOT NULL,
            reason VARCHAR(64) NOT NULL CHECK (reason IN ('attempts_exhausted', 'deadline_exceeded', 'retry_unsafe')),
            created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
        );
    END IF;
END $$;

-- Every PENDING execution is dispatchable. Rows that were PENDING before this
-- migration count as dispatched now: a scheduler re-dispatches them if they are
-- still unclaimed after its re-dispatch interval (recovering jobs lost from the
-- queue before Phase 10). A scheduled retry restored from a rollback archive
-- (next_attempt_at set) was never handed to the queue: it is owed a delivery.
INSERT INTO execution_dispatch (execution_id, attempt, dispatched_at)
SELECT id, attempt, CASE WHEN next_attempt_at IS NULL THEN clock_timestamp() END
  FROM executions WHERE status = 'PENDING';

------------------------------------------------------------------------------
-- 4. Lifecycle triggers (Phase 8 functions replaced by their Phase 10
--    versions; every Phase 8 rule is kept)
------------------------------------------------------------------------------

CREATE OR REPLACE FUNCTION lifecycle_execution_before_insert() RETURNS trigger
LANGUAGE plpgsql SET search_path FROM CURRENT AS $$
DECLARE
    version_status TEXT;
BEGIN
    IF NEW.status IS DISTINCT FROM 'PENDING' OR NEW.started_at IS NOT NULL OR NEW.finished_at IS NOT NULL
       OR NEW.output IS NOT NULL OR NEW.error IS NOT NULL OR NEW.claim_token IS NOT NULL
       OR NEW.legacy_values IS NOT NULL
       OR NEW.attempt IS DISTINCT FROM 0 OR NEW.deadline_at IS NOT NULL
       OR NEW.next_attempt_at IS NOT NULL OR NEW.last_error IS NOT NULL THEN
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
DECLARE
    now_ts TIMESTAMPTZ := clock_timestamp();
    cancel_requested BOOLEAN;
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
         OR (OLD.status = 'RUNNING' AND NEW.status IN ('COMPLETED', 'FAILED', 'CANCELLED', 'PENDING'))) THEN
        RAISE EXCEPTION 'invalid execution transition % -> %', OLD.status, NEW.status
            USING ERRCODE = 'check_violation', CONSTRAINT = 'lifecycle_invalid_transition';
    END IF;
    IF NEW.id IS DISTINCT FROM OLD.id
       OR NEW.workflow_id IS DISTINCT FROM OLD.workflow_id
       OR NEW.workflow_version_id IS DISTINCT FROM OLD.workflow_version_id
       OR NEW.input IS DISTINCT FROM OLD.input
       OR NEW.created_at IS DISTINCT FROM OLD.created_at
       OR NEW.legacy_values IS DISTINCT FROM OLD.legacy_values
       OR NEW.max_attempts IS DISTINCT FROM OLD.max_attempts
       OR NEW.timeout_ms IS DISTINCT FROM OLD.timeout_ms
       OR (NEW.output IS DISTINCT FROM OLD.output AND NEW.status <> 'COMPLETED')
       OR (NEW.error IS DISTINCT FROM OLD.error AND NEW.status NOT IN ('FAILED', 'CANCELLED'))
       OR (NEW.claim_token IS DISTINCT FROM OLD.claim_token AND NEW.status NOT IN ('RUNNING', 'PENDING'))
       OR (NEW.last_error IS DISTINCT FROM OLD.last_error AND NEW.status <> 'PENDING')
       OR (NEW.next_attempt_at IS DISTINCT FROM OLD.next_attempt_at AND NEW.status NOT IN ('PENDING', 'RUNNING')) THEN
        RAISE EXCEPTION 'execution % column cannot change in transition % -> %', OLD.id, OLD.status, NEW.status
            USING ERRCODE = 'check_violation', CONSTRAINT = 'lifecycle_immutable_column';
    END IF;

    SELECT EXISTS (SELECT 1 FROM execution_cancel_requests WHERE execution_id = OLD.id) INTO cancel_requested;

    IF NEW.status = 'RUNNING' THEN
        IF NEW.claim_token IS NULL THEN
            RAISE EXCEPTION 'claiming execution % requires a claim token', OLD.id
                USING ERRCODE = 'check_violation', CONSTRAINT = 'lifecycle_claim_requires_token';
        END IF;
        IF OLD.attempt >= OLD.max_attempts THEN
            RAISE EXCEPTION 'execution % has no attempts left (% of %)', OLD.id, OLD.attempt, OLD.max_attempts
                USING ERRCODE = 'check_violation', CONSTRAINT = 'lifecycle_attempts_exhausted';
        END IF;
        IF OLD.next_attempt_at IS NOT NULL AND OLD.next_attempt_at > now_ts AND NOT cancel_requested THEN
            RAISE EXCEPTION 'execution % retry is not due before %', OLD.id, OLD.next_attempt_at
                USING ERRCODE = 'check_violation', CONSTRAINT = 'lifecycle_retry_not_due';
        END IF;
        NEW.attempt := OLD.attempt + 1;
        NEW.next_attempt_at := NULL;
        NEW.last_error := OLD.last_error;
        -- The execution deadline starts with the first claim and never moves.
        NEW.deadline_at := COALESCE(OLD.deadline_at,
            CASE WHEN OLD.timeout_ms IS NULL THEN NULL ELSE now_ts + OLD.timeout_ms * interval '1 millisecond' END);
    ELSIF NEW.status = 'PENDING' THEN
        IF NEW.next_attempt_at IS NULL OR NEW.last_error IS NULL THEN
            RAISE EXCEPTION 'scheduling a retry of execution % requires next_attempt_at and last_error', OLD.id
                USING ERRCODE = 'check_violation', CONSTRAINT = 'lifecycle_retry_requires_schedule';
        END IF;
        IF cancel_requested THEN
            RAISE EXCEPTION 'execution % has a cancellation request and cannot be retried', OLD.id
                USING ERRCODE = 'check_violation', CONSTRAINT = 'lifecycle_retry_cancel_requested';
        END IF;
        IF OLD.attempt >= OLD.max_attempts THEN
            RAISE EXCEPTION 'execution % has no attempts left (% of %)', OLD.id, OLD.attempt, OLD.max_attempts
                USING ERRCODE = 'check_violation', CONSTRAINT = 'lifecycle_attempts_exhausted';
        END IF;
        IF OLD.deadline_at IS NOT NULL AND NEW.next_attempt_at >= OLD.deadline_at THEN
            RAISE EXCEPTION 'execution % retry at % would start after its deadline %', OLD.id, NEW.next_attempt_at, OLD.deadline_at
                USING ERRCODE = 'check_violation', CONSTRAINT = 'lifecycle_retry_after_deadline';
        END IF;
        NEW.attempt := OLD.attempt;
        NEW.claim_token := NULL;
        NEW.deadline_at := OLD.deadline_at;
    ELSE
        NEW.attempt := OLD.attempt;
        NEW.next_attempt_at := NULL;
        NEW.deadline_at := OLD.deadline_at;
    END IF;

    IF NEW.status IN ('COMPLETED', 'FAILED', 'CANCELLED', 'PENDING') AND EXISTS (
        SELECT 1 FROM node_executions WHERE execution_id = OLD.id AND status = 'RUNNING') THEN
        RAISE EXCEPTION 'execution % still has RUNNING node executions', OLD.id
            USING ERRCODE = 'check_violation', CONSTRAINT = 'lifecycle_running_nodes';
    END IF;
    NEW.started_at := CASE WHEN NEW.status = 'RUNNING' THEN now_ts
                           WHEN NEW.status = 'PENDING' THEN NULL
                           ELSE OLD.started_at END;
    NEW.finished_at := CASE WHEN NEW.status IN ('COMPLETED', 'FAILED', 'CANCELLED')
                            THEN GREATEST(now_ts, OLD.started_at) ELSE NULL END;
    NEW.updated_at := GREATEST(now_ts, OLD.updated_at);
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
    INSERT INTO execution_status_history (id, execution_id, from_status, to_status, created_at, metadata, attempt)
    VALUES (COALESCE(tid::uuid, gen_random_uuid()), NEW.id,
            CASE WHEN TG_OP = 'INSERT' THEN NULL ELSE OLD.status END,
            NEW.status, clock_timestamp(), COALESCE(meta::jsonb, '{}'::jsonb), NEW.attempt);
    RETURN NULL;
END $$;

-- Derived bookkeeping follows every lifecycle change in the same transaction:
-- a lease exists only while RUNNING, a dispatch row only while PENDING.
CREATE FUNCTION lifecycle_execution_after_change() RETURNS trigger
LANGUAGE plpgsql SET search_path FROM CURRENT AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        -- The creator dispatches a new execution itself.
        INSERT INTO execution_dispatch (execution_id, attempt, dispatched_at)
        VALUES (NEW.id, NEW.attempt, clock_timestamp());
        RETURN NULL;
    END IF;
    IF NEW.status IS NOT DISTINCT FROM OLD.status THEN
        RETURN NULL;
    END IF;
    IF NEW.status <> 'RUNNING' THEN
        DELETE FROM execution_leases WHERE execution_id = NEW.id;
    END IF;
    IF NEW.status = 'PENDING' THEN
        INSERT INTO execution_dispatch (execution_id, attempt, dispatched_at)
        VALUES (NEW.id, NEW.attempt, NULL)
        ON CONFLICT (execution_id) DO UPDATE
            SET attempt = EXCLUDED.attempt, dispatched_at = NULL;
    ELSE
        DELETE FROM execution_dispatch WHERE execution_id = NEW.id;
    END IF;
    RETURN NULL;
END $$;

CREATE TRIGGER executions_lifecycle_after_change
    AFTER INSERT OR UPDATE ON executions
    FOR EACH ROW EXECUTE FUNCTION lifecycle_execution_after_change();

-- A lease belongs to exactly the execution's current claim and attempt; its
-- identity never changes (heartbeats only move expires_at).
CREATE FUNCTION lifecycle_lease_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path FROM CURRENT AS $$
BEGIN
    IF TG_OP = 'UPDATE' AND (NEW.execution_id IS DISTINCT FROM OLD.execution_id
        OR NEW.claim_token IS DISTINCT FROM OLD.claim_token OR NEW.attempt IS DISTINCT FROM OLD.attempt
        OR NEW.owner IS DISTINCT FROM OLD.owner OR NEW.acquired_at IS DISTINCT FROM OLD.acquired_at) THEN
        RAISE EXCEPTION 'lease identity of execution % is immutable', OLD.execution_id
            USING ERRCODE = 'check_violation', CONSTRAINT = 'lifecycle_lease_immutable';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM executions WHERE id = NEW.execution_id AND status = 'RUNNING'
                     AND claim_token = NEW.claim_token AND attempt = NEW.attempt) THEN
        RAISE EXCEPTION 'lease for execution % does not match its current RUNNING claim', NEW.execution_id
            USING ERRCODE = 'check_violation', CONSTRAINT = 'lifecycle_lease_not_owner';
    END IF;
    IF TG_OP = 'INSERT' THEN
        NEW.acquired_at := clock_timestamp();
    END IF;
    NEW.heartbeat_at := clock_timestamp();
    RETURN NEW;
END $$;

CREATE TRIGGER execution_leases_guard
    BEFORE INSERT OR UPDATE ON execution_leases
    FOR EACH ROW EXECUTE FUNCTION lifecycle_lease_guard();

-- Dead letters are written with the FAILED transition of the attempt they
-- describe and never change afterwards.
CREATE FUNCTION lifecycle_dead_letter_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path FROM CURRENT AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        IF NOT EXISTS (SELECT 1 FROM executions WHERE id = NEW.execution_id AND status = 'FAILED'
                         AND attempt = NEW.attempt) THEN
            RAISE EXCEPTION 'dead letter for execution % requires it to be FAILED at attempt %', NEW.execution_id, NEW.attempt
                USING ERRCODE = 'check_violation', CONSTRAINT = 'lifecycle_dead_letter_requires_failed';
        END IF;
        NEW.created_at := clock_timestamp();
        RETURN NEW;
    END IF;
    RAISE EXCEPTION 'execution_dead_letters is append-only'
        USING ERRCODE = 'insufficient_privilege', CONSTRAINT = 'lifecycle_dead_letter_append_only';
END $$;

CREATE TRIGGER execution_dead_letters_guard
    BEFORE INSERT OR UPDATE OR DELETE ON execution_dead_letters
    FOR EACH ROW EXECUTE FUNCTION lifecycle_dead_letter_guard();

-- Node records are fenced by the execution attempt that writes them: a worker
-- whose attempt was recovered by another cannot write into the new attempt.
CREATE FUNCTION lifecycle_require_current_attempt(execution UUID, writer_attempt INTEGER) RETURNS INTEGER
LANGUAGE plpgsql SET search_path FROM CURRENT AS $$
DECLARE
    parent_status TEXT;
    parent_attempt INTEGER;
BEGIN
    SELECT status, attempt INTO parent_status, parent_attempt FROM executions WHERE id = execution FOR SHARE;
    IF NOT FOUND THEN
        RETURN writer_attempt; -- the foreign key reports the missing execution
    END IF;
    IF parent_status <> 'RUNNING' THEN
        RAISE EXCEPTION 'execution % is % and does not accept node records', execution, parent_status
            USING ERRCODE = 'check_violation', CONSTRAINT = 'lifecycle_parent_not_running';
    END IF;
    IF writer_attempt IS NOT NULL AND writer_attempt <> parent_attempt THEN
        RAISE EXCEPTION 'node write for attempt % of execution % is stale (current attempt %)', writer_attempt, execution, parent_attempt
            USING ERRCODE = 'check_violation', CONSTRAINT = 'lifecycle_stale_attempt';
    END IF;
    RETURN parent_attempt;
END $$;

CREATE OR REPLACE FUNCTION lifecycle_node_before_insert() RETURNS trigger
LANGUAGE plpgsql SET search_path FROM CURRENT AS $$
BEGIN
    NEW.execution_attempt := lifecycle_require_current_attempt(NEW.execution_id, NEW.execution_attempt);
    IF NEW.status IS DISTINCT FROM 'PENDING' OR NEW.started_at IS NOT NULL OR NEW.finished_at IS NOT NULL
       OR NEW.output IS NOT NULL OR NEW.error IS NOT NULL OR NEW.legacy_values IS NOT NULL
       OR NEW.output_values IS NOT NULL THEN
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
    PERFORM lifecycle_require_current_attempt(OLD.execution_id, OLD.execution_attempt);
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
       OR NEW.execution_attempt IS DISTINCT FROM OLD.execution_attempt
       OR NEW.created_at IS DISTINCT FROM OLD.created_at
       OR NEW.legacy_values IS DISTINCT FROM OLD.legacy_values
       OR (NEW.input IS DISTINCT FROM OLD.input AND NEW.status <> 'RUNNING')
       OR (NEW.output IS DISTINCT FROM OLD.output AND NEW.status <> 'COMPLETED')
       OR (NEW.output_values IS DISTINCT FROM OLD.output_values AND NEW.status <> 'COMPLETED')
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
