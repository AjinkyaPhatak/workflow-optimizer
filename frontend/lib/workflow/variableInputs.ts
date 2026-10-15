// Variable inputs (Phase H4): typing {{name}} in a node's settings gives the
// node an input called "name"; whatever is connected there fills {{name}}.
// Mirrors workflow.VariableInput in the backend (edge target "var:<name>").

import type { PortDefinition, WorkflowDefinition, WorkflowNode } from "@/types/api";

export const VAR_PREFIX = "var:";

const REFERENCE = /\{\{([A-Za-z_][A-Za-z0-9_.]*)\}\}/g;
const NAME = /^[A-Za-z_][A-Za-z0-9_]*$/;

/** The variable name of a variable input port ("var:query" -> "query"). */
export function variableInputName(port: string): string | null {
  return port.startsWith(VAR_PREFIX) ? port.slice(VAR_PREFIX.length) : null;
}

/** Whether a variable input may have this name (as in the backend). */
export function validVariableInputName(name: string): boolean {
  return NAME.test(name) && name !== "input";
}

/** The first path segment of every {{reference}} in a value, in order. */
function referencedHeads(value: unknown, out: string[]) {
  if (typeof value === "string") {
    for (const m of value.matchAll(REFERENCE)) out.push(m[1].split(".")[0]);
  } else if (Array.isArray(value)) {
    for (const v of value) referencedHeads(v, out);
  } else if (value && typeof value === "object") {
    for (const v of Object.values(value)) referencedHeads(v, out);
  }
}

/** A node's variable inputs: the names its settings use that are not
 * {{input.…}} or another node's {{node.port}}, plus any that are already
 * connected; in order of first use. */
export function variableInputs(node: Pick<WorkflowNode, "id" | "config">, definition: Pick<WorkflowDefinition, "nodes" | "edges">): string[] {
  const nodeIds = new Set(definition.nodes.map((n) => n.id));
  const heads: string[] = [];
  referencedHeads(node.config, heads);
  const names = heads.filter((h) => h !== "input" && !nodeIds.has(h) && validVariableInputName(h));
  for (const e of definition.edges) {
    const name = e.target === node.id ? variableInputName(e.target_port) : null;
    if (name) names.push(name);
  }
  return [...new Set(names)];
}

/** The port shown for a variable input: any value, one connection. */
export function variablePort(name: string): PortDefinition {
  return { name: `${VAR_PREFIX}${name}`, type: "json", required: false, multiple: false, description: `Fills {{${name}}} in this step's settings` };
}
