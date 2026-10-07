// Presentation helpers over the backend node catalog (GET /api/v1/nodes).
// Nothing here lists node types: categories and nodes come from the API.

import type { NodeDefinition } from "@/types/api";

const CATEGORY_ORDER = ["general", "ai", "llm", "integration"];

export function categoryLabel(category: string): string {
  if (category === "ai") return "AI";
  if (category === "llm") return "LLM";
  return category.replace(/_/g, " ").replace(/\b\w/g, (c) => c.toUpperCase()) || "Other";
}

/** Groups definitions by category (known categories first, then the rest). */
export function groupByCategory(defs: NodeDefinition[]): [string, NodeDefinition[]][] {
  const groups = new Map<string, NodeDefinition[]>();
  for (const d of defs) {
    const c = d.category || "other";
    groups.set(c, [...(groups.get(c) ?? []), d]);
  }
  const rank = (c: string) => (CATEGORY_ORDER.includes(c) ? CATEGORY_ORDER.indexOf(c) : CATEGORY_ORDER.length);
  return [...groups.entries()]
    .sort(([a], [b]) => rank(a) - rank(b) || a.localeCompare(b))
    .map(([c, ds]) => [c, ds.sort((x, y) => x.name.localeCompare(y.name))]);
}

export function categoryClass(category: string): string {
  return ["general", "ai", "llm", "integration"].includes(category) ? `cat-${category}` : "cat-other";
}

export function iconText(def: Pick<NodeDefinition, "name">): string {
  const words = def.name.split(/\s+/).filter(Boolean);
  return (words.length > 1 ? words[0][0] + words[1][0] : def.name.slice(0, 2)).toUpperCase();
}

/** MIME type of palette drags. */
export const NODE_DRAG_TYPE = "application/x-workflow-node-type";
