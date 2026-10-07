-- Phase 14: append-only execution events (observability).
--
-- Events describe what happened; they never drive execution. The execution
-- row, its status history and node_executions remain the source of truth.
-- The timestamp is taken from the database clock when the event is appended
-- (one clock for workers, API and reaper), and seq breaks ties, so the order
-- (timestamp, seq) is deterministic.
CREATE TABLE execution_events (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    seq BIGINT GENERATED ALWAYS AS IDENTITY,
    execution_id UUID NOT NULL REFERENCES executions(id),
    node_id VARCHAR(255),
    type VARCHAR(64) NOT NULL CHECK (type IN (
        'EXECUTION_STARTED', 'EXECUTION_COMPLETED', 'EXECUTION_FAILED', 'EXECUTION_CANCELLED',
        'NODE_STARTED', 'NODE_COMPLETED', 'NODE_FAILED', 'NODE_SKIPPED',
        'RETRY_SCHEDULED', 'RETRY_STARTED')),
    "timestamp" TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    data JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(data) = 'object'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX execution_events_execution_time_idx ON execution_events(execution_id, "timestamp", seq);
CREATE INDEX execution_events_execution_id_idx ON execution_events(execution_id, id);

-- Append-only: rows are never changed or removed.
CREATE FUNCTION execution_events_append_only() RETURNS trigger
LANGUAGE plpgsql SET search_path FROM CURRENT AS $$
BEGIN
    RAISE EXCEPTION 'execution_events is append-only'
        USING ERRCODE = 'insufficient_privilege', CONSTRAINT = 'execution_events_append_only';
END $$;

CREATE TRIGGER execution_events_no_change
    BEFORE UPDATE OR DELETE ON execution_events
    FOR EACH ROW EXECUTE FUNCTION execution_events_append_only();
CREATE TRIGGER execution_events_no_truncate
    BEFORE TRUNCATE ON execution_events
    FOR EACH STATEMENT EXECUTE FUNCTION execution_events_append_only();
