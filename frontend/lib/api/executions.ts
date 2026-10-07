import type { EventPage, ExecutionAccepted, ExecutionDetails, List, NodeExecutionDetails, Page, Execution } from "@/types/api";
import { request } from "./client";

export const executionApi = {
  /** Starts an execution of a published version (the active one when versionId is omitted). */
  execute: (workflowId: string, input: Record<string, unknown>, versionId?: string) =>
    request<ExecutionAccepted>("POST", `/workflows/${workflowId}/execute`, {
      body: versionId ? { version_id: versionId, input } : { input },
    }),
  get: (id: string) => request<ExecutionDetails>("GET", `/executions/${id}`),
  nodes: (id: string) => request<List<NodeExecutionDetails>>("GET", `/executions/${id}/nodes`),
  events: (id: string, page = 1, pageSize = 100) =>
    request<EventPage>("GET", `/executions/${id}/events`, { query: { page, page_size: pageSize } }),
  /** A workflow's executions, newest first. */
  listByWorkflow: (workflowId: string, page = 1, pageSize = 20) =>
    request<Page<Execution>>("GET", `/workflows/${workflowId}/executions`, { query: { page, page_size: pageSize } }),
  cancel: (id: string) => request<Execution>("POST", `/executions/${id}/cancel`),
};
