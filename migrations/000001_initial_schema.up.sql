CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE users (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email VARCHAR(255) NOT NULL UNIQUE,
    name VARCHAR(255) NOT NULL,
    password_hash TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE workspaces (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name VARCHAR(255) NOT NULL,
    owner_id UUID NOT NULL REFERENCES users(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE workspace_members (
    workspace_id UUID NOT NULL REFERENCES workspaces(id),
    user_id UUID NOT NULL REFERENCES users(id),
    role VARCHAR(32) NOT NULL CHECK (role IN ('owner', 'admin', 'member', 'viewer')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, user_id)
);

CREATE INDEX workspace_members_user_id_idx ON workspace_members(user_id);

CREATE TABLE projects (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspaces(id),
    name VARCHAR(255) NOT NULL,
    description TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX projects_workspace_id_idx ON projects(workspace_id);

CREATE TABLE workflows (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id UUID NOT NULL REFERENCES projects(id),
    name VARCHAR(255) NOT NULL,
    description TEXT,
    active_version_id UUID,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX workflows_project_id_idx ON workflows(project_id);

CREATE TABLE workflow_versions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workflow_id UUID NOT NULL REFERENCES workflows(id),
    version_number INTEGER NOT NULL CHECK (version_number > 0),
    definition JSONB NOT NULL,
    status VARCHAR(32) NOT NULL CHECK (status IN ('DRAFT', 'PUBLISHED', 'ARCHIVED')),
    created_by UUID REFERENCES users(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    published_at TIMESTAMPTZ,
    UNIQUE (workflow_id, version_number)
);

CREATE INDEX workflow_versions_workflow_id_idx ON workflow_versions(workflow_id);

ALTER TABLE workflows
    ADD CONSTRAINT workflows_active_version_id_fkey
    FOREIGN KEY (active_version_id) REFERENCES workflow_versions(id);

CREATE TABLE credentials (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspaces(id),
    name VARCHAR(255) NOT NULL,
    provider VARCHAR(255) NOT NULL,
    credential_type VARCHAR(255) NOT NULL,
    encrypted_data JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX credentials_workspace_id_idx ON credentials(workspace_id);
CREATE INDEX credentials_workspace_provider_idx ON credentials(workspace_id, provider);

CREATE TABLE executions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workflow_id UUID NOT NULL REFERENCES workflows(id),
    workflow_version_id UUID NOT NULL REFERENCES workflow_versions(id),
    status VARCHAR(32) NOT NULL CHECK (status IN ('PENDING', 'RUNNING', 'COMPLETED', 'FAILED', 'CANCELLED')),
    input JSONB,
    output JSONB,
    error JSONB,
    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX executions_workflow_id_idx ON executions(workflow_id);
CREATE INDEX executions_workflow_version_id_idx ON executions(workflow_version_id);
CREATE INDEX executions_status_idx ON executions(status);
CREATE INDEX executions_created_at_idx ON executions(created_at);

CREATE TABLE node_executions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    execution_id UUID NOT NULL REFERENCES executions(id),
    node_id VARCHAR(255) NOT NULL,
    node_type VARCHAR(255) NOT NULL,
    status VARCHAR(32) NOT NULL CHECK (status IN ('PENDING', 'RUNNING', 'COMPLETED', 'FAILED', 'CANCELLED')),
    input JSONB,
    output JSONB,
    error JSONB,
    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    duration_ms BIGINT,
    attempt INTEGER NOT NULL DEFAULT 1 CHECK (attempt > 0),
    provider VARCHAR(255),
    model VARCHAR(255),
    input_tokens BIGINT,
    output_tokens BIGINT,
    total_tokens BIGINT,
    estimated_cost NUMERIC(18, 8),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX node_executions_execution_id_idx ON node_executions(execution_id);
CREATE INDEX node_executions_execution_node_id_idx ON node_executions(execution_id, node_id);
CREATE INDEX node_executions_status_idx ON node_executions(status);
