"use client";

// Server state of the Templates page (Phase K): the workspace's templates
// (saved ones first), the node catalog (to tell which apps and keys each
// needs), and the accounts and keys the workspace already has, so "Before
// you start" can tick off what is ready.

import { useCallback, useEffect, useMemo, useState } from "react";
import { authApi, connectedAccountApi, credentialApi, nodeApi, projectApi, workflowApi } from "@/lib/api";
import { assistantApi } from "@/lib/api/assistant";
import { templateSummary, workflowSummary } from "@/lib/automation/workflowSummary";
import type { ConnectedAccount, Credential, NodeDefinition, Workflow, WorkflowTemplate, WorkspaceRef } from "@/types/api";

export function useTemplateGallery() {
  const [workspaces, setWorkspaces] = useState<WorkspaceRef[]>([]);
  const [workspaceId, setWorkspaceId] = useState<string | null>(null);
  const [templates, setTemplates] = useState<WorkflowTemplate[] | null>(null);
  const [catalogList, setCatalogList] = useState<NodeDefinition[]>([]);
  const [accounts, setAccounts] = useState<ConnectedAccount[] | null>(null);
  const [credentials, setCredentials] = useState<Credential[] | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [version, setVersion] = useState(0);

  useEffect(() => {
    Promise.all([authApi.me(), nodeApi.list()])
      .then(([me, nodes]) => {
        setWorkspaces(me.workspaces);
        setWorkspaceId((current) => current ?? me.workspaces[0]?.id ?? null);
        setCatalogList(nodes.items);
        if (me.workspaces.length === 0) setTemplates([]);
      })
      .catch(setError);
  }, []);

  useEffect(() => {
    if (!workspaceId) return;
    let cancelled = false;
    Promise.all([workflowApi.templates(workspaceId), assistantApi.templates(workspaceId)])
      .then(([legacy, modern]) => {
        if (cancelled) return;
        const native = new Map(modern.items.map(item => [item.id, templateSummary(item)]));
        const merged = legacy.items.map(item => native.get(item.id) ?? item);
        for (const item of native.values()) if (!merged.some(existing => existing.id === item.id)) merged.push(item);
        setTemplates(merged);
      })
      .catch((e) => !cancelled && setError(e));
    // What the workspace already has; the gallery works without them.
    connectedAccountApi.list(workspaceId).then((l) => !cancelled && setAccounts(l.items)).catch(() => !cancelled && setAccounts([]));
    credentialApi.list(workspaceId).then((l) => !cancelled && setCredentials(l.items)).catch(() => !cancelled && setCredentials([]));
    return () => {
      cancelled = true;
    };
  }, [workspaceId, version]);

  const catalog = useMemo(() => new Map(catalogList.map((d) => [d.type, d])), [catalogList]);
  const workspace = workspaces.find((w) => w.id === workspaceId) ?? null;
  const canCreate = !!workspace && workspace.role !== "viewer";

  /** Creates a workflow from the template (in the first project, created
   * when the workspace has none). */
  const use = useCallback(async (template: WorkflowTemplate, name: string): Promise<Workflow> => {
    if (!workspaceId) throw new Error("No workspace");
    let pid = (await projectApi.list(workspaceId)).items[0]?.id;
    if (!pid) pid = (await projectApi.create(workspaceId, "Default")).id;
    if (template.automation) return workflowSummary((await assistantApi.createWorkflow({ workspace_id: workspaceId, project_id: pid, name, template_id: template.id })).workflow);
    return workflowApi.createFromTemplate(pid, name, template.id);
  }, [workspaceId]);

  const remove = useCallback(async (template: WorkflowTemplate) => {
    if (template.automation) await assistantApi.deleteTemplate(template.id);
    else await workflowApi.deleteTemplate(template.id);
    setVersion((v) => v + 1);
  }, []);

  const have = accounts && credentials ? { accounts, credentials } : null;
  return { workspaces, workspace, setWorkspaceId, templates, catalog, have, error, canCreate, use, remove };
}
