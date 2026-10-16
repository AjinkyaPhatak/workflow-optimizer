import { describe, expect, it } from "vitest";
import { COMING_SOON, tilesFor } from "@/features/nodes/palette";
import { cardFields } from "@/lib/workflow/card";
import type { ConfigField, NodeDefinition } from "@/types/api";

const field = (name: string, extra: Partial<ConfigField> = {}): ConfigField => ({ name, type: "string", required: false, default: "", description: "", ...extra });

function airtable(action: string, config: ConfigField[]): NodeDefinition {
  return {
    type: `airtable.${action}`, name: `Airtable: ${action}`, category: "integration", description: "", role: "", side_effects: "none",
    inputs: [], outputs: [], config: [field("credential_id", { required: true }), ...config],
    integration: { id: "airtable", name: "Airtable", action, category: "Airtable" }, auth: { required: true, provider: "airtable", credential_type: "OAUTH2" },
  };
}

const table = [field("base", { required: true, options_source: "bases" }), field("table", { options_source: "tables", depends_on: ["base"] })];
const values = field("fields", { type: "object", default: null, key_value: true, keys_source: "fields" });
const match = [field("match_field", { options_source: "fields" }), field("match_value")];

describe("Phase J apps", () => {
  it("puts the values to write on the card, and IDs and limits under More settings", () => {
    const create = airtable("create_record", [...table, values]);
    expect(cardFields(create).map((f) => f.name)).toEqual(["base", "fields", "table"]);
    const update = airtable("update_record", [...table, field("record_id"), ...match, values]);
    expect(cardFields(update).map((f) => f.name)).toEqual(["base", "fields", "table"]);
    const list = airtable("list_records", [...table, ...match, field("view"), field("max_records", { type: "number", default: 100 })]);
    expect(cardFields(list).map((f) => f.name)).toEqual(["base", "table", "match_field"]);
  });

  it("shows built apps as apps, never as coming soon", () => {
    const ids = COMING_SOON.map((c) => c.id);
    for (const built of ["google_docs", "google_forms", "airtable"]) expect(ids).not.toContain(built);
    const tiles = tilesFor([airtable("list_records", table)], "apps");
    expect(tiles[0]).toMatchObject({ kind: "app", label: "Airtable" });
    expect(tiles.filter((t) => t.kind === "soon").map((t) => t.label)).toContain("Trello");
  });
});
