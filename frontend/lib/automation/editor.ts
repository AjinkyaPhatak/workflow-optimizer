import type { Binding, Plan, Question, Step, ToolDescriptor } from "@/types/automation";

export type Position = { x: number; y: number };
export type Positions = Record<string, Position>;
export type ControlDescriptor = { kind: string; name: string; description?: string };
export type EditorLayout = { revision: number; positions: Positions };
export const emptyPlan = (): Plan => ({ schema_version: 2, name: "New workflow", summary: "", mode: "once", budget: { max_actions: 30, max_tokens: 50000, max_active_seconds: 600 }, steps: [], questions: [] });
// Go encodes unused slices as null. Normalize a presentation copy so opening a
// server draft never becomes a semantic edit or triggers an autosave.
export function normalizeCanvasPlan(plan: Plan): Plan {
  const question = (value: Question): Question => ({ ...value, options: value.options ?? [] });
  const steps = (values: Step[] | null | undefined): Step[] => (values ?? []).map(step => ({
    ...step, inputs: step.inputs ?? [], children: steps(step.children), otherwise: steps(step.otherwise),
    question: step.question ? question(step.question) : null,
  }));
  return { ...plan, steps: steps(plan.steps), questions: (plan.questions ?? []).map(question) };
}
export const allSteps = (steps: Step[] | null | undefined): Step[] => (steps ?? []).flatMap(step => [step, ...allSteps(step.children), ...allSteps(step.otherwise)]);
export const updateStep = (steps: Step[], id: string, update: (step: Step) => Step): Step[] => steps.map(step => step.id === id ? update(step) : ({ ...step, children: updateStep(step.children ?? [], id, update), otherwise: updateStep(step.otherwise ?? [], id, update) }));
export const literal = (name: string, value: unknown): Binding => ({ name, source: "literal", ref: "", value_json: JSON.stringify(value) });
export const newID = () => "step_" + crypto.randomUUID().replaceAll("-", "").slice(0, 12);
export function makeStep(tool?: ToolDescriptor, control?: ControlDescriptor): Step {
  const id = newID();
  const kind = control?.kind ?? "tool";
  const question: Question | null = kind === "ask" ? { id: "question_" + id, prompt: "What should happen next?", kind: "text", required: true, allow_other: false, options: [], example: "", min: null, max: null, step_id: id, input: "answer" } : null;
  return { id, name: tool?.name ?? control?.name ?? "New step", kind, tool: tool?.id ?? "", inputs: (tool?.inputs ?? []).filter(field => (field as typeof field & { default?: unknown }).default !== undefined).map(field => literal(field.name, (field as typeof field & { default?: unknown }).default)), children: [], otherwise: [], question,
    condition: ["branch", "repeat"].includes(kind) ? { left: literal("value_to_check", ""), op: "equals", right: literal("compare_with", "") } : null,
    max_iterations: ["map", "repeat"].includes(kind) ? 10 : 0, duration_seconds: kind === "wait" ? 60 : 0, workflow_ref: "" };
}
export function removeSteps(plan: Plan, ids: string[]): Plan {
  const removed = new Set<string>();
  for (const step of allSteps(plan.steps)) if (ids.includes(step.id)) allSteps([step]).forEach(s => removed.add(s.id));
  const visit = (steps: Step[]): Step[] => steps.filter(s => !removed.has(s.id)).map(s => ({ ...s, children: visit(s.children ?? []), otherwise: visit(s.otherwise ?? []) }));
  return { ...plan, steps: visit(plan.steps), questions: plan.questions.filter(q => !removed.has(q.step_id)) };
}
export function insertStep(plan: Plan, step: Step, parent = "", path: "children" | "otherwise" = "children"): Plan {
  if (!parent) return { ...plan, steps: [...plan.steps, step] };
  return { ...plan, steps: updateStep(plan.steps, parent, value => ({ ...value, [path]: [...value[path], step] })) };
}
export function moveStep(plan: Plan, id: string, parent: string, path: "children" | "otherwise", offset?: number): Plan {
  const selected = allSteps(plan.steps).find(s => s.id === id);
  if (!selected || allSteps([selected]).some(s => s.id === parent)) return plan;
  if (offset !== undefined) {
    const reorder = (list: Step[]): Step[] => {
      const index = list.findIndex(s => s.id === id);
      if (index >= 0) { const next = [...list]; const to = Math.max(0, Math.min(next.length - 1, index + offset)); next.splice(to, 0, next.splice(index, 1)[0]); return next; }
      return list.map(s => ({ ...s, children: reorder(s.children ?? []), otherwise: reorder(s.otherwise ?? []) }));
    };
    return { ...plan, steps: reorder(plan.steps) };
  }
  // Moving retains clarification questions and references; the checker handles
  // newly unavailable results without silently substituting another value.
  const clean = removeSteps(plan, [id]);
  return { ...insertStep(clean, selected, parent, path), questions: plan.questions };
}
export type Fragment = { steps: Step[]; questions: Question[] };
export function copySteps(plan: Plan, ids: string[]): Fragment {
  const selected = new Set(ids);
  const roots: Step[] = [];
  const visit = (steps: Step[]) => { for (const step of steps) { if (selected.has(step.id)) roots.push(step); else { visit(step.children ?? []); visit(step.otherwise ?? []); } } };
  visit(plan.steps);
  const included = new Set(allSteps(roots).map(s => s.id));
  return structuredClone({ steps: roots, questions: plan.questions.filter(q => included.has(q.step_id)) });
}
export function pasteSteps(plan: Plan, fragment: Fragment): { plan: Plan; ids: string[] } {
  const ids = new Map(allSteps(fragment.steps).map(step => [step.id, newID()]));
  const questionIDs = new Map(fragment.questions.map(q => [q.id, "question_" + newID()]));
  const templatePrefixes = new Map<string, string>();
  for (const step of allSteps(fragment.steps)) if (step.question) questionIDs.set(step.question.id, "question_" + newID());
  const rewrite = (binding: Binding): Binding => {
    const next = { ...binding };
    if (next.source === "step") {
      const [head, ...tail] = next.ref.split(".");
      if (ids.has(head)) next.ref = [ids.get(head), ...tail].join(".");
    }
    if (next.source === "answer" && questionIDs.has(next.ref)) next.ref = questionIDs.get(next.ref)!;
    if (next.source === "template") {
      const prefix = next.ref;
      if (!templatePrefixes.has(prefix)) templatePrefixes.set(prefix, ids.get(prefix) ?? "template_" + newID());
      next.ref = templatePrefixes.get(prefix)!;
      for (const question of fragment.questions) if (question.id.startsWith(prefix + "_")) questionIDs.set(question.id, next.ref + question.id.slice(prefix.length));
    }
    return next;
  };
  const visit = (steps: Step[]): Step[] => steps.map(step => ({ ...structuredClone(step), id: ids.get(step.id)!, inputs: step.inputs.map(rewrite),
    condition: step.condition ? { ...step.condition, left: rewrite(step.condition.left), right: rewrite(step.condition.right) } : null,
    question: step.question ? { ...step.question, id: questionIDs.get(step.question.id)!, step_id: ids.get(step.id)! } : null,
    children: visit(step.children ?? []), otherwise: visit(step.otherwise ?? []) }));
  const steps = visit(fragment.steps);
  const questions = fragment.questions.map(q => ({ ...structuredClone(q), id: questionIDs.get(q.id)!, step_id: ids.get(q.step_id)! }));
  return { plan: { ...plan, steps: [...plan.steps, ...steps], questions: [...plan.questions, ...questions] }, ids: steps.map(s => s.id) };
}

