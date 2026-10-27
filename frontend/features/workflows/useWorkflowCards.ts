"use client";

// Extra details for the Workflows page's cards: the steps each workflow
// uses (for its app icons) and its latest run, plus a few templates to
// suggest. Loaded after the list itself, so the page shows at once.

import { useEffect, useMemo, useState } from "react";
import { executionApi, nodeApi, workflowApi } from "@/lib/api";
import { assistantApi } from "@/lib/api/assistant";
import { templateSummary } from "@/lib/automation/workflowSummary";
import type { AutomationRun, Step } from "@/types/automation";
import type { NodeDefinition, Workflow, WorkflowTemplate } from "@/types/api";

export interface CardDetails {
  /** Node types in left-to-right order on the canvas. */
  steps: string[];
  last: { status: string; created_at: string } | null;
  ready?: boolean;
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

interface StepIcon {
  key: string;
  integrationId?: string;
  def?: Pick<NodeDefinition, "type" | "category" | "integration">;
}

/** Icons for the steps already in a workflow: each app or tool once, plus AI. */
export function stepIcons(steps: string[], catalog: Map<string, NodeDefinition>): StepIcon[] {
  const out: StepIcon[] = [];
  for (const type of steps) {
    // Compiler built-ins and controls are not in the legacy node catalog.
    // Their existing plan type still gives NodeIcon a useful presentation fallback.
    const def: Pick<NodeDefinition, "type" | "category" | "integration"> = catalog.get(type) ?? { type, category: type.startsWith("ai.") ? "ai" : "" };
    const app = def.integration?.id;
    const ai = def.category === "ai" || def.category === "llm" || type === "transcribe_audio";
    const key = app ?? (ai ? "ai" : type);
    if (out.some(o => o.key === key)) continue;
    out.push(app ? { key, integrationId: app } : { key, def });
  }
  return out.slice(0, 4);
}

export function useWorkflowCards(workflows: Workflow[], workspaceId: string | null) {
  const [details, setDetails] = useState<Map<string, CardDetails>>(new Map());
  const [sources, setSources] = useState<{ workspace: string; key: string; kinds: Map<string, "compiler" | "legacy"> } | null>(null);
  const [catalogList, setCatalogList] = useState<NodeDefinition[]>([]);
  const [templates, setTemplates] = useState<WorkflowTemplate[]>([]);
  const key = workflows.map((w) => `${w.id}:${w.updated_at}:${w.active_version_id ?? ""}`).join(",");

  useEffect(() => {
    nodeApi.list().then((l) => setCatalogList(l.items)).catch(() => undefined);
  }, []);

  useEffect(() => {
    if (!workspaceId) return;
    let cancelled = false;
    Promise.allSettled([workflowApi.templates(workspaceId), assistantApi.templates(workspaceId)]).then(([legacy, modern]) => {
      if (cancelled) return;
      const merged = new Map((legacy.status === "fulfilled" ? legacy.value.items : []).map(item => [item.id, item]));
      if (modern.status === "fulfilled") for (const item of modern.value.items) merged.set(item.id, templateSummary(item));
      setTemplates([...merged.values()]);
    });
    return () => {
      cancelled = true;
    };
  }, [workspaceId]);

  useEffect(() => {
    if (!workspaceId) return;
    let cancelled = false;
    // Classify first: a compiler workflow must never fall through to legacy APIs,
    // even when its plan or run history cannot be loaded.
    const load = async () => {
      const [modern, runs] = await Promise.allSettled([assistantApi.workflows(workspaceId), assistantApi.runs(workspaceId)]);
      if (cancelled || modern.status !== "fulfilled") return;
      const compilerIds = new Set(modern.value.items.map(item => item.id));
      setSources({ workspace: workspaceId, key, kinds: new Map(workflows.map(wf => [wf.id, compilerIds.has(wf.id) ? "compiler" : "legacy"])) });
      const latest = new Map<string, AutomationRun>();
      if (runs.status === "fulfilled") for (const run of runs.value.items) {
        if (run.session_id && (!latest.has(run.session_id) || latest.get(run.session_id)!.created_at < run.created_at)) latest.set(run.session_id, run);
      }
      const flatten = (steps: Step[]): string[] => steps.flatMap(step => [
        step.kind === "tool" ? step.tool.replace(/^node\./, "") : step.kind,
        ...flatten(step.children ?? []), ...flatten(step.otherwise ?? []),
      ]);
      const all = await Promise.all(workflows.map(async wf => {
        if (!compilerIds.has(wf.id)) return detailsOf(wf).then(d => [wf.id, d] as const).catch(() => null);
        const envelope = await assistantApi.workflow(wf.id).catch(() => null);
        const compilation = envelope?.session.compilation;
        return [wf.id, { steps: flatten(compilation?.plan.steps ?? []), ready: compilation?.ready, last: latest.get(wf.id) ?? null }] as const;
      }));
      if (!cancelled) setDetails(new Map(all.filter(x => x !== null)));
    };
    void load();
    return () => { cancelled = true; };
    // eslint-disable-next-line react-hooks/exhaustive-deps -- key covers the list's contents
  }, [workspaceId, key]);

  const catalog = useMemo(() => new Map(catalogList.map((d) => [d.type, d])), [catalogList]);
  return { details, catalog, templates, kinds: sources?.workspace === workspaceId && sources.key === key ? sources.kinds : new Map<string, "compiler" | "legacy">() };
}
