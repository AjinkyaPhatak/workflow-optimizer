"use client";

// Extra details for the Workflows page's cards: the steps each workflow
// uses (for its app icons) and its latest run, plus a few templates to
// suggest. Loaded after the list itself, so the page shows at once.

import { useEffect, useMemo, useState } from "react";
import { executionApi, nodeApi, workflowApi } from "@/lib/api";
import type { Execution, NodeDefinition, Workflow, WorkflowTemplate } from "@/types/api";

export interface CardDetails {
  /** Node types in left-to-right order on the canvas. */
  steps: string[];
  last: Execution | null;
}

async function detailsOf(wf: Workflow): Promise<CardDetails> {
  const versionId = wf.active_version_id ?? (await workflowApi.listVersions(wf.id, 1, 1)).items[0]?.id;
  const [version, runs] = await Promise.all([
    versionId ? workflowApi.getVersion(wf.id, versionId) : Promise.resolve(null),
    executionApi.listByWorkflow(wf.id, 1, 1),
  ]);
  const nodes = [...(version?.definition.nodes ?? [])].sort((a, b) => (a.position?.x ?? 0) - (b.position?.x ?? 0));
  return { steps: nodes.map((n) => n.type), last: runs.items[0] ?? null };
}

/** The app icons to show for a workflow: each app once, plus AI, in order. */
export function stepIcons(steps: string[], catalog: Map<string, NodeDefinition>): { key: string; integrationId?: string; def?: NodeDefinition }[] {
  const out: { key: string; integrationId?: string; def?: NodeDefinition }[] = [];
  for (const type of steps) {
    const def = catalog.get(type);
    if (!def) continue;
    const app = def.integration?.id;
    const ai = def.category === "ai" || def.category === "llm" || type === "transcribe_audio";
    const key = app ?? (ai ? "ai" : "");
    if (!key || out.some((o) => o.key === key)) continue;
    out.push(app ? { key, integrationId: app } : { key, def });
  }
  return out.slice(0, 4);
}

export function useWorkflowCards(workflows: Workflow[], workspaceId: string | null) {
  const [details, setDetails] = useState<Map<string, CardDetails>>(new Map());
  const [catalogList, setCatalogList] = useState<NodeDefinition[]>([]);
  const [templates, setTemplates] = useState<WorkflowTemplate[]>([]);
  const key = workflows.map((w) => `${w.id}:${w.updated_at}:${w.active_version_id ?? ""}`).join(",");

  useEffect(() => {
    nodeApi.list().then((l) => setCatalogList(l.items)).catch(() => undefined);
  }, []);

  useEffect(() => {
    if (!workspaceId) return;
    let cancelled = false;
    workflowApi.templates(workspaceId).then((l) => !cancelled && setTemplates(l.items)).catch(() => undefined);
    return () => {
      cancelled = true;
    };
  }, [workspaceId]);

  useEffect(() => {
    let cancelled = false;
    // One workflow's details failing to load leaves just its card plain.
    Promise.all(workflows.map((wf) => detailsOf(wf).then((d) => [wf.id, d] as const).catch(() => null))).then((all) => {
      if (!cancelled) setDetails(new Map(all.filter((x) => x !== null)));
    });
    return () => {
      cancelled = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps -- key covers the list's contents
  }, [key]);

  const catalog = useMemo(() => new Map(catalogList.map((d) => [d.type, d])), [catalogList]);
  return { details, catalog, templates };
}
