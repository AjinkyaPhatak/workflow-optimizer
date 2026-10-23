// The top palette bar's model (Phase H1): tabs of tiles built from the
// backend node catalog. An app (integration) is one tile whose actions are
// chosen from a menu; every other node is its own tile.

import { actionName, searchNodes } from "@/features/nodes/catalog";
import { friendlyNodeName } from "@/lib/workflow/labels";
import type { NodeDefinition } from "@/types/api";

export type PaletteTab = "basics" | "ai" | "knowledge" | "apps" | "media" | "logic";

export const PALETTE_TABS: { id: PaletteTab; label: string; hint: string }[] = [
  { id: "basics", label: "Basics", hint: "Inputs, outputs and text" },
  { id: "ai", label: "AI", hint: "Ask an AI to write, summarize or decide" },
  { id: "knowledge", label: "Knowledge", hint: "Your documents and files" },
  { id: "apps", label: "Apps", hint: "Gmail, Docs, Sheets, Forms, Airtable, Notion, Discord and more" },
  { id: "media", label: "Media", hint: "Recordings and other media" },
  { id: "logic", label: "Logic", hint: "Branches, conditions and data shaping" },
];

/** Nodes that steer or reshape data rather than produce it. */
const LOGIC_TYPES = new Set(["condition", "switch", "merge", "transform", "json"]);

/** The tab a node belongs to. */
export function tabOf(d: Pick<NodeDefinition, "type" | "category" | "integration">): PaletteTab {
  if (d.integration || d.category === "integration") return "apps";
  if (d.category === "ai" || d.category === "llm") return "ai";
  if (d.category === "knowledge_base") return "knowledge";
  if (d.category === "multimodal") return "media";
  if (LOGIC_TYPES.has(d.type)) return "logic";
  return "basics";
}

/** One tile: a single node, or an app with its actions. */
export type Tile =
  | { kind: "node"; key: string; label: string; def: NodeDefinition }
  | { kind: "app"; key: string; label: string; integrationId: string; actions: NodeDefinition[] };

/** A tile's name: an app action's name ("Send Email"), else the step's
 * plain name ("Ask AI"). */
export function tileName(d: Pick<NodeDefinition, "type" | "name" | "integration">): string {
  return d.integration ? actionName(d) : friendlyNodeName(d);
}

/** The tiles of a tab, in catalog order: apps grouped by integration
 * (alphabetically) first. */
export function tilesFor(catalog: NodeDefinition[], tab: PaletteTab): Tile[] {
  const tiles: Tile[] = [];
  const apps = new Map<string, Extract<Tile, { kind: "app" }>>();
  for (const d of catalog) {
    if (tabOf(d) !== tab) continue;
    if (d.integration) {
      let app = apps.get(d.integration.id);
      if (!app) {
        app = { kind: "app", key: `app-${d.integration.id}`, label: d.integration.name, integrationId: d.integration.id, actions: [] };
        apps.set(d.integration.id, app);
      }
      app.actions.push(d);
    } else {
      tiles.push({ kind: "node", key: d.type, label: tileName(d), def: d });
    }
  }
  if (tab !== "apps") return tiles;
  const appTiles = [...apps.values()].sort((a, b) => a.label.localeCompare(b.label));
  return [...appTiles, ...tiles];
}

/** Search results: matching nodes as single tiles (an app action is named
 * "Gmail: Send Email"). */
export function searchTiles(catalog: NodeDefinition[], query: string): Tile[] {
  return searchNodes(catalog, query).map((d) => ({ kind: "node", key: d.type, label: d.integration ? d.name : tileName(d), def: d }));
}
