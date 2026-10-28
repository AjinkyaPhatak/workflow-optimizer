"use client";

import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { Background, Controls, MarkerType, MiniMap, ReactFlow, ReactFlowProvider, useReactFlow, type Connection, type Edge, type NodeChange } from "@xyflow/react";
import type { Binding, Compilation, Plan, Step, ToolDescriptor, ToolField } from "@/types/automation";
import { allSteps, availableSteps, copySteps, emptyPlan, insertStep, makeStep, moveStep, normalizeCanvasPlan, pasteSteps, projectPlan, removeSteps, updateStep, type ControlDescriptor, type EditorLayout, type Fragment, type Position, type Positions } from "@/lib/automation/editor";
import { useCanvasDraft } from "@/features/assistant/useCanvasDraft";
import { PlanNode, fieldLabel, type PlanFlowNode } from "./canvas/PlanNode";
import { PlanPalette, PLAN_DRAG_TYPE, type PaletteItem } from "./canvas/PlanPalette";
import { BindingEditor } from "./canvas/BindingEditor";
import "./canvas/canvas.css";
export { allSteps } from "@/lib/automation/editor";

export interface WorkflowDraftProps {
  plan: Plan | null; tools: ToolDescriptor[]; comparisons?: { label: string; value: string }[];
  selectedStep?: string; onSelectStep?: (id: string) => void; busy: boolean; onSave: (plan: Plan) => Promise<unknown>;
  sessionId?: string; revision?: number; controls?: ControlDescriptor[]; layout?: EditorLayout;
  onLayoutSave?: (positions: Positions) => Promise<unknown>; onManualEdit?: () => void;
  onFlushReady?: (flush: (() => Promise<void>) | null) => void; headerActions?: ReactNode;
  checkPlan?: (plan: Plan) => Promise<Compilation>; workflowVersions?: { id: string; name: string }[];
  completedSteps?: string[]; readOnly?: boolean; onDirtyChange?: (dirty: boolean) => void;
}
const nodeTypes = { plan: PlanNode };
const bindingField = (name: string, description: string, type = "json"): ToolField => ({ name, type, description, required: true });
const isTyping = (target: EventTarget | null) => { const element = target as HTMLElement | null; return element?.isContentEditable || ["INPUT", "TEXTAREA", "SELECT"].includes(element?.tagName ?? ""); };

