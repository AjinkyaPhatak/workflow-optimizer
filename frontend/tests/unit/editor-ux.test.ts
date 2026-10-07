import { beforeEach, describe, expect, it } from "vitest";
import { placeBeside } from "@/components/ui/Popover";
import { copyFragment, pasteFragment } from "@/lib/workflow/clipboard";
import { describeIssues, issueEdgeIds } from "@/lib/workflow/issues";
import { fieldControl, fieldLabel } from "@/lib/workflow/labels";
import { availableVariables, filterVariables, findOpenReference, insertReference, upstreamNodeIds } from "@/lib/workflow/variables";
import { useEditorStore } from "@/stores/workflow-editor/store";
import type { WorkflowDefinition } from "@/types/api";
import { catalog, sampleDefinition } from "./fixtures";

const expressions = (d: WorkflowDefinition, id: string) => availableVariables(d, catalog, id).map((g) => g.options.map((o) => o.expression));

describe("variable picker", () => {
  it("offers the workflow input and only upstream nodes' declared outputs", () => {
    const d = sampleDefinition();
    expect(upstreamNodeIds(d, "out")).toEqual(["llm_1", "in"]);
    expect(expressions(d, "out")).toEqual([["input", "input.query"], ["llm_1.response", "llm_1.usage", "in.data"]]);
    // The resolver rejects non-upstream nodes, so the first node sees none.
    expect(expressions(d, "in")[1]).toEqual([]);
    expect(expressions(d, "llm_1")[1]).toEqual(["in.data"]);
  });

  it("lists input keys the workflow already references", () => {
    const d = sampleDefinition();
    d.nodes[2].config = { note: "for {{input.customer.email}}" };
    expect(expressions(d, "out")[0]).toEqual(["input", "input.query", "input.customer"]);
  });

  it("detects an unfinished {{ reference at the caret", () => {
    expect(findOpenReference("Hello {{ll", 10)).toEqual({ start: 6, query: "ll" });
    expect(findOpenReference("Hello {{", 8)).toEqual({ start: 6, query: "" });
    expect(findOpenReference("{{a}} done", 10)).toBeNull();
    expect(findOpenReference("{{a b", 5)).toBeNull();
    expect(findOpenReference("plain", 5)).toBeNull();
  });

  it("filters by what was typed, prefix matches first", () => {
    const groups = availableVariables(sampleDefinition(), catalog, "out");
    const [, nodes] = filterVariables(groups, "ll");
    expect(nodes.options.map((o) => o.expression)).toEqual(["llm_1.response", "llm_1.usage"]);
    const [input, none] = filterVariables(groups, "usage");
    expect(input.options).toEqual([]);
    expect(none.options.map((o) => o.expression)).toEqual(["llm_1.usage"]);
  });

  it("inserts the complete expression and swallows a dangling close", () => {
    expect(insertReference("Hi {{ll there", 3, 7, "llm_1.response")).toEqual({ text: "Hi {{llm_1.response}} there", caret: 21 });
    expect(insertReference("Hi {{ll}} there", 3, 7, "llm_1.response").text).toBe("Hi {{llm_1.response}} there");
    expect(insertReference("ab", 1, 1, "input")).toEqual({ text: "a{{input}}b", caret: 10 });
  });
});

describe("labels", () => {
  it("humanizes field names generically", () => {
    expect(fieldLabel("max_tokens")).toBe("Max tokens");
    expect(fieldLabel("credential_id")).toBe("Credential");
    expect(fieldLabel("timeout_ms")).toBe("Timeout (ms)");
    expect(fieldLabel("json_content")).toBe("JSON content");
    expect(fieldLabel("url")).toBe("URL");
  });

  it("picks controls from declared types", () => {
    expect(fieldControl({ name: "credential_id", type: "string" })).toBe("credential");
    expect(fieldControl({ name: "temperature", type: "number" })).toBe("number");
    expect(fieldControl({ name: "enabled", type: "boolean" })).toBe("boolean");
    expect(fieldControl({ name: "template", type: "string" })).toBe("multiline");
    expect(fieldControl({ name: "model", type: "string" })).toBe("text");
    expect(fieldControl({ name: "schema", type: "json" })).toBe("json");
  });
});

