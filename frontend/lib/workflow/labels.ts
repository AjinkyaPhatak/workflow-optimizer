// Human-readable presentation of backend node metadata. Generic rules over
// names and types only: no per-node-type knowledge lives here.

import type { ConfigField, ConfigOption, NodeDefinition, ValueType } from "@/types/api";

/** The configuration field through which a node uses a workspace credential
 * (the convention every credential-using node definition follows). */
export const CREDENTIAL_FIELD = "credential_id";

const ACRONYMS = new Set(["url", "json", "http", "api", "id", "llm", "ai", "jq"]);

/** "max_tokens" -> "Max tokens", "timeout_ms" -> "Timeout (ms)",
 * "credential_id" -> "Credential", "json_content" -> "JSON content". */
export function fieldLabel(name: string): string {
  let n = name;
  let suffix = "";
  if (n.endsWith("_ms")) {
    n = n.slice(0, -3);
    suffix = " (ms)";
  } else if (n.endsWith("_id") && n.length > 3) {
    n = n.slice(0, -3);
  }
  const words = n.split(/[_\s]+/).filter(Boolean).map((w, i) => {
    const lower = w.toLowerCase();
    if (ACRONYMS.has(lower)) return lower.toUpperCase();
    return i === 0 ? lower[0].toUpperCase() + lower.slice(1) : lower;
  });
  return (words.join(" ") || name) + suffix;
}

/** The field's display name: the backend label, else derived from the name. */
export function labelOf(field: Pick<ConfigField, "name" | "label">): string {
  return field.label || fieldLabel(field.name);
}

/** The node's configuration with declared defaults filled in (what option
 * conditions are checked against, as in the backend validator). */
export function effectiveConfig(def: Pick<NodeDefinition, "config"> | undefined, config: Record<string, unknown>): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  for (const f of def?.config ?? []) if (f.default !== null && f.default !== undefined) out[f.name] = f.default;
  return { ...out, ...config };
}

/** The field's options that apply to this configuration (their "when"
 * conditions hold). */
export function optionsFor(field: Pick<ConfigField, "options">, effective: Record<string, unknown>): ConfigOption[] {
  return (field.options ?? []).filter((o) => Object.entries(o.when ?? {}).every(([k, v]) => effective[k] === v));
}

/** "string" -> "text", "json" -> "any JSON", ... for prose. */
export function typeLabel(t: ValueType): string {
  switch (t) {
    case "string": return "text";
    case "json": return "any";
    default: return t;
  }
}

/** "Requires a google credential (OAuth)." from a node's auth metadata. */
export function authNote(def: Pick<NodeDefinition, "auth">): string | undefined {
  const a = def.auth;
  if (!a) return undefined;
  const kind = a.credential_type === "OAUTH2" ? "connected account" : `${a.credential_type.toLowerCase().replace(/_/g, " ")} credential`;
  return `${a.required ? "Requires" : "Can use"} a ${a.provider} ${kind}.`;
}

export function usesCredential(def: Pick<NodeDefinition, "config">): ConfigField | undefined {
  return def.config.find((f) => f.name === CREDENTIAL_FIELD);
}

export type FieldControl = "credential" | "select" | "boolean" | "number" | "text" | "multiline" | "json";

const MULTILINE = /(template|text|prompt|body|content|expression|message|system|instructions)/;

/** Which control renders a config field, from its declared type and name. */
export function fieldControl(field: Pick<ConfigField, "name" | "type" | "options" | "multiline">): FieldControl {
  if (field.name === CREDENTIAL_FIELD) return "credential";
  if (field.options && field.options.length > 0) return "select";
  if (field.type === "boolean") return "boolean";
  if (field.type === "number") return "number";
  if (field.type === "string") return field.multiline || MULTILINE.test(field.name) ? "multiline" : "text";
  return "json";
}

/** Side-effect metadata as a short sentence, or null for pure nodes. */
export function sideEffectsNote(side: string): string | null {
  if (side === "unsafe") return "Changes external systems; a failed run is not retried automatically.";
  if (side === "idempotent") return "Changes external systems; retries are deduplicated.";
  return null;
}
