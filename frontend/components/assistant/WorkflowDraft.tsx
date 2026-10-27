"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import { Background, Controls, ReactFlow, type Edge, type Node } from "@xyflow/react";
import type { Binding, Condition, Plan, Step, ToolDescriptor, ToolField } from "@/types/automation";

export function allSteps(steps: Step[]): Step[] { return steps.flatMap(step => [step, ...allSteps(step.children ?? []), ...allSteps(step.otherwise ?? [])]); }
function makeStep(tool: ToolDescriptor): Step {
  return { id: "step_" + crypto.randomUUID().slice(0, 8), name: tool.name, kind: "tool", tool: tool.id, inputs: [], children: [], otherwise: [],
    condition: null, question: null, max_iterations: 0, duration_seconds: 0, workflow_ref: "" };
}
function updateStep(steps: Step[], id: string, change: (step: Step) => Step): Step[] {
  return steps.map(step => step.id === id ? change(step) : { ...step, children: updateStep(step.children ?? [], id, change), otherwise: updateStep(step.otherwise ?? [], id, change) });
}
function InputBinding({ field, binding, steps, tools, onChange }: { field: ToolField; binding?: Binding; steps: Step[]; tools: ToolDescriptor[]; onChange: (binding: Binding) => void }) {
  const base = binding ?? { name: field.name, source: "literal" as const, ref: "", value_json: '""' };
  const mode = base.source === "step" ? "step" : "literal";
  let value = "";
  try { const parsed: unknown = JSON.parse(base.value_json || '""'); value = typeof parsed === "string" ? parsed : JSON.stringify(parsed); } catch { value = base.value_json; }
  const choices = steps.flatMap(step => (tools.find(t => t.id === step.tool)?.outputs ?? [{ name: "result", type: "json", description: "" }])
    .map(output => ({ ref: step.id + "." + output.name, name: step.name + " → " + output.name, type: output.type })));
  return <div className="draft-input">
    <label>{field.name.replace(/^(config|port)\./, "").replaceAll("_", " ")}{field.required ? " *" : ""}</label>
    <select aria-label={"Source for " + field.name} value={mode} onChange={e => onChange({ ...base, source: e.target.value as Binding["source"], ref: "", value_json: '""' })}>
      <option value="literal">Enter a value</option><option value="step">Use another step’s result</option>
    </select>
    {mode === "step" ? <select aria-label={"Result for " + field.name} value={base.ref} onChange={e => onChange({ ...base, source: "step", ref: e.target.value })}>
      <option value="">Choose a result…</option>{choices.map(choice => <option key={choice.ref} value={choice.ref}>{choice.name} ({choice.type})</option>)}
    </select> : <input aria-label={field.name} value={value} placeholder={base.source === "answer" ? "I’ll ask you before running" : field.description}
      onChange={e => {
        let next: unknown = e.target.value;
        if (field.type === "number" && e.target.value !== "") next = Number(e.target.value);
        if (field.type === "boolean") next = e.target.value.toLowerCase() === "true" || e.target.value.toLowerCase() === "yes";
        if (["object", "array", "json"].includes(field.type)) { try { next = JSON.parse(e.target.value) as unknown; } catch { /* Preserve typed text for checker feedback. */ } }
        onChange({ ...base, source: "literal", value_json: JSON.stringify(next), ref: "" });
      }} />}
    <p className="hint">{field.description}</p>
  </div>;
}
export function WorkflowDraft({ plan, tools, comparisons = [], selectedStep, onSelectStep, busy, onSave }: {
  plan: Plan; tools: ToolDescriptor[]; comparisons?: { label: string; value: string }[]; selectedStep?: string;
  onSelectStep?: (id: string) => void; busy: boolean; onSave: (plan: Plan) => Promise<void>;
}) {
  const [draft, setDraft] = useState(plan);
  const [localSelected, setLocalSelected] = useState("");
  const selected = selectedStep ?? localSelected;
  const inspector = useRef<HTMLDivElement>(null);
  useEffect(() => { if (selected) inspector.current?.scrollIntoView?.({ behavior: "smooth", block: "nearest" }); }, [selected]);
  function select(id: string) { setLocalSelected(id); onSelectStep?.(id); }
  const [addTool, setAddTool] = useState("");
  const [dirty, setDirty] = useState(false);
  const steps = useMemo(() => allSteps(draft.steps), [draft.steps]);
  const active = steps.find(step => step.id === selected);
  const catalog = tools.find(tool => tool.id === active?.tool);
  function change(next: Plan) { setDraft(next); setDirty(true); }
  const { nodes, edges } = useMemo(() => {
    const nodes: Node[] = []; const edges: Edge[] = [];
    let row = 0;
    const walk = (list: Step[], column: number, previous?: string, label?: string) => {
      let last = previous;
      for (const step of list) {
        nodes.push({ id: step.id, position: { x: column * 250 + 30, y: row++ * 115 + 30 },
          data: { label: <div className="draft-node"><span>{step.kind === "tool" ? "✦" : "◇"} {step.kind === "tool" ? (tools.find(t => t.id === step.tool)?.name ?? step.tool) : step.kind === "branch" ? "Decision" : step.kind}</span><strong>{step.name}</strong>{step.condition && <small>{comparisons.find(option => option.value === step.condition?.op)?.label || "Decision rule needs repair"} {step.condition.right.source === "literal" ? step.condition.right.value_json.replace(/^"|"$/g, "") : "another result"}</small>}</div> },
          className: "draft-flow-node", selected: selected === step.id });
        if (last) edges.push({ id: last + "-" + step.id, source: last, target: step.id, label, animated: false });
        walk(step.children ?? [], column + 1, step.id, step.kind === "branch" ? "Yes" : undefined);
        walk(step.otherwise ?? [], column + 2, step.id, "Otherwise");
        last = step.id;
      }
    };
    walk(draft.steps, 0); return { nodes, edges };
  }, [draft.steps, tools, selected, comparisons]);
  function setCondition(condition: Condition) {
    if (active) change({ ...draft, steps: updateStep(draft.steps, active.id, step => ({ ...step, condition })) });
  }
  return <section className="workflow-draft" aria-label="Workflow builder">
    <header><div><span className="eyebrow">Your workflow</span><h2>{draft.name}</h2></div><button className="primary" disabled={!dirty || busy} onClick={async () => { await onSave(draft); setDirty(false); }}>Save changes</button></header>
    <div className="draft-canvas">
      <ReactFlow nodes={nodes} edges={edges} onNodeClick={(_, node) => select(node.id)} fitView fitViewOptions={{ minZoom: 0.7, maxZoom: 1, padding: 0.15 }} minZoom={0.3} maxZoom={1.5} nodesDraggable nodesConnectable={false}>
        <Background gap={22} size={1} /><Controls showInteractive={false} />
      </ReactFlow>
    </div>
    <div className="draft-add"><select aria-label="Tool to add" value={addTool} onChange={e => setAddTool(e.target.value)}>
      <option value="">Add a step…</option>{tools.map(tool => <option key={tool.id} value={tool.id}>{tool.name}</option>)}
    </select><button disabled={!addTool || busy} onClick={() => { const tool = tools.find(t => t.id === addTool); if (tool) { const step = makeStep(tool); change({ ...draft, steps: [...draft.steps, step] }); select(step.id); } }}>Add</button></div>
    {active && <div className="draft-inspector" role="region" aria-label={"Edit " + active.name} ref={inspector}>
      <div className="row"><h3>{active.name || "Edit step"}</h3><span className="spacer" /><button className="small-button" onClick={() => select("")}>Close</button></div>
      <label htmlFor="step-name">Step name</label><input id="step-name" value={active.name} onChange={e => change({ ...draft, steps: updateStep(draft.steps, active.id, step => ({ ...step, name: e.target.value })) })} />
      {catalog?.inputs.map(field => <InputBinding key={field.name} field={field} binding={active.inputs.find(b => b.name === field.name)}
        tools={tools} steps={steps.filter(s => s.id !== active.id)} onChange={binding => change({ ...draft, steps: updateStep(draft.steps, active.id, step => ({
          ...step, inputs: [...step.inputs.filter(b => b.name !== binding.name), binding],
        })), questions: draft.questions.filter(q => q.step_id !== active.id || q.input !== binding.name) })} />)}
      {active.condition && <fieldset className="decision-editor"><legend>{active.kind === "repeat" ? "Stop repeating when" : "Continue on the Yes path when"}</legend>
        <InputBinding field={{ name: "value_to_check", type: "json", description: "Choose the result to check, or enter a value.", required: true }} binding={active.condition.left} steps={steps.filter(step => step.id !== active.id)} tools={tools} onChange={left => setCondition({ ...active.condition!, left })} />
        <label htmlFor="decision-rule">Rule</label>
        <select id="decision-rule" value={active.condition.op} onChange={e => setCondition({ ...active.condition!, op: e.target.value })}>
          {!comparisons.some(option => option.value === active.condition?.op) && <option value={active.condition.op}>Choose how to compare these values…</option>}
          {comparisons.map(option => <option key={option.value} value={option.value}>{option.label}</option>)}
        </select>
        <InputBinding field={{ name: "compare_with", type: "json", description: "For a number such as 150, enter just 150.", required: true }} binding={active.condition.right} steps={steps.filter(step => step.id !== active.id)} tools={tools} onChange={right => setCondition({ ...active.condition!, right })} />
      </fieldset>}
      {active.kind === "wait" && <><label>Wait in seconds (zero waits for an event)</label><input type="number" min={0} value={active.duration_seconds}
        onChange={e => change({ ...draft, steps: updateStep(draft.steps, active.id, step => ({ ...step, duration_seconds: Number(e.target.value) })) })} /></>}
      {["repeat", "map"].includes(active.kind) && <><label>Maximum repetitions</label><input type="number" min={1} max={10000} value={active.max_iterations}
        onChange={e => change({ ...draft, steps: updateStep(draft.steps, active.id, step => ({ ...step, max_iterations: Number(e.target.value) })) })} /></>}
      <p className="hint">Describe a branch, parallel task, or stopping rule in the conversation to update this structure.</p>
      <button className="danger small-button" onClick={() => {
        const remove = (list: Step[]): Step[] => list.filter(step => step.id !== active.id).map(step => ({ ...step, children: remove(step.children ?? []), otherwise: remove(step.otherwise ?? []) }));
        change({ ...draft, steps: remove(draft.steps) }); select("");
      }}>Remove step</button>
    </div>}
  </section>;
}
