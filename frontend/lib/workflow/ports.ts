// Connection feedback while the user drags an edge. This is UX only: it
// mirrors the backend's port-type rule so obviously wrong edges are refused
// immediately, but the backend graph validator stays authoritative and runs
// on save, validate and publish.

import type { NodeDefinition, ValueType, WorkflowDefinition } from "@/types/api";

/** Same rule as workflow.PortTypesCompatible: equal types, or either side json. */
export function portTypesCompatible(source: ValueType, target: ValueType): boolean {
  return source === target || source === "json" || target === "json";
}

export interface ConnectionAttempt {
  source: string;
  sourcePort: string;
  target: string;
  targetPort: string;
}

export type ConnectionCheck = { ok: true } | { ok: false; reason: string };

export function checkConnection(
  c: ConnectionAttempt,
  definition: WorkflowDefinition,
  catalog: Map<string, NodeDefinition>,
): ConnectionCheck {
  if (c.source === c.target) return { ok: false, reason: "A node cannot connect to itself" };
  const source = definition.nodes.find((n) => n.id === c.source);
  const target = definition.nodes.find((n) => n.id === c.target);
  if (!source || !target) return { ok: false, reason: "Unknown node" };
  const sourceDef = catalog.get(source.type);
  const targetDef = catalog.get(target.type);
  if (!sourceDef || !targetDef) return { ok: false, reason: "Unknown node type" };
  const out = sourceDef.outputs.find((p) => p.name === c.sourcePort);
  const inp = targetDef.inputs.find((p) => p.name === c.targetPort);
  if (!out) return { ok: false, reason: `"${c.sourcePort}" is not an output of ${sourceDef.name}` };
  if (!inp) return { ok: false, reason: `"${c.targetPort}" is not an input of ${targetDef.name}` };
  if (!portTypesCompatible(out.type, inp.type)) {
    return { ok: false, reason: `${out.type} cannot connect to ${inp.type}` };
  }
  const incoming = definition.edges.filter((e) => e.target === c.target && e.target_port === c.targetPort);
  if (incoming.some((e) => e.source === c.source && e.source_port === c.sourcePort)) {
    return { ok: false, reason: "These ports are already connected" };
  }
  if (!inp.multiple && incoming.length > 0) {
    return { ok: false, reason: `Input "${inp.name}" accepts one connection` };
  }
  return { ok: true };
}
