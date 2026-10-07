// The adapter between the backend workflow definition (source of truth) and
// the React Flow canvas model:
//
//   WorkflowDefinition --toCanvas--> canvas nodes/edges --(user edits)-->
//   canvas nodes/edges --fromCanvas--> WorkflowDefinition
//
// Canvas objects never reach the API; only WorkflowDefinition does.

import type { Edge, Node } from "@xyflow/react";
import type { Position, WorkflowDefinition, WorkflowEdge, WorkflowNode } from "@/types/api";

export const CANVAS_NODE_TYPE = "workflow";

export interface CanvasNodeData extends Record<string, unknown> {
  /** The workflow node this canvas node renders (position excluded). */
  node: Omit<WorkflowNode, "position">;
  /** Where a node without a stored position was placed; if it is still there,
   * fromCanvas writes position null back (no invented positions). */
  autoPosition: Position | null;
}

export type CanvasNode = Node<CanvasNodeData, typeof CANVAS_NODE_TYPE>;
export type CanvasEdge = Edge;

const AUTO_SPACING_X = 280;

function autoPosition(index: number): Position {
  return { x: 80 + (index % 4) * AUTO_SPACING_X, y: 80 + Math.floor(index / 4) * 180 };
}

export interface ToCanvasOptions {
  selectedNodeIds?: ReadonlySet<string>;
  selectedEdgeIds?: ReadonlySet<string>;
}

export function toCanvas(definition: WorkflowDefinition, opts: ToCanvasOptions = {}): { nodes: CanvasNode[]; edges: CanvasEdge[] } {
  const nodes = definition.nodes.map((n, i): CanvasNode => {
    const auto = n.position ? null : autoPosition(i);
    const { position, ...rest } = n;
    return {
      id: n.id,
      type: CANVAS_NODE_TYPE,
      position: position ? { x: position.x, y: position.y } : auto!,
      data: { node: rest, autoPosition: auto },
      selected: opts.selectedNodeIds?.has(n.id) ?? false,
    };
  });
  const edges = definition.edges.map(
    (e): CanvasEdge => ({
      id: e.id,
      source: e.source,
      sourceHandle: e.source_port,
      target: e.target,
      targetHandle: e.target_port,
      selected: opts.selectedEdgeIds?.has(e.id) ?? false,
    }),
  );
  return { nodes, edges };
}

/** Rebuilds the definition from the canvas. `base` supplies what the canvas
 * does not carry (schema version, settings). */
export function fromCanvas(nodes: CanvasNode[], edges: CanvasEdge[], base: Pick<WorkflowDefinition, "version" | "settings" | "variables">): WorkflowDefinition {
  const out: WorkflowDefinition = {
    version: base.version,
    nodes: nodes.map((cn): WorkflowNode => {
      const { node, autoPosition: auto } = cn.data;
      const unmoved = auto !== null && cn.position.x === auto.x && cn.position.y === auto.y;
      return {
        id: node.id,
        type: node.type,
        name: node.name,
        position: unmoved ? null : { x: cn.position.x, y: cn.position.y },
        config: structuredClone(node.config),
      };
    }),
    edges: edges.map(
      (ce): WorkflowEdge => ({
        id: ce.id,
        source: ce.source,
        source_port: ce.sourceHandle ?? "",
        target: ce.target,
        target_port: ce.targetHandle ?? "",
      }),
    ),
    settings: structuredClone(base.settings),
  };
  if (base.variables) out.variables = structuredClone(base.variables);
  return out;
}
