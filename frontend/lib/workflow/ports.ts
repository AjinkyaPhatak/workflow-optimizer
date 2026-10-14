// Connection feedback while the user drags an edge. This is UX only: it
// mirrors the backend's port-type rule so obviously wrong edges are refused
// immediately, but the backend graph validator stays authoritative and runs
// on save, validate and publish.

import type { NodeDefinition, PortDefinition, ValueType, WorkflowDefinition, WorkflowNode } from "@/types/api";

/** The extra output of a node set to "go to the error output" (Phase E2). */
export const ERROR_PORT: PortDefinition = {
  name: "error", type: "json", required: false, multiple: false, description: "The failure, when the node fails: code, message, node_id",
};

/** A node's outputs: its type's, plus the error output when its settings
 * route failures there (same rule as workflow.OutputPort). */
export function nodeOutputs(def: NodeDefinition | undefined, node: Pick<WorkflowNode, "settings">): PortDefinition[] {
  const outs = def?.outputs ?? [];
  if (node.settings?.on_error === "error_output" && !outs.some((p) => p.name === ERROR_PORT.name)) return [...outs, ERROR_PORT];
  return outs;
}

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
  const out = nodeOutputs(sourceDef, source).find((p) => p.name === c.sourcePort);
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
