CREATE TABLE automation_outbox (
 event_id TEXT PRIMARY KEY,
 body JSONB NOT NULL,
 pending BOOLEAN NOT NULL DEFAULT true,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX automation_outbox_pending ON automation_outbox(created_at) WHERE pending;