/** Lexical availability mirrors execution scopes, not canvas coordinates. */
export function availableSteps(plan: Plan, target: string, includeRepeatChildren = false): Step[] {
  let found: Step[] = [];
  const walk = (steps: Step[], inherited: Step[]) => {
    const available = [...inherited];
    for (const step of steps) {
      if (step.id === target) { found = includeRepeatChildren && step.kind === "repeat" ? [...available, ...step.children] : available; return true; }
      if (step.kind === "parallel") { for (const child of step.children ?? []) if (walk([child], available)) return true; }
      else if (walk(step.children ?? [], available)) return true;
      if (walk(step.otherwise ?? [], available)) return true;
      available.push(step);
    }
    return false;
  };
  walk(plan.steps, []); return found;
}
export function resultFields(step: Step, tools: ToolDescriptor[]) {
  return step.kind === "tool" ? (tools.find(t => t.id === step.tool)?.outputs ?? []) : [{ name: "result", type: "json", description: "Result", required: false }];
}
export type ProjectedNode = { id: string; position: Position; step?: Step; label?: string };
export type ProjectedEdge = { id: string; source: string; target: string; sourceHandle: string; targetHandle: string; label?: string; kind: "control" | "data" | "loop" };
export function projectPlan(plan: Plan): { nodes: ProjectedNode[]; edges: ProjectedEdge[] } {
  const nodes: ProjectedNode[] = []; const edges: ProjectedEdge[] = [];
  const connect = (source: string | undefined, target: string, label?: string, kind: ProjectedEdge["kind"] = "control") => {
    if (source) edges.push({ id: [kind, source, target, label ?? ""].join(":"), source, target, label, kind, sourceHandle: "flow-out", targetHandle: "flow-in" });
  };
  const walk = (list: Step[], x: number, y: number, source?: string, label?: string): { tail?: string; x: number; bottom: number } => {
    let previous = source, bottom = y + 400;
    for (const step of list) {
      nodes.push({ id: step.id, position: { x, y }, step }); connect(previous, step.id, label); label = undefined;
      let tail = step.id;
      if (["branch", "parallel", "map", "repeat"].includes(step.kind)) {
        const join = "__join_" + step.id;
        const paths = step.kind === "branch" ? [{ list: step.children, label: "Yes" }, { list: step.otherwise, label: "Otherwise" }]
          : step.kind === "parallel" ? (step.children ?? []).map((child, i) => ({ list: [child], label: "Path " + (i + 1) }))
          : [{ list: step.children, label: step.kind === "map" ? "For each item" : "Repeat" }];
        let nextY = y, right = x + 360;
        const exits: { tail: string; label?: string }[] = [];
        for (const path of paths) {
          const result = walk(path.list ?? [], x + 360, nextY, step.id, path.label);
          exits.push({ tail: result.tail ?? step.id, label: path.list?.length ? undefined : path.label });
          right = Math.max(right, result.x); nextY = result.bottom + 40;
        }
        if (!paths.length) exits.push({ tail: step.id });
        nodes.push({ id: join, position: { x: right, y: y + 35 }, label: ["repeat", "map"].includes(step.kind) ? "Done" : "Continue" });
        for (const exit of exits) { connect(exit.tail, join, exit.label); if (["repeat", "map"].includes(step.kind) && exit.tail !== step.id) connect(exit.tail, step.id, "Next iteration", "loop"); }
        x = right + 180; bottom = Math.max(bottom, nextY - 40); tail = join;
      } else { x += 360; }
      previous = tail;
    }
    return { tail: previous, x, bottom };
  };
  walk(plan.steps ?? [], 60, 60);
  for (const step of allSteps(plan.steps)) for (const binding of step.inputs ?? []) if (binding.source === "step") {
    const [source, ...port] = binding.ref.split(".");
    if (nodes.some(n => n.id === source)) edges.push({ id: "data:" + source + ":" + step.id + ":" + binding.name, source, target: step.id, sourceHandle: "out:" + port[0], targetHandle: "in:" + binding.name, label: undefined, kind: "data" });
  }
  return { nodes, edges };
}
