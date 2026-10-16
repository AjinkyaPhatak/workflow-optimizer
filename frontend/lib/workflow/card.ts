// What a node card shows on the canvas (Phase H3): the account or key it
// uses and its few main settings; everything else is under More settings.

import { CREDENTIAL_FIELD, fieldControl } from "@/lib/workflow/labels";
import type { ConfigField, NodeDefinition } from "@/types/api";

/** At most this many settings are edited on the card itself. */
export const CARD_FIELD_LIMIT = 3;

/** Fine-tuning settings that stay in More settings even when there is room
 * on the card. */
const ADVANCED = new Set([
  "temperature", "max_tokens", "image_urls", "image_file", "input_mode", "max_results", "days_ahead", "duration_minutes", "time_zone",
  "language", "timeout_ms", "top_k", "notify_attendees", "add_meet_link", "embed_title", "embed_description", "embed_color", "headers",
  "query_params", "response_format", "access_type", "cc", "bcc", "reply_to", "from_name", "view", "max_records", "max_responses",
  // Technical IDs, normally connected from an earlier step.
  "message_id", "thread_id", "event_id", "page_id", "file_id", "record_id",
]);

const hasDefault = (f: ConfigField) => !(f.default === null || f.default === undefined || f.default === "");

/** The settings edited on the card, most important first: required ones
 * that must be filled in, then what the user writes (prompts, messages,
 * the values of a row or record),
 * then required ones with a default, then the rest; never the credential
 * (shown as the account picker), fine-tuning or JSON settings. */
export function cardFields(def: NodeDefinition | undefined): ConfigField[] {
  if (!def) return [];
  const usable = def.config.filter((f) => f.name !== CREDENTIAL_FIELD && !ADVANCED.has(f.name) && fieldControl(f) !== "json");
  const rank = (f: ConfigField) => (f.required && !hasDefault(f) ? 0 : ["multiline", "key_value"].includes(fieldControl(f)) ? 1 : f.required ? 2 : 3);
  return usable.map((f, i) => ({ f, i })).sort((a, b) => rank(a.f) - rank(b.f) || a.i - b.i).slice(0, CARD_FIELD_LIMIT).map((x) => x.f);
}

/** The node's credential setting, if it uses one. */
export function credentialField(def: NodeDefinition | undefined): ConfigField | undefined {
  return def?.config.find((f) => f.name === CREDENTIAL_FIELD);
}

/** The other actions of a node's app, in catalog order (empty for nodes
 * that are not app actions). */
export function siblingActions(def: NodeDefinition | undefined, catalog: NodeDefinition[]): NodeDefinition[] {
  const app = def?.integration?.id;
  return app ? catalog.filter((d) => d.integration?.id === app) : [];
}
