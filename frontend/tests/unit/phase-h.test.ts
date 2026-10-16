import { beforeEach, describe, expect, it } from "vitest";
import { COMING_SOON, searchTiles, tabOf, tilesFor } from "@/features/nodes/palette";
import { cardFields, credentialField, siblingActions } from "@/lib/workflow/card";
import { createNode } from "@/lib/workflow/definition";
import { useEditorStore } from "@/stores/workflow-editor/store";
import type { ConfigField, NodeDefinition, PortDefinition, WorkflowDefinition } from "@/types/api";
import { catalogList } from "./fixtures";

const port = (name: string): PortDefinition => ({ name, type: "json", required: false, multiple: false, description: "" });
const field = (name: string, extra: Partial<ConfigField> = {}): ConfigField => ({ name, type: "string", required: false, default: "", description: "", ...extra });

function gmail(action: string, name: string, inputs: string[], outputs: string[], config: ConfigField[]): NodeDefinition {
  return {
    type: `gmail.${action}`, name: `Gmail: ${name}`, category: "integration", description: "", role: "", side_effects: "none",
    inputs: inputs.map(port), outputs: outputs.map(port), config: [field("credential_id", { required: true }), ...config],
    integration: { id: "gmail", name: "Gmail", action, category: "Google" }, auth: { required: true, provider: "google", credential_type: "OAUTH2" },
  };
}
const search = gmail("search", "Search Emails", ["query"], ["messages", "message_id"], [field("query"), field("max_results", { type: "number", default: 10 })]);
const send = gmail("send", "Send Email", ["body"], ["message_id", "thread_id"],
  [field("to", { required: true }), field("cc"), field("subject"), field("body", { multiline: true }), field("max_results", { type: "number", default: 5 })]);
const catalog = [...catalogList, search, send,
  { ...catalogList[0], type: "transcribe_audio", name: "Transcribe Audio", category: "multimodal", role: "" },
  { ...catalogList[0], type: "condition", name: "Condition", role: "" }];

describe("palette bar", () => {
  it("sorts nodes into tabs and an app into one tile with its actions", () => {
    expect(tabOf(search)).toBe("apps");
    expect(tabOf(catalogList.find((d) => d.type === "http")!)).toBe("apps");
    expect(tabOf(catalogList.find((d) => d.type === "llm")!)).toBe("ai");
    expect(tabOf(catalog.find((d) => d.type === "transcribe_audio")!)).toBe("media");
    expect(tabOf(catalog.find((d) => d.type === "condition")!)).toBe("logic");
    expect(tabOf(catalogList.find((d) => d.type === "input")!)).toBe("basics");

    const apps = tilesFor(catalog, "apps");
    expect(apps[0]).toMatchObject({ kind: "app", label: "Gmail", integrationId: "gmail" });
    expect(apps[0].kind === "app" && apps[0].actions.map((a) => a.type)).toEqual(["gmail.search", "gmail.send"]);
    expect(apps.find((t) => t.kind === "node")).toMatchObject({ label: "Web request (API)" });
    expect(apps.filter((t) => t.kind === "soon")).toHaveLength(COMING_SOON.length);
    // A coming-soon app disappears once it exists.
    const drive = { ...search, type: "google_drive.list", integration: { id: "google_drive", name: "Google Drive", action: "list" } };
    expect(tilesFor([...catalog, drive], "apps").some((t) => t.key === "soon-google_drive")).toBe(false);
    expect(tilesFor(catalog, "basics").map((t) => t.label)).toEqual(["Input", "Output"]);
    expect(searchTiles(catalog, "send email").map((t) => t.label)).toEqual(["Gmail: Send Email"]);
  });
});

describe("node cards", () => {
  it("edits the account and up to three main settings on the card, writing first", () => {
    expect(credentialField(send)?.name).toBe("credential_id");
    // Required without a default, then text to write; never the credential
    // or fine-tuning settings (Cc and Max results are under More settings).
    expect(cardFields(send).map((f) => f.name)).toEqual(["to", "body", "subject"]);
    expect(cardFields(search).map((f) => f.name)).toEqual(["query"]);
    const llm = { ...catalogList.find((d) => d.type === "llm")!, config: [...catalogList.find((d) => d.type === "llm")!.config,
      field("system", { multiline: true }), field("prompt", { multiline: true })] };
    expect(cardFields(llm).map((f) => f.name)).toEqual(["system", "prompt", "provider"]);
    expect(siblingActions(send, catalog).map((d) => d.type)).toEqual(["gmail.search", "gmail.send"]);
    expect(siblingActions(catalogList[0], catalog)).toEqual([]);
  });

  it("names app cards after the app", () => {
    const first = createNode(search, { x: 0, y: 0 }, []);
    expect(first.name).toBe("Gmail");
    expect(createNode(send, { x: 0, y: 0 }, [first]).name).toBe("Gmail 2");
  });
});

