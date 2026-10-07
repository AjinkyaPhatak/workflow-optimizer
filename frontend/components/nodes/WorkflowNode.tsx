"use client";

import { Handle, Position, type NodeProps } from "@xyflow/react";
import { memo } from "react";
import { useEditorContext } from "@/features/workflows/EditorContext";
import { categoryClass, iconText } from "@/features/nodes/catalog";
import type { CanvasNode } from "@/lib/workflow/mapping";
import { useEditorStore } from "@/stores/workflow-editor/store";
import type { PortDefinition } from "@/types/api";

/** Renders any workflow node from its backend NodeDefinition: the ports are
 * the definition's ports, never assumptions about a node type. */
function WorkflowNodeView({ id, data, selected }: NodeProps<CanvasNode>) {
  const { catalog } = useEditorContext();
  const def = catalog.get(data.node.type);
  const edges = useEditorStore((s) => s.definition.edges);
  const errors = useEditorStore((s) => s.validation?.errors);
  const nodeErrors = (errors ?? []).filter((e) => e.node_id === id);
  const errorPorts = new Set(nodeErrors.map((e) => e.port).filter(Boolean));
  const connected = (port: string, dir: "in" | "out") =>
    edges.some((e) => (dir === "in" ? e.target === id && e.target_port === port : e.source === id && e.source_port === port));

  const port = (p: PortDefinition, dir: "in" | "out") => (
    <div
      key={p.name}
      className={`wf-port ${dir} ${p.required ? "required" : ""} ${errorPorts.has(p.name) ? "port-error" : ""}`}
      title={p.description || undefined}
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
      className={`wf-node ${selected ? "selected" : ""} ${nodeErrors.length ? "invalid" : ""}`}
      data-testid={`node-${id}`}
      data-node-type={data.node.type}
    >
      <div className="wf-node-header">
        <span className={`node-icon ${categoryClass(def?.category ?? "")}`}>{iconText(def ?? { name: data.node.type })}</span>
        <div>
          <div className="wf-node-title">{data.node.name}</div>
          <div className="wf-node-type">{def ? def.name : `${data.node.type} (unknown type)`}</div>
        </div>
      </div>
      <div className="wf-ports">
        <div>{def?.inputs.map((p) => port(p, "in"))}</div>
        <div>{def?.outputs.map((p) => port(p, "out"))}</div>
      </div>
      {nodeErrors.length > 0 && (
        <div className="wf-node-errors">
          {nodeErrors.map((e, i) => (
            <div key={i}>⚠ {e.port ? `${e.port}: ` : ""}{e.message}</div>
          ))}
        </div>
      )}
    </div>
  );
}

export const WorkflowNode = memo(WorkflowNodeView);
