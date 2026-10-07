// Copy / paste of canvas selections. The clipboard is in-memory only (never
// browser storage): it holds definition fragments, which contain credential
// IDs at most, never secrets.

import type { WorkflowDefinition, WorkflowEdge, WorkflowNode } from "@/types/api";
import { newEdgeId, newNodeId } from "./definition";

export interface Fragment {
  nodes: WorkflowNode[];
  /** Only edges between copied nodes. */
  edges: WorkflowEdge[];
}

export const PASTE_OFFSET = 32;

export function copyFragment(definition: WorkflowDefinition, nodeIds: string[]): Fragment | null {
  const ids = new Set(nodeIds);
  const nodes = definition.nodes.filter((n) => ids.has(n.id));
  if (nodes.length === 0) return null;
  return structuredClone({
    nodes,
    edges: definition.edges.filter((e) => ids.has(e.source) && ids.has(e.target)),
  });
}

function uniqueName(name: string, taken: Set<string>): string {
  if (!taken.has(name)) return name;
  const base = name.replace(/ \d+$/, "");
  for (let i = 2; ; i++) if (!taken.has(`${base} ${i}`)) return `${base} ${i}`;
}

/** Rewrites {{old_id.…}} references to copied nodes so a pasted group keeps
 * referring to itself rather than to the originals. */
function remapReferences(v: unknown, ids: Map<string, string>): unknown {
  if (typeof v === "string") {
    return v.replace(/\{\{([A-Za-z_][A-Za-z0-9_]*)(\.[A-Za-z0-9_.]*)?\}\}/g, (m, head: string, rest = "") =>
      ids.has(head) ? `{{${ids.get(head)}${rest}}}` : m,
    );
  }
  if (Array.isArray(v)) return v.map((x) => remapReferences(x, ids));
  if (v && typeof v === "object") return Object.fromEntries(Object.entries(v).map(([k, x]) => [k, remapReferences(x, ids)]));
  return v;
}

/** Adds a copy of the fragment (new IDs, unique names, offset positions). */
export function pasteFragment(definition: WorkflowDefinition, fragment: Fragment, offset = PASTE_OFFSET): { definition: WorkflowDefinition; nodeIds: string[] } {
  const takenIds = new Set(definition.nodes.map((n) => n.id));
  const takenNames = new Set(definition.nodes.map((n) => n.name));
  const ids = new Map<string, string>();
  for (const n of fragment.nodes) {
    const id = newNodeId(n.type, takenIds);
    takenIds.add(id);
    ids.set(n.id, id);
  }
  const nodes = fragment.nodes.map((n): WorkflowNode => {
    const name = uniqueName(n.name, takenNames);
    takenNames.add(name);
    return {
      id: ids.get(n.id)!,
      type: n.type,
      name,
      position: { x: (n.position?.x ?? 0) + offset, y: (n.position?.y ?? 0) + offset },
      config: remapReferences(structuredClone(n.config), ids) as Record<string, unknown>,
    };
  });
  const edgeIds = new Set(definition.edges.map((e) => e.id));
  const edges = fragment.edges.map((e): WorkflowEdge => {
    const id = newEdgeId(edgeIds);
    edgeIds.add(id);
    return { ...e, id, source: ids.get(e.source)!, target: ids.get(e.target)! };
  });
  return {
    definition: { ...definition, nodes: [...definition.nodes, ...nodes], edges: [...definition.edges, ...edges] },
    nodeIds: nodes.map((n) => n.id),
  };
}
