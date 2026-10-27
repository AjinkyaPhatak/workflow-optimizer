"use client";

// Server state for the dashboard: the user's workspaces, the selected
// workspace's projects, and their workflows (all read through the API).

import { useCallback, useEffect, useState } from "react";
import { authApi, projectApi, workflowApi } from "@/lib/api";
import { assistantApi } from "@/lib/api/assistant";
import { workflowSummary } from "@/lib/automation/workflowSummary";
import type { Project, Workflow, WorkspaceRef } from "@/types/api";

export interface WorkflowRow {
  workflow: Workflow;
  project: Project;
}

async function loadWorkspace(workspaceId: string): Promise<{ ps: Project[]; rs: WorkflowRow[] }> {
  const ps = (await projectApi.list(workspaceId)).items;
  const [lists, modern] = await Promise.all([Promise.all(ps.map((p) => workflowApi.list(p.id))), assistantApi.workflows(workspaceId)]);
  const rs = lists.flatMap((l, i) => l.items.map((workflow) => ({ workflow, project: ps[i] })));
  for (const item of modern.items) {
    const workflow = workflowSummary(item);
    const project = ps.find(p => p.id === workflow.project_id) ?? { id: "", workspace_id: workspaceId, name: "Workspace", description: null, created_at: item.created_at, updated_at: item.updated_at };
    rs.push({ workflow, project });
  }
  return { ps, rs: rs.sort((a, b) => b.workflow.updated_at.localeCompare(a.workflow.updated_at)) };
}

export function useWorkflowDashboard() {
  const [workspaces, setWorkspaces] = useState<WorkspaceRef[]>([]);
  const [workspaceId, setWorkspaceId] = useState<string | null>(null);
  const [projects, setProjects] = useState<Project[]>([]);
  const [rows, setRows] = useState<WorkflowRow[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<unknown>(null);

  useEffect(() => {
    authApi
      .me()
      .then((me) => {
        setWorkspaces(me.workspaces);
        setWorkspaceId((current) => current ?? me.workspaces[0]?.id ?? null);
        if (me.workspaces.length === 0) setLoading(false);
      })
      .catch((e) => {
        setError(e);
        setLoading(false);
      });
  }, []);

  const [version, setVersion] = useState(0);
  const reload = useCallback(() => setVersion((v) => v + 1), []);

  useEffect(() => {
    if (!workspaceId) return;
    let cancelled = false;
    loadWorkspace(workspaceId)
      .then(({ ps, rs }) => {
        if (cancelled) return;
        setProjects(ps);
        setRows(rs);
        setError(null);
      })
      .catch((e) => !cancelled && setError(e))
      .finally(() => !cancelled && setLoading(false));
    return () => {
      cancelled = true;
    };
  }, [workspaceId, version]);

  const workspace = workspaces.find((w) => w.id === workspaceId) ?? null;

  /** Creates the workflow (metadata only), creating a first project if the
   * workspace has none. */
  const createWorkflow = useCallback(
    async (name: string, projectId?: string, templateId?: string) => {
      if (!workspaceId) throw new Error("No workspace");
      let pid = projectId ?? projects[0]?.id;
      if (!pid) pid = (await projectApi.create(workspaceId, "Default")).id;
      const created = await assistantApi.createWorkflow({ workspace_id: workspaceId, project_id: pid, name, ...(templateId ? { template_id: templateId } : {}) });
      const wf = workflowSummary(created.workflow);
      reload();
      return wf;
    },
    [workspaceId, projects, reload],
  );

  return { workspaces, workspace, setWorkspaceId, projects, rows, loading, error, reload, createWorkflow };
}
