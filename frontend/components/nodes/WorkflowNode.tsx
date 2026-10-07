"use client";

import { Handle, Position, type NodeProps } from "@xyflow/react";
import { memo } from "react";
import { useCanvasState } from "@/components/canvas/CanvasContext";
import { useEditorContext } from "@/features/workflows/EditorContext";
import { categoryClass, iconText } from "@/features/nodes/catalog";
import type { CanvasNode } from "@/lib/workflow/mapping";
import { checkConnection } from "@/lib/workflow/ports";
import { useEditorStore } from "@/stores/workflow-editor/store";
import type { PortDefinition } from "@/types/api";

/** Renders any workflow node from its backend NodeDefinition: the ports are
 * the definition's ports, never assumptions about a node type. */
function WorkflowNodeView({ id, data, selected, dragging }: NodeProps<CanvasNode>) {
  const { catalog } = useEditorContext();
  const { issues, connecting } = useCanvasState();
  const def = catalog.get(data.node.type);
  const edges = useEditorStore((s) => s.definition.edges);
  // Most specific first: a named port or field says exactly what to fix.
  const nodeIssues = issues
    .filter((i) => i.nodeId === id && i.edgeId === undefined)
    .sort((a, b) => Number(!(a.port || a.field)) - Number(!(b.port || b.field)));
  const errorPorts = new Set(nodeIssues.map((i) => i.port).filter(Boolean));
  const hasError = nodeIssues.some((i) => i.severity === "error");
  const connected = (port: string, dir: "in" | "out") =>
    edges.some((e) => (dir === "in" ? e.target === id && e.target_port === port : e.source === id && e.source_port === port));

  /** While a connection is dragged: can this port take it? (UX only; the
   * backend validator stays authoritative.) */
  const affordance = (p: PortDefinition, dir: "in" | "out"): string => {
    if (!connecting || connecting.nodeId === id) return "";
    const wantsInput = connecting.handleType === "source";
    if ((dir === "in") !== wantsInput) return "port-dim";
    const attempt = wantsInput
      ? { source: connecting.nodeId, sourcePort: connecting.port, target: id, targetPort: p.name }
      : { source: id, sourcePort: p.name, target: connecting.nodeId, targetPort: connecting.port };
    return checkConnection(attempt, useEditorStore.getState().definition, catalog).ok ? "port-compatible" : "port-dim";
  };

  const port = (p: PortDefinition, dir: "in" | "out") => (
    <div
      key={p.name}
      className={`wf-port ${dir} ${p.required ? "required" : ""} ${errorPorts.has(p.name) ? "port-error" : ""} ${affordance(p, dir)}`}
      title={`${p.name} (${p.type})${p.description ? ` — ${p.description}` : ""}`}
      data-port={p.name}
    >
      <Handle
        type={dir === "in" ? "target" : "source"}
        position={dir === "in" ? Position.Left : Position.Right}
        id={p.name}
        className={connected(p.name, dir) ? "connected" : undefined}
      />
      <span className="pname">{p.name}</span>
      <span className="ptype">{p.type}</span>
    </div>
  );

  return (
    <div
      className={`wf-node ${selected ? "selected" : ""} ${hasError ? "invalid" : nodeIssues.length ? "warning" : ""} ${dragging ? "dragging" : ""}`}
      data-testid={`node-${id}`}
      data-node-type={data.node.type}
    >
      <div className="wf-node-header">
        <span className={`node-icon ${categoryClass(def?.category ?? "")}`}>{iconText(def ?? { name: data.node.type })}</span>
        <div className="wf-node-heading">
          <div className="wf-node-title">{data.node.name}</div>
          <div className="wf-node-type">{def ? def.name : `${data.node.type} (unknown type)`}</div>
        </div>
        {nodeIssues.length > 0 && (
          <span
            className={`wf-node-badge ${hasError ? "error" : "warning"}`}
            data-testid={`node-issues-${id}`}
            aria-label={`${nodeIssues.length} problem${nodeIssues.length === 1 ? "" : "s"}`}
          >
            ⚠{nodeIssues.length > 1 ? ` ${nodeIssues.length}` : ""}
          </span>
        )}
      </div>
      <div className="wf-ports">
        <div>{def?.inputs.map((p) => port(p, "in"))}</div>
        <div>{def?.outputs.map((p) => port(p, "out"))}</div>
      </div>
      {nodeIssues.length > 0 && (
        <div className={`wf-node-errors ${hasError ? "" : "warning"}`}>
          <div>{nodeIssues[0].message}</div>
          {nodeIssues.length > 1 && <div className="more">+{nodeIssues.length - 1} more · hover for details</div>}
        </div>
      )}
    </div>
  );
}

export const WorkflowNode = memo(WorkflowNodeView);
