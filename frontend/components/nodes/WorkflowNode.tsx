"use client";

import { nodeOutputs } from "@/lib/workflow/ports";
import { useNodeTestResult } from "@/stores/test-run/store";
import { Handle, Position, type NodeProps } from "@xyflow/react";
import { memo } from "react";
import { useCanvasState } from "@/components/canvas/CanvasContext";
import { FieldEditor } from "@/components/panels/ConfigPanel";
import { NodeIcon } from "@/components/ui/NodeIcon";
import { useEditorContext } from "@/features/workflows/EditorContext";
import { actionName, categoryClass } from "@/features/nodes/catalog";
import { tabOf } from "@/features/nodes/palette";
import { cardFields, credentialField, siblingActions } from "@/lib/workflow/card";
import type { CanvasNode } from "@/lib/workflow/mapping";
import { checkConnection } from "@/lib/workflow/ports";
import { useEditorStore } from "@/stores/workflow-editor/store";
import type { PortDefinition } from "@/types/api";

/** "message_id" -> "Message ID", "first_row" -> "First row". */
export function portLabel(name: string): string {
  const words = name.split(/[_\s]+/).filter(Boolean).map((w) => (["id", "url", "json", "api"].includes(w.toLowerCase()) ? w.toUpperCase() : w.toLowerCase()));
  if (words.length === 0) return name;
  words[0] = words[0][0].toUpperCase() + words[0].slice(1);
  return words.join(" ");
}

/** Renders any workflow node from its backend NodeDefinition (Phase H2/H3):
 * its icon and name, for app actions a menu to switch the action, the
 * account it uses and its main settings edited in place, and its inputs and
 * outputs as connection points. The ports are the definition's ports, never
 * assumptions about a node type. */
function WorkflowNodeView({ id, data, selected, dragging }: NodeProps<CanvasNode>) {
  const { catalog, catalogList } = useEditorContext();
  const { issues, connecting } = useCanvasState();
  const def = catalog.get(data.node.type);
  const edges = useEditorStore((s) => s.definition.edges);
  const readonly = useEditorStore((s) => s.mode === "readonly");
  const changeType = useEditorStore((s) => s.changeNodeType);
  // Most specific first: a named port or field says exactly what to fix.
  const tested = useNodeTestResult(id);
  const nodeIssues = issues
    .filter((i) => i.nodeId === id && i.edgeId === undefined)
    .sort((a, b) => Number(!(a.port || a.field)) - Number(!(b.port || b.field)));
  const errorPorts = new Set(nodeIssues.map((i) => i.port).filter(Boolean));
  const hasError = nodeIssues.some((i) => i.severity === "error");
  const connected = (port: string, dir: "in" | "out") =>
    edges.some((e) => (dir === "in" ? e.target === id && e.target_port === port : e.source === id && e.source_port === port));
  const actions = siblingActions(def, catalogList);
  const account = credentialField(def);
  const fields = cardFields(def);
  const hidden = (def?.config.length ?? 0) - fields.length - (account ? 1 : 0);
  const fieldIssues = (name: string) => nodeIssues.filter((i) => i.field === name);
  const fieldNode = { ...data.node, position: null };

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
      title={p.description || portLabel(p.name)}
      data-port={p.name}
    >
      <Handle
        type={dir === "in" ? "target" : "source"}
        position={dir === "in" ? Position.Left : Position.Right}
        id={p.name}
        className={connected(p.name, dir) ? "connected" : undefined}
      />
      <span className="pname">{portLabel(p.name)}</span>
    </div>
  );

  const kind = def ? tabOf(def) : "basics";
  return (
    <div
      className={`wf-node kind-${kind} ${selected ? "selected" : ""} ${hasError ? "invalid" : nodeIssues.length ? "warning" : ""} ${dragging ? "dragging" : ""}`}
      data-testid={`node-${id}`}
      data-node-type={data.node.type}
    >
      <div className="wf-node-header">
        <span className={`node-icon ${categoryClass(def?.category ?? "")}`}><NodeIcon def={def ?? { type: data.node.type, category: "" }} size={18} /></span>
        <div className="wf-node-heading">
          <div className="wf-node-title">{data.node.name}</div>
          {actions.length > 1 && def ? (
            <select
              className="wf-node-action nodrag" aria-label={`What should ${def.integration?.name ?? "this step"} do?`} value={def.type}
              disabled={readonly} data-testid={`node-action-${id}`}
              onChange={(e) => {
                const to = catalog.get(e.target.value);
                if (to) changeType(id, def, to);
              }}
            >
              {actions.map((a) => <option key={a.type} value={a.type}>{actionName(a)}</option>)}
            </select>
          ) : (
            <div className="wf-node-type">{def ? (def.integration ? actionName(def) : def.name === data.node.name ? "" : def.name) : `${data.node.type} (unknown type)`}</div>
          )}
        </div>
        {tested && (
          <span className={`wf-node-test ${tested.status.toLowerCase()}`} data-testid={`node-test-${id}`}
            title={`Last test: ${tested.status.toLowerCase()}${tested.error ? ` (${tested.error.message})` : ""}`}>
            {tested.status === "COMPLETED" ? "✓" : tested.status === "FAILED" ? "✕" : "—"}
          </span>
        )}
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
      {def && (account || fields.length > 0) && (
        <div className="wf-node-body nodrag nowheel" data-testid={`node-fields-${id}`}>
          {account && <FieldEditor node={fieldNode} field={account} issues={fieldIssues(account.name)} idPrefix={`card-${id}`} compact />}
          {fields.map((f) => <FieldEditor key={f.name} node={fieldNode} field={f} issues={fieldIssues(f.name)} idPrefix={`card-${id}`} compact />)}
          {hidden > 0 && (
            <button type="button" className="link wf-node-more" onClick={() => useEditorStore.getState().select([id])}>
              More settings ({hidden})
            </button>
          )}
        </div>
      )}
      <div className="wf-ports">
        <div>{def?.inputs.map((p) => port(p, "in"))}</div>
        <div>{nodeOutputs(def, data.node).map((p) => port(p, "out"))}</div>
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
