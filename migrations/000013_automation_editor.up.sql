CREATE TABLE automation_workflows (
 id UUID PRIMARY KEY REFERENCES assistant_sessions(id),
 workspace_id UUID NOT NULL REFERENCES workspaces(id),
 project_id UUID REFERENCES projects(id),
 name TEXT NOT NULL CHECK (name <> ''),
 active_version_id UUID,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX automation_workflows_workspace ON automation_workflows(workspace_id,updated_at DESC);
INSERT INTO automation_workflows(id,workspace_id,name,created_at,updated_at)
 SELECT id,workspace_id,COALESCE(NULLIF(body->'compilation'->'plan'->>'name',''),'New workflow'),created_at,updated_at FROM assistant_sessions;

CREATE TABLE automation_workflow_versions (
 id UUID PRIMARY KEY,
 workflow_id UUID NOT NULL REFERENCES automation_workflows(id),
 version_number INT NOT NULL,
 revision INT NOT NULL,
 body JSONB NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 UNIQUE(workflow_id,version_number),
 UNIQUE(workflow_id,revision)
);
ALTER TABLE automation_workflows ADD CONSTRAINT automation_workflows_active_version_fk FOREIGN KEY(active_version_id) REFERENCES automation_workflow_versions(id);
CREATE FUNCTION automation_version_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 RAISE EXCEPTION 'Published automation versions are immutable' USING ERRCODE='check_violation';
END $$;
CREATE TRIGGER automation_workflow_versions_immutable BEFORE UPDATE OR DELETE ON automation_workflow_versions FOR EACH ROW EXECUTE FUNCTION automation_version_immutable();

CREATE TABLE automation_editor_layout (
 workflow_id UUID PRIMARY KEY REFERENCES automation_workflows(id),
 revision INT NOT NULL DEFAULT 1,
 positions JSONB NOT NULL DEFAULT '{}'
);
CREATE TABLE automation_workflow_templates (
 id UUID PRIMARY KEY,
 workspace_id UUID NOT NULL REFERENCES workspaces(id),
 body JSONB NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX automation_workflow_templates_workspace ON automation_workflow_templates(workspace_id,created_at DESC);
