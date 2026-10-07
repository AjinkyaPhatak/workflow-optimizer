"use client";

import { Background, BackgroundVariant, Handle, Position, ReactFlow, ReactFlowProvider, type NodeProps } from "@xyflow/react";
import { useMemo } from "react";
import { STATUS_ICON, type NodeRunStatus, type TimelineRow } from "@/features/executions/timeline";
import { toCanvas, type CanvasNode } from "@/lib/workflow/mapping";
import type { WorkflowDefinition } from "@/types/api";

type StatusNode = CanvasNode & { data: CanvasNode["data"] & { status: NodeRunStatus } };

function StatusNodeView({ data, selected }: NodeProps<StatusNode>) {
  return (
    <div className={`graph-node status-${data.status.toLowerCase()} ${selected ? "selected" : ""}`}>
      <Handle type="target" position={Position.Left} isConnectable={false} />
      <span className="timeline-icon">{STATUS_ICON[data.status]}</span> {data.node.name}
      <Handle type="source" position={Position.Right} isConnectable={false} />
    </div>
  );
}

const nodeTypes = { workflow: StatusNodeView };

/** The executed workflow, read-only, each node showing its status. Reuses the
 * editor's workflow -> canvas mapping (same node identity). Edges are drawn
 * node-to-node; port detail lives in the editor. */
export function ExecutionGraph({
  definition,
  rows,
  selected,
  onSelect,
}: {
  definition: WorkflowDefinition;
  rows: TimelineRow[];
  selected: string | null;
  onSelect: (id: string) => void;
}) {
  const { nodes, edges } = useMemo(() => {
    const status = new Map(rows.map((r) => [r.nodeId, r.status]));
    const c = toCanvas(definition, { selectedNodeIds: new Set(selected ? [selected] : []) });
    return {
      nodes: c.nodes.map((n) => ({ ...n, data: { ...n.data, status: status.get(n.id) ?? "PENDING" } })) as StatusNode[],
      edges: c.edges.map((e) => ({ ...e, sourceHandle: null, targetHandle: null })),
    };
  }, [definition, rows, selected]);

  return (
    <div className="execution-graph" data-testid="execution-graph">
      <ReactFlowProvider>
        <ReactFlow<StatusNode>
          nodes={nodes}
          edges={edges}
          nodeTypes={nodeTypes}
          nodesDraggable={false}
          nodesConnectable={false}
          elementsSelectable
          onNodeClick={(_, n) => onSelect(n.id)}
          fitView
          fitViewOptions={{ padding: 0.2, maxZoom: 1 }}
          proOptions={{ hideAttribution: true }}
        >
          <Background variant={BackgroundVariant.Dots} gap={16} size={1} />
        </ReactFlow>
      </ReactFlowProvider>
    </div>
  );
}
