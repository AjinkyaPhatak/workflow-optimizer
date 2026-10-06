-- Phase 10 remediation: node records are fenced by the claim that wrote them,
-- and remember the side effects of the node they describe.
--
--   * node_executions.claim_token is the claim (executions.claim_token) under
--     which the record was written. A writer that supplies a claim token may
--     only write while that exact claim owns the RUNNING execution; a record
--     may only change while the claim that wrote it still owns the execution.
--     A worker whose attempt was recovered (and possibly re-claimed) can no
--     longer write anything, even when it believes it runs the current
--     attempt. Writers without a token (Phase 8 callers, raw SQL) stay
--     unfenced and are stamped with the current claim.
--   * node_executions.side_effects records the node definition's side-effect
--     declaration ('none', 'idempotent', 'unsafe') when the record is
--     written, so recovery decides from persisted state whether a node that
--     was interrupted may be run again. NULL (records written before this
--     migration) means unknown and is treated like 'unsafe'.
--
-- If a previous 000004 DOWN archived these columns, they are restored here.

ALTER TABLE node_executions
    ADD COLUMN claim_token UUID,
    ADD COLUMN side_effects VARCHAR(16)
        CONSTRAINT node_executions_side_effects_check CHECK (side_effects IN ('none', 'idempotent', 'unsafe'));

ALTER TABLE node_executions DISABLE TRIGGER USER;
-- Records of the current claim belong to it; older records are terminal.
UPDATE node_executions n SET claim_token = e.claim_token
  FROM executions e
 WHERE e.id = n.execution_id AND e.status = 'RUNNING' AND n.execution_attempt = e.attempt;
DO $$
BEGIN
    IF to_regclass('phase10_rollback_node_claims') IS NOT NULL THEN
        UPDATE node_executions n SET claim_token = a.claim_token, side_effects = a.side_effects
          FROM phase10_rollback_node_claims a
         WHERE a.id = n.id;
        DROP TABLE phase10_rollback_node_claims;
    END IF;
END $$;
ALTER TABLE node_executions ENABLE TRIGGER USER;

-- The reaper's deadline query.
CREATE INDEX executions_running_deadline_idx
    ON executions (deadline_at) WHERE status = 'RUNNING' AND deadline_at IS NOT NULL;

-- The current claim of a RUNNING execution, enforced for node writers.
CREATE FUNCTION lifecycle_require_current_claim(execution UUID, writer_token UUID, writer_attempt INTEGER,
                                                OUT current_attempt INTEGER, OUT current_token UUID)
LANGUAGE plpgsql SET search_path FROM CURRENT AS $$
DECLARE
    parent_status TEXT;
BEGIN
    SELECT status, attempt, claim_token INTO parent_status, current_attempt, current_token
      FROM executions WHERE id = execution FOR SHARE;
    IF NOT FOUND THEN
        current_attempt := COALESCE(writer_attempt, 1); -- the foreign key reports the missing execution
        current_token := writer_token;
        RETURN;
    END IF;
    IF parent_status <> 'RUNNING' THEN
        RAISE EXCEPTION 'execution % is % and does not accept node records', execution, parent_status
            USING ERRCODE = 'check_violation', CONSTRAINT = 'lifecycle_parent_not_running';
    END IF;
    IF writer_token IS NOT NULL AND writer_token IS DISTINCT FROM current_token THEN
        RAISE EXCEPTION 'node write for execution % comes from a claim that no longer owns it', execution
            USING ERRCODE = 'check_violation', CONSTRAINT = 'lifecycle_stale_owner';
    END IF;
    IF writer_attempt IS NOT NULL AND writer_attempt <> current_attempt THEN
        RAISE EXCEPTION 'node write for attempt % of execution % is stale (current attempt %)', writer_attempt, execution, current_attempt
            USING ERRCODE = 'check_violation', CONSTRAINT = 'lifecycle_stale_attempt';
    END IF;
END $$;

CREATE OR REPLACE FUNCTION lifecycle_node_before_insert() RETURNS trigger
LANGUAGE plpgsql SET search_path FROM CURRENT AS $$
DECLARE
    claim RECORD;
BEGIN
    SELECT * INTO claim FROM lifecycle_require_current_claim(NEW.execution_id, NEW.claim_token, NEW.execution_attempt);
    NEW.execution_attempt := claim.current_attempt;
    NEW.claim_token := COALESCE(NEW.claim_token, claim.current_token);
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
    -- A record changes only while the claim that wrote it owns the execution.
    PERFORM lifecycle_require_current_claim(OLD.execution_id, OLD.claim_token, OLD.execution_attempt);
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
       OR NEW.claim_token IS DISTINCT FROM OLD.claim_token
       OR NEW.side_effects IS DISTINCT FROM OLD.side_effects
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

DROP FUNCTION lifecycle_require_current_attempt(UUID, INTEGER);
