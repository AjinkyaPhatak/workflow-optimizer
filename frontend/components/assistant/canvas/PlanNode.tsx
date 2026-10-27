"use client";
import { Handle, Position, useUpdateNodeInternals, type Node, type NodeProps } from "@xyflow/react";
import { memo, useEffect, useRef, useState, type ReactNode } from "react";
import { WorkflowNodeFrame } from "@/components/nodes/WorkflowNodeFrame";
import { Popover, type AnchorRect } from "@/components/ui/Popover";
import { NodeIcon } from "@/components/ui/NodeIcon";
import type { Step, ToolDescriptor } from "@/types/automation";
import { resultFields } from "@/lib/automation/editor";

export interface PlanNodeData extends Record<string, unknown> {
  step?: Step; tool?: ToolDescriptor; label?: string; kindName?: string; comparison?: string; summary?: string;
  onEdit?: (id: string) => void; fieldEditor?: ReactNode; completed?: boolean;
}
export type PlanFlowNode = Node<PlanNodeData, "plan">;
export function fieldLabel(name: string) { const clean = name.replace(/^(config|port)\./, "").replaceAll("_", " "); return clean.charAt(0).toUpperCase() + clean.slice(1); }
export function toolIcon(tool?: ToolDescriptor, kind = "tool") {
  const value = tool as (ToolDescriptor & { category?: string; integration_id?: string; icon?: string }) | undefined;
  const id = tool?.id.replace(/^node\./, "") ?? kind;
  const rawCategory = value?.category ?? (id.startsWith("ai.") || id.includes("llm") ? "ai" : id.startsWith("knowledge.") ? "knowledge_base" : tool?.provider ? "integration" : "general");
  const category = rawCategory === "knowledge" ? "knowledge_base" : rawCategory === "apps" ? "integration" : rawCategory === "basics" ? "general" : rawCategory;
  const integration = value?.integration_id ?? (id.startsWith("whatsapp.") ? "whatsapp" : undefined);
  return { type: value?.icon ?? (kind === "branch" ? "condition" : id), category, integration: integration ? { id: integration, name: tool?.name ?? integration, action: tool?.name ?? "" } : undefined };
}
function View({ id, data, selected, dragging }: NodeProps<PlanFlowNode>) {
  const { step, tool } = data;
  const updateInternals = useUpdateNodeInternals();
  const ports = (tool?.inputs ?? []).map(field => field.name).join("|") + ":" + (tool?.outputs ?? []).map(field => field.name).join("|");
  useEffect(() => { updateInternals(id); }, [id, ports, updateInternals]);
  const [hover, setHover] = useState<AnchorRect | null>(null);
  const hoverTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const clearHover = () => { if (hoverTimer.current) clearTimeout(hoverTimer.current); hoverTimer.current = null; setHover(null); };
  useEffect(() => () => { if (hoverTimer.current) clearTimeout(hoverTimer.current); }, []);
  if (!step) return <div className="plan-join" aria-label={data.label}><Handle type="target" position={Position.Left} id="flow-in" isConnectable={false} /><span>{data.label}</span><Handle type="source" position={Position.Right} id="flow-out" isConnectable={false} /></div>;
  const icon = toolIcon(tool, step.kind);
  const inputs = tool?.inputs ?? (step.kind === "map" ? [{ name: "items", description: "List to repeat over", required: true }] : []);
  const outputs = resultFields(step, tool ? [tool] : []);
  const kind = step.kind !== "tool" ? "logic" : icon.integration ? "apps" : icon.category === "ai" || icon.category === "llm" ? "ai" : icon.category === "knowledge_base" ? "knowledge" : "basics";
  return <WorkflowNodeFrame id={id} type={step.tool || step.kind} kind={kind} selected={selected} dragging={dragging}>
    <Handle type="target" position={Position.Left} id="flow-in" className="plan-control-handle" isConnectable={false} />
    <Handle type="source" position={Position.Right} id="flow-out" className="plan-control-handle" isConnectable={false} />
    <div className="wf-node-header" onPointerDown={clearHover} onMouseLeave={clearHover} onMouseEnter={event => {
      const box = event.currentTarget.getBoundingClientRect(); const anchor = { left: box.left, top: box.top, right: box.right, bottom: box.bottom };
      hoverTimer.current = setTimeout(() => setHover(anchor), 450);
    }}>
      <span className={`node-icon cat-${icon.category === "ai" || icon.category === "llm" ? "ai" : icon.integration ? "integration" : "general"}`}><NodeIcon def={icon} size={18} /></span>
      <div className="wf-node-heading"><div className="wf-node-title">{step.name}</div><div className="wf-node-type">{tool?.name ?? data.kindName ?? fieldLabel(step.kind)}</div></div>
      {data.completed && <span className="wf-node-test completed" title="Step completed">✓</span>}
    </div>
    {(step.condition || step.kind === "wait" || step.question || data.summary) && <div className="plan-node-description">
      {step.condition ? (data.comparison ?? "Decision") + " " + (step.condition.right.source === "literal" ? step.condition.right.value_json.replace(/^"|"$/g, "") : "another result")
        : step.question?.prompt ?? (step.kind === "wait" ? step.duration_seconds ? `Wait ${step.duration_seconds} seconds` : "Wait for an event" : data.summary)}
    </div>}
    {data.fieldEditor && <div className="wf-node-body nodrag nowheel">{data.fieldEditor}</div>}
    <div className="wf-ports"><div>{inputs.map(input => <div key={input.name} className={`wf-port in ${input.required ? "required" : ""}`} title={input.description}>
      <Handle type="target" position={Position.Left} id={"in:" + input.name} /><span className="pname">{fieldLabel(input.name)}</span>
    </div>)}</div><div>{outputs.map(output => <div key={output.name} className="wf-port out" title={output.description}>
      <Handle type="source" position={Position.Right} id={"out:" + output.name} /><span className="pname">{fieldLabel(output.name)}</span>
    </div>)}</div></div>
    <button className="link wf-node-more nodrag" onClick={() => data.onEdit?.(step.id)}>Edit step</button>
    {hover && !dragging && <Popover anchor={hover} testId="node-hover"><div className="node-info">
      <div className="info-title">{step.name}</div><p className="info-desc">{tool?.description || data.summary || data.kindName}</p>
      {inputs.length > 0 && <div className="info-section"><div className="info-heading">Inputs</div><ul className="info-ports">{inputs.map(input => <li key={input.name}><span className="info-port-name">{fieldLabel(input.name)}</span>{input.required && <span className="info-port-req">required</span>}<div className="info-port-desc">{input.description}</div></li>)}</ul></div>}
      {outputs.length > 0 && <div className="info-section"><div className="info-heading">Outputs</div><ul className="info-ports">{outputs.map(output => <li key={output.name}><span className="info-port-name">{fieldLabel(output.name)}</span><div className="info-port-desc">{output.description}</div></li>)}</ul></div>}
      {tool?.provider && <div className="info-section">Uses your connected {tool.provider} account.</div>}
      {tool?.side_effects && tool.side_effects !== "none" && <div className="info-section">This step may change an external service when run.</div>}
    </div></Popover>}
  </WorkflowNodeFrame>;
}
export const PlanNode = memo(View);
