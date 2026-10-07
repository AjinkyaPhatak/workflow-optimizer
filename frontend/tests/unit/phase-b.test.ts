import { beforeEach, describe, expect, it } from "vitest";
import { searchNodes } from "@/features/nodes/catalog";
import { normalizeDefinition, stableStringify } from "@/lib/workflow/definition";
import { describeIssues } from "@/lib/workflow/issues";
import { effectiveConfig, fieldControl, labelOf, optionsFor } from "@/lib/workflow/labels";
import { fromCanvas, toCanvas } from "@/lib/workflow/mapping";
import { availableVariables } from "@/lib/workflow/variables";
import { useEditorStore } from "@/stores/workflow-editor/store";
import type { ConfigField, NodeDefinition } from "@/types/api";
import { catalog, catalogList, sampleDefinition } from "./fixtures";

const modelField: ConfigField = {
  name: "model", type: "string", required: true, default: "gpt-5", description: "", label: "Model", allow_custom: true,
  options: [
    { value: "gpt-5", label: "GPT-5", when: { provider: "openai" } },
    { value: "o-1", label: "O1", when: { provider: "other" } },
  ],
};
const llmWithOptions: NodeDefinition = {
  ...catalog.get("llm")!,
  config: [{ name: "provider", type: "string", required: true, default: "openai", description: "" }, modelField],
};

describe("config field metadata", () => {
  it("filters options by their conditions against the effective configuration", () => {
    expect(optionsFor(modelField, effectiveConfig(llmWithOptions, {})).map((o) => o.value)).toEqual(["gpt-5"]);
    expect(optionsFor(modelField, effectiveConfig(llmWithOptions, { provider: "other" })).map((o) => o.value)).toEqual(["o-1"]);
    expect(optionsFor({ options: [{ value: "GET", label: "GET" }] }, {}).length).toBe(1);
  });

  it("chooses controls and labels from the metadata", () => {
    expect(fieldControl(modelField)).toBe("select");
    expect(fieldControl({ name: "notes", type: "string", multiline: true })).toBe("multiline");
    expect(labelOf(modelField)).toBe("Model");
    expect(labelOf({ name: "max_tokens" })).toBe("Max tokens");
  });
});

describe("node search", () => {
  const names = (q: string) => searchNodes(catalogList, q).map((d) => d.type);
  it("matches name, type, category, description and ports; best first", () => {
    const withDesc = catalogList.map((d) => (d.type === "http" ? { ...d, description: "Calls an external API" } : d));
    expect(names("out")[0]).toBe("output");
    expect(searchNodes(withDesc, "external").map((d) => d.type)).toEqual(["http"]);
    expect(names("status_code")).toEqual(["http"]); // a port name
    expect(names("integration")).toEqual(["http"]); // a category
    expect(names("llm response")).toEqual(["llm"]); // every word must match
    expect(names("zzz")).toEqual([]);
    expect(names("")).toHaveLength(catalogList.length);
  });
});

describe("workflow variables", () => {
  beforeEach(() => useEditorStore.getState().load(sampleDefinition()));

  it("are offered by the variable picker", () => {
    const d = { ...sampleDefinition(), variables: [{ name: "customer_name", type: "string" as const, default: "", description: "Name" }] };
    const groups = availableVariables(d, catalog, "out");
    expect(groups.map((g) => g.id)).toEqual(["input", "variables", "nodes"]);
    expect(groups[1].options).toEqual([{ expression: "customer_name", label: "customer_name", type: "string", description: "Name" }]);
  });

  it("are part of the definition: normalized, kept by the canvas mapper, undoable", () => {
    const vars = [{ name: "tone", type: "string" as const, default: null }];
    expect(normalizeDefinition({ ...sampleDefinition(), variables: vars }).variables).toEqual(vars);
    // A definition without variables stays without the key (unchanged JSON).
    expect("variables" in normalizeDefinition(sampleDefinition())).toBe(false);
    expect("variables" in normalizeDefinition({ ...sampleDefinition(), variables: [] })).toBe(false);

    const s = useEditorStore.getState;
    s().setVariables(vars);
    expect(s().definition.variables).toEqual(vars);
    const c = toCanvas(s().definition);
    expect(fromCanvas(c.nodes, c.edges, s().definition).variables).toEqual(vars);
    // Moving a node keeps the variables.
    s().moveNode("in", { x: 5, y: 5 });
    expect(s().definition.variables).toEqual(vars);
    s().undo();
    s().undo();
    expect(s().definition.variables).toBeUndefined();
    expect(stableStringify(s().definition)).toBe(stableStringify(normalizeDefinition(sampleDefinition())));
  });

  it("explains backend variable and option findings", () => {
    const issues = describeIssues({ valid: false, warnings: [], errors: [
      { code: "INVALID_VARIABLE", message: "duplicate variable name", details: { variable: "tone" } },
      { code: "INVALID_NODE_CONFIG", message: "configuration value is not one of the allowed options", node_id: "llm_1", port: "provider" },
      { code: "INVALID_NODE_CONFIG", message: "configuration value is out of range", node_id: "llm_1", port: "temperature" },
    ] }, sampleDefinition(), new Map([...catalog, ["llm", { ...catalog.get("llm")!, config: [
      { name: "provider", type: "string", required: true, default: "openai", description: "", label: "Provider", options: [{ value: "openai", label: "OpenAI" }] },
      { name: "temperature", type: "number", required: false, default: 0.2, description: "", label: "Temperature", min: 0, max: 2 },
    ] }]]));
    expect(issues.map((i) => i.message)).toEqual([
      'Variable "tone": Duplicate variable name.',
      "Provider must be one of the listed options.",
      "Temperature must be between 0 and 2.",
    ]);
    expect(issues[0]).toMatchObject({ variable: "tone", title: "Workflow variables" });
  });
});