function CanvasEditor({ plan, tools, comparisons = [], selectedStep, onSelectStep, busy, onSave, sessionId, controls = [], layout, onLayoutSave, onManualEdit, onFlushReady, headerActions, checkPlan, workflowVersions = [], completedSteps = [], readOnly = false, onDirtyChange }: WorkflowDraftProps) {
  const editor = useCanvasDraft(plan, onSave, onManualEdit, sessionId);
  const draft = useMemo(() => normalizeCanvasPlan(editor.draft), [editor.draft]);
  const graph = useMemo(() => projectPlan(draft), [draft]);
  const locked = busy || readOnly || graph.ambiguousStepIDs.length > 0;
  const lockedRef = useRef(locked);
  useEffect(() => { lockedRef.current = locked; }, [locked]);
  const { change: commit, undo: undoDraft, redo: redoDraft, retry } = editor;
  const change = useCallback((next: Plan) => { if (!lockedRef.current) commit(next); }, [commit]);
  const undo = useCallback(() => { if (!lockedRef.current) undoDraft(); }, [undoDraft]);
  const redo = useCallback(() => { if (!lockedRef.current) redoDraft(); }, [redoDraft]);
  const flow = useReactFlow<PlanFlowNode>();
  const [selected, setSelected] = useState<string[]>([]), [positions, setPositions] = useState<Positions>(layout?.positions ?? {});
  const [measurements, setMeasurements] = useState<Record<string, { width: number; height: number }>>({});
  const [layoutDirty, setLayoutDirty] = useState(false), [layoutError, setLayoutError] = useState("");
  const [hint, setHint] = useState(""), [moveTarget, setMoveTarget] = useState("");
  const [selectedEdges, setSelectedEdges] = useState<string[]>([]);
  const [paletteOpen, setPaletteOpen] = useState(true);
  const clipboard = useRef<Fragment | null>(null);
  const latest = useRef(draft), layoutRef = useRef(positions), layoutSave = useRef(onLayoutSave);
  const scope = useRef(sessionId), fitted = useRef(false), layoutFlight = useRef(false), incomingLayout = useRef(layout);
  const region = useRef<HTMLDivElement>(null);
  useEffect(() => { latest.current = draft; }, [draft]);
  useEffect(() => { layoutSave.current = onLayoutSave; }, [onLayoutSave]);
  useEffect(() => {
    const before = scope.current; scope.current = sessionId;
    if (before === sessionId || !before && sessionId) return;
    setSelected([]); setMeasurements({}); setPositions(layout?.positions ?? {}); layoutRef.current = layout?.positions ?? {}; setLayoutDirty(false); setLayoutError(""); fitted.current = false;
  }, [sessionId, layout]);
  useEffect(() => {
    if (layout === incomingLayout.current) return;
    incomingLayout.current = layout;
    // An empty revision-zero layout can be the async loader's placeholder.
    if (layout && (layout.revision > 0 || Object.keys(layout.positions).length) && !layoutDirty && !layoutFlight.current) {
      setPositions(layout.positions); layoutRef.current = layout.positions;
    }
  }, [layout, layoutDirty]);
  useEffect(() => { if (selectedStep) setSelected(current => current.length === 1 && current[0] === selectedStep ? current : [selectedStep]); }, [selectedStep]);
  const select = useCallback((id: string) => { setSelected(id ? [id] : []); onSelectStep?.(id); }, [onSelectStep]);
  useEffect(() => { onFlushReady?.(editor.flush); return () => onFlushReady?.(null); }, [editor.flush, onFlushReady]);
  useEffect(() => { onDirtyChange?.(editor.dirty); }, [editor.dirty, onDirtyChange]);
  const steps = useMemo(() => allSteps(draft.steps), [draft.steps]);
  const active = graph.nodes.find(node => node.id === selected[0])?.step;
  const tool = tools.find(item => item.id === active?.tool);
  const available = (id: string, includeRepeatChildren = false) => availableSteps(draft, id, includeRepeatChildren).filter(step => !graph.ambiguousStepIDs.includes(step.id));
  function editStep(id: string, update: (step: Step) => Step) { change({ ...latest.current, steps: updateStep(latest.current.steps, id, update) }); }
  function setInput(step: Step, binding: Binding) {
    const next = latest.current;
    let questions = next.questions;
    if (binding.source === "answer" && !binding.ref) {
      const id = "question_" + step.id + "_" + binding.name.replace(/[^a-zA-Z0-9_-]/g, "_");
      const field = tools.find(tool => tool.id === step.tool)?.inputs.find(field => field.name === binding.name);
      const kind = field?.options?.length ? "choice" : field?.type === "number" ? "number" : field?.type === "boolean" ? "boolean" : "text";
      binding = { ...binding, ref: id };
      if (!questions.some(question => question.id === id)) questions = [...questions, { id, step_id: step.id, input: binding.name, prompt: "What should I use for " + fieldLabel(binding.name).toLowerCase() + "?", kind, required: true, allow_other: false, options: field?.options ?? [], example: "", min: null, max: null }];
    }
    if (binding.source === "template") {
      const prefix = binding.ref || "template_" + step.id + "_" + binding.name.replace(/[^a-zA-Z0-9_-]/g, "_");
      binding = { ...binding, ref: prefix };
      let text = "";
      try { const value: unknown = JSON.parse(binding.value_json); if (typeof value === "string") text = value; } catch { /* The checker handles incomplete text. */ }
      const tokens = text.match(/\[(?:amount|name|recipient|email|phone|value|insert[^\]]*|your[^\]]*)\]|<\s*(?:name|amount|email|recipient)\s*>|\{\{\s*[^}]+\s*\}\}/gi) ?? [];
      const additions = tokens.map((token, index) => ({ id: prefix + "_" + index, step_id: step.id, input: binding.name, prompt: "What should replace " + token + "?", kind: /amount/i.test(token) ? "number" as const : "text" as const, required: true, allow_other: false, options: [], example: token, min: null, max: null }));
      questions = [...questions, ...additions.filter(question => !questions.some(existing => existing.id === question.id))];
    }
    change({ ...next, steps: updateStep(next.steps, step.id, s => ({ ...s, inputs: [...s.inputs.filter(b => b.name !== binding.name), binding] })),
      questions: binding.source === "answer" || binding.source === "template" ? questions : questions.filter(q => q.step_id !== step.id || q.input !== binding.name) });
  }
  const nodes: PlanFlowNode[] = graph.nodes.map(node => {
    const descriptor = node.step && tools.find(item => item.id === node.step!.tool);
    const compact = descriptor?.inputs.filter(field => node.step?.inputs.some(binding => binding.name === field.name && binding.source === "literal")).slice(0, 2) ?? [];
    return { id: node.id, type: "plan", position: positions[node.id] ?? node.position, selectable: !!node.step, draggable: !!node.step && !locked,
      selected: selected.includes(node.id), measured: measurements[node.id], data: { step: node.step, tool: descriptor, label: node.label, kindName: controls.find(control => control.kind === node.step?.kind)?.name,
        comparison: comparisons.find(option => option.value === node.step?.condition?.op)?.label, onEdit: select, completed: completedSteps.includes(node.id),
        fieldEditor: node.step && compact.length ? <fieldset className="plan-inline-fields" disabled={locked}>{compact.map(field => <BindingEditor key={field.name} compact field={field} binding={node.step!.inputs.find(b => b.name === field.name)} steps={available(node.step!.id)} tools={tools} plan={draft} onChange={binding => setInput(node.step!, binding)} />)}</fieldset> : undefined } };
  });
  const edges: Edge[] = graph.edges.map(edge => ({ ...edge, type: "smoothstep", className: "plan-edge " + edge.kind,
    markerEnd: { type: MarkerType.ArrowClosed, width: 16, height: 16 }, selectable: edge.kind === "data", deletable: edge.kind === "data", selected: selectedEdges.includes(edge.id),
    style: edge.kind === "data" ? { strokeDasharray: "5 4", stroke: "var(--accent)" } : edge.kind === "loop" ? { strokeDasharray: "3 5" } : undefined,
    labelStyle: { fontSize: 11 }, labelBgPadding: [5, 3], labelBgBorderRadius: 4 }));
  useEffect(() => {
    if (!graph.nodes.length || fitted.current) return;
    fitted.current = true;
    const timer = setTimeout(() => {
      const first = graph.nodes[0];
      // Show readable cards on opening; long workflows remain pannable.
      void flow.setViewport({ x: 40 - first.position.x * .85, y: 40 - first.position.y * .85, zoom: .85 });
    }, 50);
    return () => clearTimeout(timer);
  }, [graph.nodes, flow]);
  const storePositions = useCallback((next: Positions) => { setPositions(next); layoutRef.current = next; setLayoutDirty(true); setLayoutError(""); }, []);
  useEffect(() => {
    const next = { ...layoutRef.current }; let changed = false;
    for (const node of graph.nodes) {
      if (!node.step || next[node.id]) continue;
      const position = { ...node.position };
      // Only new cards are placed. Existing cards keep their arrangement when
      // AI inserts or restructures steps elsewhere in the plan.
      for (let attempt = 0; attempt < 100 && Object.values(next).some(point => Math.abs(point.x - position.x) < 300 && Math.abs(point.y - position.y) < 350); attempt++) position.y += 420;
      next[node.id] = position; changed = true;
    }
    // Derived placement is presentation, not a user arrangement. In particular,
    // it must not race a saved layout that is still loading after navigation.
    if (changed) { setPositions(next); layoutRef.current = next; }
  }, [graph.nodes, layout]);
  useEffect(() => {
    if (!layoutDirty || !sessionId || !layoutSave.current || layoutFlight.current || layoutError || locked || editor.dirty) return;
    const timer = setTimeout(async () => {
      const snapshot = layoutRef.current; layoutFlight.current = true;
      try { const ids = new Set(allSteps(latest.current.steps).map(step => step.id)); await layoutSave.current?.(Object.fromEntries(Object.entries(snapshot).filter(([id]) => ids.has(id)))); if (snapshot === layoutRef.current) setLayoutDirty(false); }
      catch { setLayoutError("Your arrangement could not be saved. It is still visible here."); }
      finally { layoutFlight.current = false; }
    }, 600);
    return () => clearTimeout(timer);
  }, [positions, layoutDirty, layoutError, sessionId, locked, editor.dirty]);
  const onNodesChange = (changes: NodeChange<PlanFlowNode>[]) => {
    // Controlled nodes must retain React Flow's measurements across renders;
    // discarding them hides cards and invalidates their connection handles.
    if (changes.some(change => change.type === "dimensions" && change.dimensions)) setMeasurements(previous => {
      const next = { ...previous }; let changed = false;
      for (const item of changes) {
        if (item.type !== "dimensions" || !item.dimensions) continue;
        const before = previous[item.id];
        if (before?.width === item.dimensions.width && before?.height === item.dimensions.height) continue;
        next[item.id] = item.dimensions; changed = true;
      }
      return changed ? next : previous;
    });
    const selectedChanges = changes.filter(change => change.type === "select");
    if (selectedChanges.length) setSelected(previous => {
      const next = new Set(previous); for (const item of selectedChanges) if (item.type === "select") { if (item.selected) next.add(item.id); else next.delete(item.id); }
      return [...next].filter(id => graph.nodes.some(node => node.id === id && node.step));
    });
    const moved = changes.filter(change => change.type === "position" && change.position);
    if (moved.length && !locked) {
      const next = { ...layoutRef.current }; for (const item of moved) if (item.type === "position" && item.position) next[item.id] = item.position;
      storePositions(next);
    }
  };
  const add = (item: PaletteItem, position?: Position) => {
    if (locked) return;
    const step = makeStep(item.tool, item.control); change(insertStep(latest.current ?? emptyPlan(), step)); select(step.id);
    const rectangle = region.current?.getBoundingClientRect();
    if (!position && rectangle) position = flow.screenToFlowPosition({ x: rectangle.left + rectangle.width / 2 - 140, y: rectangle.top + Math.min(160, rectangle.height / 3) });
    if (position) storePositions({ ...layoutRef.current, [step.id]: position });
  };
  const paste = useCallback(() => {
    if (lockedRef.current || !clipboard.current?.steps.length) return;
    const result = pasteSteps(latest.current, clipboard.current); change(result.plan); setSelected(result.ids);
    const next = { ...layoutRef.current }; let i = 0;
    for (const step of allSteps(result.plan.steps).filter(s => result.ids.includes(s.id))) next[step.id] = { x: 100 + i++ * 340, y: 360 };
    storePositions(next);
  }, [change, storePositions]);
  useEffect(() => {
    const keyboard = (event: KeyboardEvent) => {
      if (isTyping(event.target) || !(event.target instanceof Node && region.current?.closest(".compiler-editor")?.contains(event.target)) && document.activeElement !== document.body) return;
      if (event.key === "Escape") { select(""); return; }
      if (locked) return;
      if (event.key === "Delete" || event.key === "Backspace") {
        if (selectedEdges.length) {
          event.preventDefault(); const removed = new Set(selectedEdges); const graph = projectPlan(latest.current);
          const list = graph.edges.filter(edge => edge.kind === "data" && removed.has(edge.id));
          let value = latest.current;
          for (const edge of list) value = { ...value, steps: updateStep(value.steps, edge.target, step => ({ ...step, inputs: step.inputs.filter(binding => binding.name !== edge.targetHandle.slice(3)) })) };
          change(value); setSelectedEdges([]);
        }
        if (selected.length) { event.preventDefault(); change(removeSteps(latest.current, selected)); select(""); } return; }
      if (!(event.ctrlKey || event.metaKey) || event.altKey) return;
      const key = event.key.toLowerCase();
      if (key === "a") { event.preventDefault(); setSelected(allSteps(latest.current.steps).map(step => step.id)); }
      if (key === "c") { if (selected.length) { event.preventDefault(); clipboard.current = copySteps(latest.current, selected); } }
      if (key === "v") { if (clipboard.current) { event.preventDefault(); paste(); } }
      if (key === "z") { event.preventDefault(); if (event.shiftKey) redo(); else undo(); }
      if (key === "y") { event.preventDefault(); redo(); }
      if (key === "s") { event.preventDefault(); void retry().catch(() => undefined); }
    };
    window.addEventListener("keydown", keyboard); return () => window.removeEventListener("keydown", keyboard);
  }, [selected, selectedEdges, select, change, paste, undo, redo, retry, locked]);
  async function connect(connection: Connection) {
    if (locked) return;
    const source = connection.source, target = connection.target;
    if (!source || !target || !connection.sourceHandle?.startsWith("out:") || !connection.targetHandle?.startsWith("in:")) return;
    const current = latest.current;
    if (!availableSteps(current, target).some(step => step.id === source)) { setHint("That result is not available on this path. Move the step earlier or describe the connection below."); return; }
    const name = connection.targetHandle.slice(3), ref = source + "." + connection.sourceHandle.slice(4);
    const proposed = { ...current, questions: current.questions.filter(question => question.step_id !== target || question.input !== name), steps: updateStep(current.steps, target, step => ({ ...step, inputs: [...step.inputs.filter(input => input.name !== name), { name, source: "step" as const, ref, value_json: "" }] })) };
    try {
      const checked = checkPlan ? await checkPlan(proposed) : null;
      if (lockedRef.current || scope.current !== sessionId) return;
      if (latest.current !== current) { setHint("The workflow changed while connecting. Try that connection again."); return; }
      if (checked?.diagnostics.some(diagnostic => diagnostic.step_id === target && ["REFERENCE", "PORT", "SCOPE"].includes(diagnostic.code))) { setHint("That result is not available here. Describe the connection below and I’ll help connect it."); return; }
      change(checked?.plan ?? proposed); setHint("");
    } catch { setHint("The connection could not be checked. Your existing steps are unchanged."); }
  }
  const updateActive = (update: (step: Step) => Step) => { if (active) editStep(active.id, update); };
  const scopeSteps = active ? available(active.id) : [];
  const conditionScope = active ? available(active.id, true) : [];
  const fields = tool?.inputs ?? (active?.kind === "map" ? [bindingField("items", "Choose the list to repeat over.", "array")] : []);
  const closeInspector = () => select("");
  return <section className="compiler-editor" aria-label="Workflow builder">
    <header className="editor-header compiler-editor-header"><input className="compiler-workflow-name" aria-label="Workflow name" disabled={locked} value={draft.name} onChange={event => change({ ...draft, name: event.target.value })} />
      <span className={"save-state " + (editor.error ? "error" : "")} role="status">{busy ? "Building…" : graph.ambiguousStepIDs.length ? "Draft needs repair" : editor.saving ? "Saving…" : editor.error ? "Changes not saved" : editor.dirty ? "Unsaved changes" : plan ? "All changes saved" : ""}</span><span className="spacer" />
      <button onClick={undo} disabled={locked || !editor.past.length} aria-label="Undo" title="Undo (Ctrl+Z)">↶</button><button onClick={redo} disabled={locked || !editor.future.length} aria-label="Redo" title="Redo (Ctrl+Shift+Z)">↷</button>
      <button onClick={() => { storePositions(Object.fromEntries(graph.nodes.filter(node => node.step).map(node => [node.id, node.position]))); void flow.fitView({ padding: .15, minZoom: .55, maxZoom: 1 }); }} disabled={locked || !steps.length}>Arrange</button>
      <button aria-expanded={paletteOpen} onClick={() => setPaletteOpen(!paletteOpen)}>{paletteOpen ? "Hide steps" : "Add steps"}</button>
      <button disabled={locked || !editor.dirty || editor.saving || !!editor.conflict} onClick={() => void editor.retry().catch(() => undefined)}>Save changes</button>{headerActions}
    </header>
    {paletteOpen && <PlanPalette tools={tools} controls={controls} onAdd={add} disabled={locked} />}
    {(editor.error || layoutError || hint) && <div className="compiler-notice" role="status">{editor.error || layoutError || hint}
      {editor.conflict ? <><button disabled={locked} onClick={() => editor.resolve(true)}>Keep my changes</button><button disabled={locked} onClick={() => editor.resolve(false)}>Use latest version</button></>
        : editor.error ? <button disabled={locked} onClick={() => void editor.retry().catch(() => undefined)}>Retry saving</button> : layoutError ? <button disabled={locked} onClick={() => setLayoutError("")}>Retry arrangement</button> : <button onClick={() => setHint("")}>Dismiss</button>}
    </div>}
    <div className={"compiler-editor-body " + (active ? "has-inspector" : "")}>
      <div className="canvas-wrap plan-canvas" data-testid="canvas" ref={region} tabIndex={0} onDragOver={event => { event.preventDefault(); event.dataTransfer.dropEffect = "copy"; }} onDrop={event => {
        event.preventDefault(); const id = event.dataTransfer.getData(PLAN_DRAG_TYPE); const tool = tools.find(tool => tool.id === id); const control = controls.find(control => "control:" + control.kind === id);
        if (tool || control) add({ id, name: tool?.name ?? control!.name, tool, control }, flow.screenToFlowPosition({ x: event.clientX, y: event.clientY }));
      }}>
        <ReactFlow<PlanFlowNode> nodes={nodes} edges={edges} nodeTypes={nodeTypes} onNodesChange={onNodesChange} onConnect={connection => void connect(connection)} onNodeClick={(_, node) => { setSelectedEdges([]); if (node.data.step) select(node.id); }} onPaneClick={() => { closeInspector(); setSelectedEdges([]); }} onEdgeClick={(event, edge) => { if (edge.id.startsWith("data:")) setSelectedEdges(current => event.ctrlKey || event.metaKey ? current.includes(edge.id) ? current.filter(id => id !== edge.id) : [...current, edge.id] : [edge.id]); }}
          onEdgesDelete={deleted => { if (locked) return; const targets = new Map(deleted.filter(edge => edge.id.startsWith("data:")).map(edge => [edge.target, edge.targetHandle?.slice(3)])); if (targets.size) change({ ...draft, steps: draft.steps.map(function visit(step): Step { return { ...step, inputs: step.inputs.filter(binding => targets.get(step.id) !== binding.name), children: step.children.map(visit), otherwise: step.otherwise.map(visit) }; }) }); }}
          minZoom={.15} maxZoom={2} snapToGrid snapGrid={[16, 16]} deleteKeyCode={null} nodesConnectable={!locked} elementsSelectable selectionOnDrag panOnDrag={[1, 2]} panOnScroll preventScrolling zoomOnDoubleClick={false} proOptions={{ hideAttribution: false }}>
          <Background gap={16} size={1} /><Controls showInteractive={false} /><MiniMap pannable zoomable position="bottom-right" />
        </ReactFlow>
        {!steps.length && <div className="canvas-empty"><div className="empty-card compiler-empty"><div className="empty-mark" aria-hidden><span /><span /><span /></div><h2>Your workflow starts here</h2><p className="muted">Describe it below, or add a step above.</p></div></div>}
        {busy && <div className="canvas-hint" role="status">Building your workflow… You can pan and zoom while I work.</div>}
        {!busy && graph.ambiguousStepIDs.length > 0 && <div className="canvas-hint" role="status">This draft needs repair before editing. You can still inspect every step.</div>}
      </div>
      {active && <aside className="config-panel compiler-config" role="region" aria-label={"Edit " + active.name}>
        <div className="row"><h3>{active.name}</h3><span className="spacer" /><button className="small-button" onClick={closeInspector}>Close</button></div>
        <fieldset className="compiler-edit-fields" disabled={locked}><p className="muted small">{tool?.description ?? controls.find(control => control.kind === active.kind)?.description}</p>
        <div className="field"><label htmlFor="plan-step-name">Step name</label><input id="plan-step-name" value={active.name} onChange={event => updateActive(step => ({ ...step, name: event.target.value }))} /></div>
        {fields.map(field => <BindingEditor key={active.id + field.name} field={field} binding={active.inputs.find(binding => binding.name === field.name)} steps={scopeSteps} tools={tools} plan={draft} onChange={binding => setInput(active, binding)} />)}
        {active.condition && <fieldset className="decision-editor"><legend>{active.kind === "repeat" ? "Stop repeating when" : "Continue on the Yes path when"}</legend>
          <BindingEditor field={bindingField("value_to_check", "Choose the value to check.")} binding={active.condition.left} steps={conditionScope} tools={tools} plan={draft} onChange={left => updateActive(step => ({ ...step, condition: { ...step.condition!, left } }))} />
          <label htmlFor="decision-rule">Rule</label><select id="decision-rule" value={active.condition.op} onChange={event => updateActive(step => ({ ...step, condition: { ...step.condition!, op: event.target.value } }))}>{!comparisons.some(option => option.value === active.condition!.op) && <option value={active.condition.op}>Choose a rule…</option>}{comparisons.map(option => <option key={option.value} value={option.value}>{option.label}</option>)}</select>
          <BindingEditor field={bindingField("compare_with", "For a number such as 150, enter just 150.")} binding={active.condition.right} steps={conditionScope} tools={tools} plan={draft} onChange={right => updateActive(step => ({ ...step, condition: { ...step.condition!, right } }))} />
        </fieldset>}
        {active.kind === "wait" && <div className="field"><label htmlFor="wait-seconds">Wait in seconds</label><input id="wait-seconds" type="number" min={0} max={31536000} value={active.duration_seconds} onChange={event => updateActive(step => ({ ...step, duration_seconds: Number(event.target.value) }))} /><p className="hint">Use zero to wait for an event.</p></div>}
        {["repeat", "map"].includes(active.kind) && <div className="field"><label htmlFor="repeat-limit">Maximum repetitions</label><input id="repeat-limit" type="number" min={1} max={10000} value={active.max_iterations} onChange={event => updateActive(step => ({ ...step, max_iterations: Number(event.target.value) }))} /></div>}
        {active.kind === "workflow" && <div className="field"><label htmlFor="linked-workflow">Saved workflow version</label>{workflowVersions.length ? <select id="linked-workflow" value={active.workflow_ref} onChange={event => updateActive(step => ({ ...step, workflow_ref: event.target.value }))}><option value="">Choose a version…</option>{active.workflow_ref && !workflowVersions.some(version => (version.id.startsWith("version:") ? version.id : "version:" + version.id) === active.workflow_ref) && <option value={active.workflow_ref}>Current saved version</option>}{workflowVersions.map(version => <option key={version.id} value={version.id.startsWith("version:") ? version.id : "version:" + version.id}>{version.name}</option>)}</select> : <input id="linked-workflow" value={active.workflow_ref} placeholder="Choose or describe a saved workflow below" onChange={event => updateActive(step => ({ ...step, workflow_ref: event.target.value }))} />}</div>}
        {active.question && <fieldset><legend>Ask a person</legend><label htmlFor="question-prompt">Question</label><textarea id="question-prompt" value={active.question.prompt} onChange={event => updateActive(step => ({ ...step, question: { ...step.question!, prompt: event.target.value } }))} />
          <label htmlFor="question-kind">Answer type</label><select id="question-kind" value={active.question.kind} onChange={event => updateActive(step => ({ ...step, question: { ...step.question!, kind: event.target.value as NonNullable<Step["question"]>["kind"] } }))}>{["text", "number", "boolean", "date", "choice", "contact", "file"].map(kind => <option key={kind} value={kind}>{fieldLabel(kind)}</option>)}</select>
          {active.question.kind === "number" && <div className="row"><label>Minimum<input aria-label="Minimum answer" type="number" value={active.question.min ?? ""} onChange={event => updateActive(step => ({ ...step, question: { ...step.question!, min: event.target.value === "" ? null : Number(event.target.value) } }))} /></label><label>Maximum<input aria-label="Maximum answer" type="number" value={active.question.max ?? ""} onChange={event => updateActive(step => ({ ...step, question: { ...step.question!, max: event.target.value === "" ? null : Number(event.target.value) } }))} /></label></div>}
          <label className="switch-row"><input type="checkbox" checked={active.question.required} onChange={event => updateActive(step => ({ ...step, question: { ...step.question!, required: event.target.checked } }))} />Required answer</label>
          {active.question.kind === "choice" && <><label htmlFor="question-options">Choices, one per line</label><textarea id="question-options" value={(active.question.options ?? []).map(option => option.label).join("\n")} onChange={event => updateActive(step => ({ ...step, question: { ...step.question!, options: event.target.value.split("\n").map(label => step.question!.options?.find(option => option.label === label) ?? { label, value: label }) } }))} /><label className="switch-row"><input type="checkbox" checked={active.question.allow_other} onChange={event => updateActive(step => ({ ...step, question: { ...step.question!, allow_other: event.target.checked } }))} />Allow another answer</label></>}
        </fieldset>}
        <fieldset><legend>Position in the workflow</legend><div className="row"><button onClick={() => change(moveStep(draft, active.id, "", "children", -1))}>Move earlier</button><button onClick={() => change(moveStep(draft, active.id, "", "children", 1))}>Move later</button></div>
          <label htmlFor="move-step">Move to a path</label><select id="move-step" value={moveTarget} onChange={event => setMoveTarget(event.target.value)}><option value="">Main sequence</option>{graph.nodes.filter(node => node.step && ["branch", "parallel", "map", "repeat"].includes(node.step.kind) && !allSteps([active]).some(child => child.id === node.step!.id)).flatMap(node => [<option key={node.id + ":children"} value={node.step!.id + ":children"}>{node.step!.name} · {node.step!.kind === "branch" ? "Yes" : "Steps"}</option>, ...(node.step!.kind === "branch" ? [<option key={node.id + ":otherwise"} value={node.step!.id + ":otherwise"}>{node.step!.name} · Otherwise</option>] : [])])}</select><button onClick={() => { const [parent, path] = moveTarget.split(":"); change(moveStep(draft, active.id, parent, path === "otherwise" ? "otherwise" : "children")); }}>Move step</button>
        </fieldset>
        <div className="row"><button onClick={() => { clipboard.current = copySteps(draft, [active.id]); paste(); }}>Duplicate</button><button className="danger" onClick={() => { change(removeSteps(draft, [active.id])); select(""); }}>Remove step</button></div></fieldset>
      </aside>}
    </div>
  </section>;
}
export function WorkflowDraft(props: WorkflowDraftProps) { return <ReactFlowProvider><CanvasEditor {...props} /></ReactFlowProvider>; }
