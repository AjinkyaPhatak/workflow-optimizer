// Discovery and insertion of {{variable}} references in text configuration.
// These are typing aids only: the backend resolver (internal/execution
// variables.go) stays authoritative and nothing here resolves a value.
//
// The options follow what the backend can resolve for a node:
//   {{input}} / {{input.<key>}}  the workflow's run input
//   {{<node_id>.<port>}}         an output port of an UPSTREAM node (the
//                                resolver rejects non-upstream nodes)

import type { NodeDefinition, ValueType, WorkflowDefinition } from "@/types/api";

export interface VariableOption {
  /** The reference without braces, e.g. "llm_1.response". */
  expression: string;
  label: string;
  type?: ValueType;
  description?: string;
}

export interface VariableGroup {
  id: "input" | "nodes";
  label: string;
  options: VariableOption[];
}

/** The run-input key the editor's Run panel starts with. */
const DEFAULT_INPUT_KEY = "query";

// Same identifier grammar as workflow.VariableReferences.
const REFERENCE = /\{\{([A-Za-z_][A-Za-z0-9_.]*)\}\}/g;
const PARTIAL = /^[A-Za-z0-9_.]*$/;

/** IDs of the nodes upstream of nodeId (nearest first), following edges. */
export function upstreamNodeIds(definition: WorkflowDefinition, nodeId: string): string[] {
  const seen = new Set<string>([nodeId]);
  const out: string[] = [];
  let frontier = [nodeId];
  while (frontier.length > 0) {
    const next: string[] = [];
    for (const id of frontier) {
      for (const e of definition.edges) {
        if (e.target !== id || seen.has(e.source)) continue;
        seen.add(e.source);
        out.push(e.source);
        next.push(e.source);
      }
    }
    frontier = next;
  }
  return out;
}

/** Input keys the workflow already references anywhere ({{input.x}}). */
function referencedInputKeys(definition: WorkflowDefinition): string[] {
  const keys = new Set<string>();
  const visit = (v: unknown) => {
    if (typeof v === "string") {
      for (const m of v.matchAll(REFERENCE)) {
        const [head, key] = m[1].split(".");
        if (head === "input" && key) keys.add(key);
      }
    } else if (Array.isArray(v)) v.forEach(visit);
    else if (v && typeof v === "object") Object.values(v).forEach(visit);
  };
  definition.nodes.forEach((n) => visit(n.config));
  return [...keys];
}

/** The references a field of nodeId can use, grouped for the picker. */
export function availableVariables(definition: WorkflowDefinition, catalog: Map<string, NodeDefinition>, nodeId: string): VariableGroup[] {
  const inputKeys = [DEFAULT_INPUT_KEY, ...referencedInputKeys(definition).filter((k) => k !== DEFAULT_INPUT_KEY)];
  const input: VariableGroup = {
    id: "input",
    label: "Workflow input",
    options: [
      { expression: "input", label: "Entire input", type: "object", description: "Everything the workflow was run with" },
      ...inputKeys.map((k) => ({ expression: `input.${k}`, label: k, description: `The "${k}" field of the run input` })),
    ],
  };
  const nodes: VariableGroup = { id: "nodes", label: "Previous nodes", options: [] };
  for (const id of upstreamNodeIds(definition, nodeId)) {
    const n = definition.nodes.find((x) => x.id === id);
    const def = n && catalog.get(n.type);
    if (!n || !def) continue;
    for (const p of def.outputs) {
      nodes.options.push({ expression: `${n.id}.${p.name}`, label: `${n.name} › ${p.name}`, type: p.type, description: p.description || undefined });
    }
  }
  return [input, nodes];
}

/** Filters options by what was typed after "{{" (prefix matches first). */
export function filterVariables(groups: VariableGroup[], query: string): VariableGroup[] {
  const q = query.trim().toLowerCase();
  if (!q) return groups;
  return groups.map((g) => {
    const scored = g.options
      .map((o) => {
        const e = o.expression.toLowerCase();
        const l = o.label.toLowerCase();
        const score = e.startsWith(q) ? 0 : e.includes(q) ? 1 : l.includes(q) ? 2 : -1;
        return { o, score };
      })
      .filter((x) => x.score >= 0)
      .sort((a, b) => a.score - b.score);
    return { ...g, options: scored.map((x) => x.o) };
  });
}

/** The unfinished "{{..." reference the caret is in, if any. */
export function findOpenReference(text: string, caret: number): { start: number; query: string } | null {
  const before = text.slice(0, caret);
  const start = before.lastIndexOf("{{");
  if (start < 0) return null;
  const query = before.slice(start + 2);
  if (!PARTIAL.test(query)) return null; // closed, or not a reference
  return { start, query };
}

/** Replaces text[start, caret) (and a dangling "...}}" right after the
 * caret) with {{expression}}. Returns the new text and caret position. */
export function insertReference(text: string, start: number, caret: number, expression: string): { text: string; caret: number } {
  const token = `{{${expression}}}`;
  const tail = text.slice(caret);
  const dangling = /^[A-Za-z0-9_.]*\}\}/.exec(tail);
  const rest = dangling ? tail.slice(dangling[0].length) : tail;
  return { text: text.slice(0, start) + token + rest, caret: start + token.length };
}
