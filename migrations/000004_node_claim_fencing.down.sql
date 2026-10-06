-- Roll back to the 000003 node fencing (by execution attempt only). The claim
-- tokens and side-effect declarations of node records are archived in
-- phase10_rollback_node_claims (without foreign keys); a later 000004 UP
-- restores them.

CREATE TABLE IF NOT EXISTS phase10_rollback_node_claims (
    id UUID PRIMARY KEY, claim_token UUID, side_effects VARCHAR(16));
INSERT INTO phase10_rollback_node_claims (id, claim_token, side_effects)
SELECT id, claim_token, side_effects FROM node_executions
ON CONFLICT (id) DO UPDATE SET claim_token = EXCLUDED.claim_token, side_effects = EXCLUDED.side_effects;

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

DROP FUNCTION lifecycle_require_current_claim(UUID, UUID, INTEGER);
DROP INDEX IF EXISTS executions_running_deadline_idx;
ALTER TABLE node_executions DROP COLUMN side_effects;
ALTER TABLE node_executions DROP COLUMN claim_token;