describe("switching an app card's action", () => {
  beforeEach(() => {
    const def: WorkflowDefinition = {
      version: 1,
      settings: {},
      nodes: [
        { id: "in", type: "input", name: "Input", position: { x: 0, y: 0 }, config: {} },
        { id: "g", type: "gmail.search", name: "Gmail", position: { x: 300, y: 0 }, config: { credential_id: "acc-1", query: "is:unread", max_results: 10 } },
        { id: "out", type: "output", name: "Output", position: { x: 600, y: 0 }, config: {} },
      ],
      edges: [
        { id: "e1", source: "in", source_port: "data", target: "g", target_port: "query" },
        { id: "e2", source: "g", source_port: "message_id", target: "out", target_port: "value" },
      ],
    };
    useEditorStore.getState().load(def);
  });

  it("keeps the account and shared settings, drops connections the new action cannot take", () => {
    useEditorStore.getState().changeNodeType("g", search, send);
    const { definition } = useEditorStore.getState();
    const g = definition.nodes.find((n) => n.id === "g")!;
    expect(g.type).toBe("gmail.send");
    expect(g.name).toBe("Gmail");
    // The account carries over; Query does not exist on Send; Max results
    // was the old default, so the new action's default applies.
    expect(g.config).toEqual({ credential_id: "acc-1", max_results: 5 });
    expect(definition.edges.map((e) => e.id)).toEqual(["e2"]);
    // One undoable step.
    useEditorStore.getState().undo();
    expect(useEditorStore.getState().definition.nodes.find((n) => n.id === "g")!.type).toBe("gmail.search");
    expect(useEditorStore.getState().definition.edges).toHaveLength(2);
  });

  it("keeps a name the user chose", () => {
    useEditorStore.getState().renameNode("g", "Unread mail");
    useEditorStore.getState().changeNodeType("g", search, send);
    expect(useEditorStore.getState().definition.nodes.find((n) => n.id === "g")!.name).toBe("Unread mail");
  });
});

describe("{{name}} inputs", () => {
  it("turns names typed in settings into inputs, not node or input references", async () => {
    const { variableInputs, variableInputName } = await import("@/lib/workflow/variableInputs");
    const definition = {
      nodes: [{ id: "in", type: "input", name: "Input", position: null, config: {} },
        { id: "ai", type: "llm", name: "AI", position: null, config: { prompt: "About {{topic}} for {{input.who}}, see {{in.data}} and {{topic}}", system: "Tone: {{tone.style}}" } }],
      edges: [{ id: "e", source: "in", source_port: "data", target: "ai", target_port: "var:extra" }],
    };
    expect(variableInputs(definition.nodes[1], definition)).toEqual(["topic", "tone", "extra"]);
    expect(variableInputName("var:topic")).toBe("topic");
    expect(variableInputName("prompt")).toBeNull();
  });

  it("lets anything connect to a {{name}} input, once", async () => {
    const { checkConnection } = await import("@/lib/workflow/ports");
    const { catalog: fixtures } = await import("./fixtures");
    const definition: WorkflowDefinition = {
      version: 1, settings: {},
      nodes: [{ id: "in", type: "input", name: "Input", position: null, config: {} }, { id: "h", type: "http", name: "API", position: null, config: {} },
        { id: "ai", type: "llm", name: "AI", position: null, config: {} }],
      edges: [],
    };
    expect(checkConnection({ source: "h", sourcePort: "status_code", target: "ai", targetPort: "var:code" }, definition, fixtures)).toEqual({ ok: true });
    expect(checkConnection({ source: "ai", sourcePort: "response", target: "in", targetPort: "var:x" }, definition, fixtures).ok).toBe(false);
    expect(checkConnection({ source: "h", sourcePort: "response", target: "ai", targetPort: "var:input" }, definition, fixtures).ok).toBe(false);
    definition.edges.push({ id: "e1", source: "in", source_port: "data", target: "ai", target_port: "var:code" });
    expect(checkConnection({ source: "h", sourcePort: "status_code", target: "ai", targetPort: "var:code" }, definition, fixtures))
      .toEqual({ ok: false, reason: 'Input "code" accepts one connection' });
  });
});
