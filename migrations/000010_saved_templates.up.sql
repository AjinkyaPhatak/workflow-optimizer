-- Phase K: templates people save from their own workflows ("Save as
-- template"). A saved template is a copy of a workflow's definition at the
-- time it was saved, shared with the workspace's members; using one creates
-- a new workflow with fresh node IDs, like a built-in template.
CREATE TABLE saved_templates (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspaces(id),
    name VARCHAR(255) NOT NULL CHECK (name <> ''),
    description TEXT NOT NULL DEFAULT '',
    definition JSONB NOT NULL,
    created_by UUID NOT NULL REFERENCES users(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX saved_templates_workspace_idx ON saved_templates(workspace_id, created_at DESC);