describe("validation presentation", () => {
  const d = () => {
    const def = sampleDefinition();
    def.edges.push({ id: "bad", source: "llm_1", source_port: "usage", target: "out", target_port: "value" });
    return def;
  };

  it("rewords backend findings with what, where and how to fix", () => {
    const issues = describeIssues(
      {
        valid: false,
        errors: [
          { code: "INVALID_NODE_CONFIG", message: "required configuration field is missing", node_id: "llm_1", port: "model" },
          { code: "MISSING_REQUIRED_INPUT", message: "required input has no connection", node_id: "out", port: "value" },
          { code: "INCOMPATIBLE_PORT_TYPES", message: "connected port types differ", node_id: "in", edge_id: "e1", port: "data" },
          { code: "MISSING_OUTPUT_NODE", message: "workflow has no exit node" },
          { code: "SOMETHING_NEW", message: "a new backend rule" },
        ],
        warnings: [],
      },
      d(),
      catalog,
    );
    expect(issues.map((i) => [i.title, i.message])).toEqual([
      ["Ask", "Model is required."],
      ["Out", 'Input "value" needs a connection.'],
      ["Input → Ask", 'Cannot connect json output "data" to string input "prompt".'],
      ["Workflow", "The workflow has no final node."],
      ["Workflow", "A new backend rule."],
    ]);
    expect(issues[0]).toMatchObject({ nodeId: "llm_1", field: "model", hint: "Set Model in the node's configuration." });
    expect(issues[2]).toMatchObject({ edgeId: "e1" });
    expect(issues[3].hint).toBe("Add Output.");
  });

  it("maps findings to the edges they concern", () => {
    const def = d();
    const issues = describeIssues(
      { valid: false, warnings: [], errors: [
        { code: "MULTIPLE_CONNECTIONS_NOT_ALLOWED", message: "input accepts one connection", node_id: "out", port: "value" },
      ] },
      def,
      catalog,
    );
    expect([...issueEdgeIds(issues, def).keys()].sort()).toEqual(["bad", "e2"]);
  });
});

describe("copy and paste", () => {
  beforeEach(() => useEditorStore.getState().load(sampleDefinition()));

  it("copies selected nodes with their inner edges and remaps references", () => {
    const d = sampleDefinition();
    d.nodes[2].config = { note: "{{llm_1.response}} and {{in.data}}" };
    const fragment = copyFragment(d, ["llm_1", "out"])!;
    expect(fragment.edges.map((e) => e.id)).toEqual(["e2"]);
    const { definition, nodeIds } = pasteFragment(d, fragment);
    expect(definition.nodes).toHaveLength(5);
    const [llm, out] = nodeIds.map((id) => definition.nodes.find((n) => n.id === id)!);
    expect(llm.name).toBe("Ask 2");
    expect(llm.position).toEqual({ x: 332, y: 72 });
    // References to copied nodes follow the copy; others are kept.
    expect(out.config.note).toBe(`{{${llm.id}.response}} and {{in.data}}`);
    expect(definition.edges.at(-1)).toMatchObject({ source: llm.id, target: out.id, source_port: "response", target_port: "value" });
  });

  it("selects all, pastes as one undo step", () => {
    const s = useEditorStore.getState;
    s().selectAll();
    expect(s().selectedNodeIds).toEqual(["in", "llm_1", "out"]);
    expect(s().selectedEdgeIds).toEqual(["e1", "e2"]);
    const ids = s().paste(copyFragment(s().definition, ["in"])!);
    expect(s().selectedNodeIds).toEqual(ids);
    expect(s().definition.nodes).toHaveLength(4);
    s().undo();
    expect(s().definition.nodes).toHaveLength(3);
  });
});

describe("popover placement", () => {
  const anchor = { left: 100, right: 300, top: 100, bottom: 200 };
  it("prefers the right side, then the left, and stays inside the viewport", () => {
    expect(placeBeside(anchor, 200, 150, 1200, 800)).toEqual({ left: 310, top: 100 });
    expect(placeBeside({ left: 900, right: 1100, top: 100, bottom: 200 }, 200, 150, 1200, 800)).toEqual({ left: 690, top: 100 });
    expect(placeBeside({ left: 100, right: 300, top: 700, bottom: 780 }, 200, 150, 1200, 800).top).toBe(642);
  });
});
