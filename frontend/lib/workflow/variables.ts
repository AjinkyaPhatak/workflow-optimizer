// Suggestions for {{variable}} references in text configuration. The backend
// resolver stays authoritative; these are typing aids only.

import type { NodeDefinition, WorkflowDefinition } from "@/types/api";

export function variableSuggestions(definition: WorkflowDefinition, catalog: Map<string, NodeDefinition>, selfId?: string): string[] {
  const out = ["{{input.query}}"];
  for (const n of definition.nodes) {
    if (n.id === selfId) continue;
    for (const p of catalog.get(n.type)?.outputs ?? []) out.push(`{{${n.id}.${p.name}}}`);
  }
  return out;
}
