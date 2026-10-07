import { beforeEach, describe, expect, it } from "vitest";
import { isDirty, useEditorStore } from "@/stores/workflow-editor/store";
import { catalog, def, sampleDefinition } from "./fixtures";

const store = () => useEditorStore.getState();
const nodeIds = () => store().definition.nodes.map((n) => n.id);

beforeEach(() => store().load(null));

describe("editor store", () => {
  it("creates nodes from the catalog definition", () => {
    const id = store().addNode(def("llm"), { x: 400.4, y: 250.6 })!;
    const node = store().definition.nodes[0];
    expect(id).toMatch(/^llm_[0-9a-f]{4}$/);
    expect(node).toEqual({
      id, type: "llm", name: "LLM", position: { x: 400, y: 251 },
      // Declared defaults only; empty/null defaults are left to the backend.
      config: { provider: "openai", model: "gpt-5", temperature: 0.2 },
    });
    expect(store().selectedNodeIds).toEqual([id]);
    const second = store().addNode(def("llm"), { x: 0, y: 0 })!;
    expect(second).not.toBe(id);
    expect(store().definition.nodes[1].name).toBe("LLM 2");
    expect(isDirty(store())).toBe(true);
  });

  it("deletes nodes together with their edges", () => {
    store().load(sampleDefinition());
    store().removeNodes(["llm_1"]);
    expect(nodeIds()).toEqual(["in", "out"]);
    expect(store().definition.edges).toEqual([]);
  });

  it("moves nodes, one history entry per drag", () => {
    store().load(sampleDefinition());
    const apply = store().applyNodeChanges;
    apply([{ type: "position", id: "in", position: { x: 5, y: 5 }, dragging: true }]);
    apply([{ type: "position", id: "in", position: { x: 50, y: 60 }, dragging: true }]);
    apply([{ type: "position", id: "in", dragging: false }]);
    expect(store().definition.nodes[0].position).toEqual({ x: 50, y: 60 });
    expect(store().past).toHaveLength(1);
    store().undo();
    expect(store().definition.nodes[0].position).toEqual({ x: 0, y: 40.5 });
    store().moveNode("out", { x: 1, y: 2 });
    expect(store().definition.nodes[2].position).toEqual({ x: 1, y: 2 });
  });

  it("creates edges between compatible ports and refuses the rest", () => {
    const inId = store().addNode(def("input"), { x: 0, y: 0 })!;
    const llmId = store().addNode(def("llm"), { x: 300, y: 0 })!;
    const httpId = store().addNode(def("http"), { x: 600, y: 0 })!;
    const ok = store().connect({ source: inId, sourcePort: "data", target: llmId, targetPort: "prompt" }, catalog);
    expect(ok).toEqual({ ok: true });
    expect(store().definition.edges).toEqual([
      { id: expect.stringMatching(/^edge_/), source: inId, source_port: "data", target: llmId, target_port: "prompt" },
    ]);
    // Single-valued input already connected, wrong types, wrong direction, self.
    const refused = [
      store().connect({ source: inId, sourcePort: "data", target: llmId, targetPort: "prompt" }, catalog),
      store().connect({ source: httpId, sourcePort: "status_code", target: llmId, targetPort: "system" }, catalog),
      store().connect({ source: llmId, sourcePort: "prompt", target: httpId, targetPort: "url" }, catalog),
      store().connect({ source: llmId, sourcePort: "response", target: llmId, targetPort: "system" }, catalog),
    ];
    expect(refused.map((r) => r.ok)).toEqual([false, false, false, false]);
    expect(store().definition.edges).toHaveLength(1);
  });

  it("deletes edges through canvas changes", () => {
    store().load(sampleDefinition());
    store().applyEdgeChanges([{ type: "select", id: "e2", selected: true }]);
    expect(store().selectedEdgeIds).toEqual(["e2"]);
    store().applyEdgeChanges([{ type: "remove", id: "e2" }]);
    expect(store().definition.edges.map((e) => e.id)).toEqual(["e1"]);
    expect(store().selectedEdgeIds).toEqual([]);
    expect(nodeIds()).toEqual(["in", "llm_1", "out"]);
  });

  it("tracks selection without touching the definition", () => {
    store().load(sampleDefinition());
    store().applyNodeChanges([{ type: "select", id: "llm_1", selected: true }, { type: "select", id: "in", selected: true }]);
    expect(store().selectedNodeIds.sort()).toEqual(["in", "llm_1"]);
    store().applyNodeChanges([{ type: "select", id: "in", selected: false }]);
    expect(store().selectedNodeIds).toEqual(["llm_1"]);
    expect(isDirty(store())).toBe(false);
    expect(store().past).toHaveLength(0);
  });

  it("changes configuration and names", () => {
    store().load(sampleDefinition());
    store().setConfigValue("llm_1", "model", "gpt-5-mini");
    store().setConfigValue("llm_1", "credential_id", undefined);
    store().setConfigValue("llm_1", "max_tokens", 256);
    store().renameNode("llm_1", "Answer");
    const llm = store().definition.nodes[1];
    expect(llm.name).toBe("Answer");
    expect(llm.config).toMatchObject({ model: "gpt-5-mini", max_tokens: 256 });
    expect("credential_id" in llm.config).toBe(false);
    // Unchanged values add no history.
    const steps = store().past.length;
    store().setConfigValue("llm_1", "model", "gpt-5-mini");
    expect(store().past).toHaveLength(steps);
  });

  it("undoes and redoes every kind of edit, and dirty follows the saved state", () => {
    store().load(sampleDefinition());
    const saved = store().definition;
    const inId = store().addNode(def("input"), { x: 9, y: 9 })!;
    store().setConfigValue("llm_1", "model", "other");
    store().removeEdges(["e1"]);
    store().removeNodes([inId]);
    expect(isDirty(store())).toBe(true);
    for (let i = 0; i < 4; i++) store().undo();
    expect(store().definition).toEqual(saved);
    expect(isDirty(store())).toBe(false);
    store().undo(); // nothing left
    expect(store().definition).toEqual(saved);
    store().redo();
    store().redo();
    expect(nodeIds()).toContain(inId);
    expect(store().definition.nodes[1].config.model).toBe("other");
    // A new edit clears the redo stack.
    store().renameNode("in", "Start");
    expect(store().future).toHaveLength(0);
    store().markSaved();
    expect(isDirty(store())).toBe(false);
  });

  it("refuses edits in read-only mode", () => {
    store().load(sampleDefinition(), "readonly");
    expect(store().addNode(def("llm"), { x: 0, y: 0 })).toBeNull();
    store().removeNodes(["in"]);
    store().setConfigValue("llm_1", "model", "x");
    store().applyNodeChanges([{ type: "position", id: "in", position: { x: 1, y: 1 } }]);
    expect(store().definition).toEqual(sampleDefinition());
  });
});
