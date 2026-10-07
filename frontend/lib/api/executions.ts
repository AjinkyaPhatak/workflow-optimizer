import type { Execution, ExecutionAccepted, List, NodeExecution } from "@/types/api";
import { request } from "./client";

export const executionApi = {
  /** Starts an execution of a published version (the active one when versionId is omitted). */
  execute: (workflowId: string, input: Record<string, unknown>, versionId?: string) =>
    request<ExecutionAccepted>("POST", `/workflows/${workflowId}/execute`, {
      body: versionId ? { version_id: versionId, input } : { input },
    }),
  get: (id: string) => request<Execution>("GET", `/executions/${id}`),
  nodes: (id: string) => request<List<NodeExecution>>("GET", `/executions/${id}/nodes`),
  cancel: (id: string) => request<Execution>("POST", `/executions/${id}/cancel`),
};
