// Which apps can be used (Coming soon otherwise): an app whose steps sign in
// with an OAuth provider needs that provider set up on the server. Apps with
// keys or no account are always available.

import type { NodeDefinition } from "@/types/api";

/** The integration IDs of apps that are not available yet. providers null
 * (not loaded) marks nothing. */
export function unavailableApps(catalog: Iterable<NodeDefinition>, providers: Set<string> | null): Set<string> {
  const out = new Set<string>();
  if (!providers) return out;
  for (const d of catalog) {
    const id = d.integration?.id;
    if (id && d.auth?.credential_type === "OAUTH2" && d.auth.provider && !providers.has(d.auth.provider)) out.add(id);
  }
  return out;
}

/** Whether a step type belongs to an app that is not available yet. */
export function isUnavailable(def: Pick<NodeDefinition, "integration"> | undefined, unavailable: Set<string>): boolean {
  return !!def?.integration && unavailable.has(def.integration.id);
}

/** The names of the not-yet-available apps a template uses, in order. */
export function comingSoonApps(nodeTypes: string[], catalog: Map<string, NodeDefinition>, unavailable: Set<string>): string[] {
  const names: string[] = [];
  for (const t of nodeTypes) {
    const i = catalog.get(t)?.integration;
    if (i && unavailable.has(i.id) && !names.includes(i.name)) names.push(i.name);
  }
  return names;
}
