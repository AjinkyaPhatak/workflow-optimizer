DROP TABLE IF EXISTS automation_workflow_templates;
DROP TABLE IF EXISTS automation_editor_layout;
ALTER TABLE automation_workflows DROP CONSTRAINT IF EXISTS automation_workflows_active_version_fk;
DROP TABLE IF EXISTS automation_workflow_versions;
DROP FUNCTION IF EXISTS automation_version_immutable();
DROP TABLE IF EXISTS automation_workflows;
