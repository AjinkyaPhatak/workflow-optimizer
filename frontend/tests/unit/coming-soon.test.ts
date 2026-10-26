import { describe, expect, it } from "vitest";
import { searchTiles, tilesFor } from "@/features/nodes/palette";
import { comingSoonApps, unavailableApps } from "@/lib/workflow/availability";
import type { NodeDefinition } from "@/types/api";

function app(type: string, id: string, name: string, provider: string): NodeDefinition {
  return {
    type, name: `${name}: Do`, category: "integration", description: "", role: "", side_effects: "none", inputs: [], outputs: [], config: [],
    integration: { id, name, action: "do", category: "" }, auth: { required: true, provider, credential_type: "OAUTH2" },
  };
}
const catalog = [app("gmail.send", "gmail", "Gmail", "google"), app("airtable.create_record", "airtable", "Airtable", "airtable"),
  app("notion.search", "notion", "Notion", "notion")];

describe("apps not set up on the server are Coming soon", () => {
  it("are listed last as Coming soon tiles and left out of search", () => {
    const unavailable = unavailableApps(catalog, new Set(["google"]));
    expect([...unavailable].sort()).toEqual(["airtable", "notion"]);
    const tiles = tilesFor(catalog, "apps", unavailable);
    expect(tiles.map((t) => `${t.kind}:${t.label}`)).toEqual(["app:Gmail", "soon:Airtable", "soon:Notion"]);
    expect(searchTiles(catalog, "airtable", unavailable)).toHaveLength(0);
    expect(searchTiles(catalog, "gmail", unavailable)).toHaveLength(1);
  });

  it("mark nothing until the server's providers are known, and stop once set up", () => {
    expect(unavailableApps(catalog, null).size).toBe(0);
    expect(unavailableApps(catalog, new Set(["google", "airtable", "notion"])).size).toBe(0);
  });

  it("name the apps a template is waiting for", () => {
    const byType = new Map(catalog.map((d) => [d.type, d]));
    const unavailable = unavailableApps(catalog, new Set(["google"]));
    expect(comingSoonApps(["input", "llm", "airtable.create_record", "gmail.send"], byType, unavailable)).toEqual(["Airtable"]);
    expect(comingSoonApps(["input", "gmail.send"], byType, unavailable)).toEqual([]);
  });
});
