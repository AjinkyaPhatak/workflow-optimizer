"use client";

// Runs the editor's current draft (Phase E3), up to a node or whole. Nodes
// really run, so before including any that change external systems (send an
// email, add a row) the user confirms.

import { useCallback } from "react";
import { useEditorContext } from "@/features/workflows/EditorContext";
import { workflowApi } from "@/lib/api";
import { inputOf } from "@/lib/workflow/runInput";
import { runFields, runInput } from "@/lib/workflow/runFields";
import { useEditorStore } from "@/stores/workflow-editor/store";
import { useTestRunStore } from "@/stores/test-run/store";
import type { NodeDefinition, WorkflowDefinition } from "@/types/api";

/** The node and every node it depends on (same rule as the API). */
export function upstreamIds(def: WorkflowDefinition, target: string): Set<string> {
  const keep = new Set<string>();
  const visit = (id: string) => {
    if (keep.has(id)) return;
    keep.add(id);
    for (const e of def.edges) if (e.target === id) visit(e.source);
  };
  visit(target);
  return keep;
}

/** Names of the nodes a test would run that change external systems. */
export function sideEffectNodes(def: WorkflowDefinition, catalog: Map<string, NodeDefinition>, until: string | null): string[] {
  const ids = until ? upstreamIds(def, until) : new Set(def.nodes.map((n) => n.id));
  return def.nodes
    .filter((n) => ids.has(n.id) && ["unsafe", "idempotent"].includes(catalog.get(n.type)?.side_effects ?? ""))
    .map((n) => n.name);
}

export function useTestRun(confirm: (message: string) => boolean = (m) => window.confirm(m)) {
  const { workflow, catalog } = useEditorContext();
  const { start, finish } = useTestRunStore.getState();
  const running = useTestRunStore((s) => s.running);

  const run = useCallback(async (until: string | null) => {
    if (!workflow) return;
    const definition = useEditorStore.getState().definition;
    const risky = sideEffectNodes(definition, catalog, until);
    if (risky.length > 0 && !confirm(`This test really runs ${risky.join(", ")}: ${risky.length === 1 ? "it changes" : "they change"} things outside the app (sends, posts or saves). Continue?`)) {
      return;
    }
    const store = useTestRunStore.getState();
    // The Run window's answers, plus the old Run tab's rows when used.
    const input = { ...(store.rows ? inputOf(store.rows) : {}), ...runInput(runFields(definition), store.answers).input };
    start(until);
    try {
      const result = await workflowApi.test(workflow.id, { definition, input, ...(until ? { until_node_id: until } : {}) });
      finish(result);
    } catch (e) {
      finish(null, e);
    }
  }, [workflow, catalog, confirm, start, finish]);

  return { run, running };
}
