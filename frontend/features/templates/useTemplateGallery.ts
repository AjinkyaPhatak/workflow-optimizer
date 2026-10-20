"use client";

// Server state of the Templates page (Phase K): the workspace's templates
// (saved ones first), the node catalog (to tell which apps and keys each
// needs), and the accounts and keys the workspace already has, so "Before
// you start" can tick off what is ready.

import { useCallback, useEffect, useMemo, useState } from "react";
import { authApi, connectedAccountApi, credentialApi, nodeApi, projectApi, workflowApi } from "@/lib/api";
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
    workflowApi.templates(workspaceId)
      .then((l) => !cancelled && setTemplates(l.items))
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
    return workflowApi.createFromTemplate(pid, name, template.id);
  }, [workspaceId]);

  const remove = useCallback(async (template: WorkflowTemplate) => {
    await workflowApi.deleteTemplate(template.id);
    setVersion((v) => v + 1);
  }, []);

  const have = accounts && credentials ? { accounts, credentials } : null;
  return { workspaces, workspace, setWorkspaceId, templates, catalog, have, error, canCreate, use, remove };
}
