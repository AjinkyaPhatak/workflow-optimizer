// Helpers over the backend workflow definition (the source of truth). Pure
// functions only: no React, no canvas types.

import type { NodeDefinition, Position, WorkflowDefinition, WorkflowEdge, WorkflowNode } from "@/types/api";

export const DEFINITION_SCHEMA_VERSION = 1;

export function emptyDefinition(): WorkflowDefinition {
  return { version: DEFINITION_SCHEMA_VERSION, nodes: [], edges: [], settings: {} };
}

/** Normalizes a definition loaded from the API (null collections become empty). */
export function normalizeDefinition(d: Partial<WorkflowDefinition> | null | undefined): WorkflowDefinition {
  const out: WorkflowDefinition = {
    version: d?.version ?? DEFINITION_SCHEMA_VERSION,
    nodes: (d?.nodes ?? []).map((n) => ({ ...n, config: n.config ?? {}, position: n.position ?? null })),
    edges: d?.edges ?? [],
    settings: d?.settings ?? {},
  };
  // Variables are optional in the schema; an empty list is the same as none.
  if (d?.variables && d.variables.length > 0) out.variables = d.variables.map((v) => ({ ...v, default: v.default ?? null }));
  return out;
}

/** Deterministic JSON (sorted keys) used for dirty tracking. */
export function stableStringify(value: unknown): string {
  if (value === null || typeof value !== "object") return JSON.stringify(value) ?? "null";
  if (Array.isArray(value)) return `[${value.map(stableStringify).join(",")}]`;
  const obj = value as Record<string, unknown>;
  return `{${Object.keys(obj)
    .filter((k) => obj[k] !== undefined)
    .sort()
    .map((k) => `${JSON.stringify(k)}:${stableStringify(obj[k])}`)
    .join(",")}}`;
}

function slug(s: string): string {
  return s.toLowerCase().replace(/[^a-z0-9]+/g, "_").replace(/^_+|_+$/g, "") || "node";
}

function randomSuffix(): string {
  const bytes = new Uint8Array(2);
  globalThis.crypto.getRandomValues(bytes);
  return Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("");
}

/** A short, readable, unique node ID such as "llm_a83f". */
export function newNodeId(type: string, existing: Iterable<string>): string {
  const taken = new Set(existing);
  for (;;) {
    const id = `${slug(type)}_${randomSuffix()}`;
    if (!taken.has(id)) return id;
  }
}

export function newEdgeId(existing: Iterable<string>): string {
  return newNodeId("edge", existing);
}

/** "LLM", then "LLM 2", "LLM 3", ... */
export function defaultNodeName(def: NodeDefinition, nodes: WorkflowNode[]): string {
  const names = new Set(nodes.map((n) => n.name));
  if (!names.has(def.name)) return def.name;
  for (let i = 2; ; i++) if (!names.has(`${def.name} ${i}`)) return `${def.name} ${i}`;
}

/** The config fields' declared defaults (empty defaults are left out so the
 * backend default applies). */
export function defaultConfig(def: NodeDefinition): Record<string, unknown> {
  const cfg: Record<string, unknown> = {};
  for (const f of def.config) {
    if (f.default === null || f.default === undefined || f.default === "") continue;
    cfg[f.name] = structuredClone(f.default);
  }
  return cfg;
}

export function createNode(def: NodeDefinition, position: Position, nodes: WorkflowNode[]): WorkflowNode {
  return {
    id: newNodeId(def.type, nodes.map((n) => n.id)),
    type: def.type,
    name: defaultNodeName(def, nodes),
    position: { x: Math.round(position.x), y: Math.round(position.y) },
    config: defaultConfig(def),
  };
}

export function edgesOf(definition: WorkflowDefinition, nodeId: string): WorkflowEdge[] {
  return definition.edges.filter((e) => e.source === nodeId || e.target === nodeId);
}
