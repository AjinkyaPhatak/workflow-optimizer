// What a workflow needs before it can run (Phase K): the accounts its app
// steps use and the AI keys its AI steps use, worked out from the steps
// themselves, plus a template's own notes ("a sheet with these columns").
// Also: filling in a step's account or key when there is only one choice.

import { CREDENTIAL_FIELD, providerName } from "@/lib/workflow/labels";
import type { ConnectedAccount, Credential, NodeDefinition, WorkflowDefinition } from "@/types/api";

export interface Requirement {
  key: string;
  kind: "account" | "key" | "note";
  label: string;
  /** Met already (accounts and keys only; notes are for the person). */
  done?: boolean;
  href?: string;
  action?: string;
}

/** Plain names of the services accounts are connected for. */
export const ACCOUNT_NAMES: Record<string, string> = { google: "Google", notion: "Notion", discord: "Discord", airtable: "Airtable" };

function keyProvider(node: { config: Record<string, unknown> }, def: NodeDefinition): string | undefined {
  if (!def.config.some((f) => f.name === CREDENTIAL_FIELD)) return undefined;
  const set = node.config.provider;
  if (typeof set === "string" && set) return set;
  const field = def.config.find((f) => f.name === "provider");
  return typeof field?.default === "string" ? field.default : undefined;
}

/** The provider of the account (OAuth) or key a step uses, if any. */
export function credentialNeed(node: { config: Record<string, unknown> }, def: NodeDefinition | undefined):
  { kind: "account" | "key"; provider: string } | null {
  if (!def) return null;
  if (def.auth?.credential_type === "OAUTH2" && def.auth.provider) return { kind: "account", provider: def.auth.provider };
  const p = def.type === "http" ? undefined : keyProvider(node, def);
  return p ? { kind: "key", provider: p } : null;
}

/** Accounts first (with the apps they are for), then AI keys, then notes. */
export function requirements(definition: Pick<WorkflowDefinition, "nodes">, catalog: Map<string, NodeDefinition>,
  have: { accounts: ConnectedAccount[]; credentials: Credential[] } | null, notes: string[] = []): Requirement[] {
  const accounts = new Map<string, Set<string>>();
  const keys = new Set<string>();
  for (const n of definition.nodes) {
    const def = catalog.get(n.type);
    const need = credentialNeed(n, def);
    if (need?.kind === "account") {
      const apps = accounts.get(need.provider) ?? new Set<string>();
      if (def?.integration?.name) apps.add(def.integration.name);
      accounts.set(need.provider, apps);
    } else if (need?.kind === "key") {
      keys.add(need.provider);
    }
  }
  const out: Requirement[] = [];
  for (const [provider, apps] of accounts) {
    const name = ACCOUNT_NAMES[provider] ?? provider;
    const list = [...apps].sort();
    const forApps = list.length && !(list.length === 1 && list[0] === name) ? ` (for ${list.join(", ")})` : "";
    out.push({
      key: `account-${provider}`, kind: "account", label: `Connect your ${name} account${forApps}`,
      done: have ? have.accounts.some((a) => a.provider === provider && a.status === "ACTIVE") : undefined,
      href: "/settings/connected-accounts", action: `Connect ${name}`,
    });
  }
  for (const provider of keys) {
    const free = provider === "gemini" || provider === "groq";
    out.push({
      key: `key-${provider}`, kind: "key", label: `Add ${free ? "a free" : "an"} ${providerName(provider)} key, so the AI steps can work`,
      done: have ? have.credentials.some((c) => c.provider === provider) : undefined,
      href: "/settings/credentials", action: "Add a key",
    });
  }
  notes.forEach((text, i) => out.push({ key: `note-${i}`, kind: "note", label: text }));
  return out;
}

/** Steps whose account or key is not chosen yet and has exactly one choice
 * (an active connected account, or the only key of that provider). */
export function credentialFills(definition: Pick<WorkflowDefinition, "nodes">, catalog: Map<string, NodeDefinition>,
  accounts: ConnectedAccount[], credentials: Credential[]): { nodeId: string; credentialId: string }[] {
  const out: { nodeId: string; credentialId: string }[] = [];
  for (const n of definition.nodes) {
    const current = n.config[CREDENTIAL_FIELD];
    if (typeof current === "string" && current.trim()) continue;
    const need = credentialNeed(n, catalog.get(n.type));
    if (!need) continue;
    const ids = need.kind === "account"
      ? accounts.filter((a) => a.provider === need.provider && a.status === "ACTIVE").map((a) => a.credential_id)
      : credentials.filter((c) => c.provider === need.provider).map((c) => c.id);
    if (ids.length === 1) out.push({ nodeId: n.id, credentialId: ids[0] });
  }
  return out;
}

/** The apps a workflow uses, by integration ID (for brand icons), in order. */
export function appsOf(nodeTypes: string[], catalog: Map<string, NodeDefinition>): { id: string; name: string }[] {
  const seen = new Map<string, string>();
  for (const t of nodeTypes) {
    const i = catalog.get(t)?.integration;
    if (i && !seen.has(i.id)) seen.set(i.id, i.name);
  }
  return [...seen].map(([id, name]) => ({ id, name }));
}
