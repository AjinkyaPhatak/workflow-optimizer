// The questions the Run window asks (Phase I1), from the workflow itself:
// Input cards with a Field name, declared variables, and run-input names
// the settings use ({{input.topic}}, or a bare {{topic}} nothing connects).

import { variableInputName } from "@/lib/workflow/variableInputs";
import type { WorkflowDefinition, WorkflowVariable } from "@/types/api";

export type RunFieldKind = "text" | "file" | "number" | "boolean" | "json";

export interface RunField {
  name: string;
  kind: RunFieldKind;
  /** Must be filled in (an Input field, a used name, a variable without a default). */
  required: boolean;
  variable?: WorkflowVariable;
}

const REFERENCE = /\{\{([A-Za-z_][A-Za-z0-9_.]*)\}\}/g;

function refsIn(value: unknown, out: string[][]) {
  if (typeof value === "string") {
    for (const m of value.matchAll(REFERENCE)) out.push(m[1].split("."));
  } else if (Array.isArray(value)) {
    for (const v of value) refsIn(v, out);
  } else if (value && typeof value === "object") {
    for (const v of Object.values(value)) refsIn(v, out);
  }
}

export function runFields(definition: Pick<WorkflowDefinition, "nodes" | "edges" | "variables">): RunField[] {
  const fields = new Map<string, RunField>();
  const add = (f: RunField) => {
    if (!fields.has(f.name)) fields.set(f.name, f);
  };
  for (const n of definition.nodes) {
    const field = typeof n.config.field === "string" ? n.config.field.trim() : "";
    if (n.type === "input" && field) add({ name: field, kind: n.config.field_type === "file" ? "file" : "text", required: true });
  }
  for (const v of definition.variables ?? []) {
    const kind: RunFieldKind = v.type === "string" ? "text" : v.type === "number" || v.type === "boolean" ? v.type : "json";
    add({ name: v.name, kind, required: v.default === null || v.default === undefined, variable: v });
  }
  const nodeIds = new Set(definition.nodes.map((n) => n.id));
  for (const n of definition.nodes) {
    const connected = new Set(definition.edges.filter((e) => e.target === n.id).map((e) => variableInputName(e.target_port)).filter(Boolean));
    const refs: string[][] = [];
    refsIn(n.config, refs);
    for (const [head, next] of refs) {
      if (head === "input") {
        if (next) add({ name: next, kind: "text", required: true });
      } else if (!nodeIds.has(head) && !connected.has(head)) {
        add({ name: head, kind: "text", required: true });
      }
    }
  }
  return [...fields.values()];
}

/** A typed-in answer as the run input value (variables keep their type). */
export function fieldValue(f: RunField, text: string): { set: boolean; value?: unknown } {
  if (text === "") return { set: false };
  switch (f.kind) {
    case "number": return { set: true, value: Number.isNaN(Number(text)) ? text : Number(text) };
    case "boolean": return { set: true, value: text === "true" ? true : text === "false" ? false : text };
    case "json":
      try {
        return { set: true, value: JSON.parse(text) };
      } catch {
        return { set: true, value: text };
      }
    default: return { set: true, value: text };
  }
}

/** The run input of the answers, and the required questions left empty. */
export function runInput(fields: RunField[], answers: Record<string, string>): { input: Record<string, unknown>; missing: string[] } {
  const input: Record<string, unknown> = {};
  const missing: string[] = [];
  for (const f of fields) {
    const v = fieldValue(f, (answers[f.name] ?? "").trim() === "" ? "" : answers[f.name]);
    if (v.set) input[f.name] = v.value;
    else if (f.required) missing.push(f.name);
  }
  return { input, missing };
}

/** "customer_name" -> "Customer name". */
export function questionLabel(name: string): string {
  const words = name.replace(/([a-z])([A-Z])/g, "$1 $2").split(/[_\s]+/).filter(Boolean).map((w) => w.toLowerCase());
  if (words.length === 0) return name;
  words[0] = words[0][0].toUpperCase() + words[0].slice(1);
  return words.join(" ");
}
