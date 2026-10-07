import type { List, Page, ValidationResult, Version, VersionSummary, Workflow, WorkflowDefinition, WorkflowFromTemplate, WorkflowTemplate } from "@/types/api";
import { request } from "./client";

export const workflowApi = {
  list: (projectId: string, page = 1, pageSize = 100) =>
    request<Page<Workflow>>("GET", "/workflows", { query: { project_id: projectId, page, page_size: pageSize } }),
  get: (id: string) => request<Workflow>("GET", `/workflows/${id}`),
  create: (projectId: string, name: string, description?: string) =>
    request<Workflow>("POST", "/workflows", { body: { project_id: projectId, name, description } }),
  /** Creates the workflow with a first DRAFT copied from a template (the
   * backend instantiates it with fresh node IDs). */
  createFromTemplate: (projectId: string, name: string, templateId: string) =>
    request<WorkflowFromTemplate>("POST", "/workflows", { body: { project_id: projectId, name, template_id: templateId } }),
  /** Workflow templates: ready-made workflow definitions. */
  templates: () => request<List<WorkflowTemplate>>("GET", "/templates"),
  update: (id: string, patch: { name?: string; description?: string }) =>
    request<Workflow>("PATCH", `/workflows/${id}`, { body: patch }),
  remove: (id: string) => request<void>("DELETE", `/workflows/${id}`),

  /** Version metadata, newest first. */
  listVersions: (id: string, page = 1, pageSize = 20) =>
    request<Page<VersionSummary>>("GET", `/workflows/${id}/versions`, { query: { page, page_size: pageSize } }),
  getVersion: (id: string, versionId: string) => request<Version>("GET", `/workflows/${id}/versions/${versionId}`),
  /** Stores the definition as a new immutable DRAFT version (the API has no version update). */
  createVersion: (id: string, definition: WorkflowDefinition) =>
    request<Version>("POST", `/workflows/${id}/versions`, { body: { definition } }),
  validate: (id: string, versionId: string) =>
    request<ValidationResult>("POST", `/workflows/${id}/versions/${versionId}/validate`),
  publish: (id: string, versionId: string) =>
    request<VersionSummary>("POST", `/workflows/${id}/versions/${versionId}/publish`),
};
