import { describe, expect, it } from "vitest";
import { applyNodeChanges } from "@xyflow/react";
import { fromCanvas, toCanvas, type CanvasNode } from "@/lib/workflow/mapping";
import { stableStringify } from "@/lib/workflow/definition";
import { sampleDefinition } from "./fixtures";

describe("workflow <-> canvas mapping", () => {
  it("round-trips a definition without changing its meaning", () => {
    const original = sampleDefinition();
    const before = JSON.stringify(original);
    const { nodes, edges } = toCanvas(original);
    const back = fromCanvas(nodes, edges, original);

    // Identical: IDs, types, names, positions (incl. null), configs, edges,
    // ports, settings and order.
    expect(back).toEqual(original);
    expect(stableStringify(back)).toBe(stableStringify(original));
    // The input was not mutated, and the output shares no objects with it.
    expect(JSON.stringify(original)).toBe(before);
    back.nodes[1].config.nested = "changed";
    expect(original.nodes[1].config.nested).toEqual({ a: [1, 2, { b: "{{input.query}}" }] });
  });

  it("maps ports to handles and nothing else into the canvas", () => {
    const { nodes, edges } = toCanvas(sampleDefinition(), { selectedNodeIds: new Set(["llm_1"]) });
    expect(edges[0]).toMatchObject({ id: "e1", source: "in", sourceHandle: "data", target: "llm_1", targetHandle: "prompt" });
    expect(nodes.map((n) => n.selected)).toEqual([false, true, false]);
    // An unpositioned node is placed on the canvas but keeps no invented position.
    expect(nodes[2].position).toEqual(nodes[2].data.autoPosition);
  });

  it("writes moved positions back, including for previously unpositioned nodes", () => {
    const d = sampleDefinition();
    const { nodes, edges } = toCanvas(d);
    const moved = applyNodeChanges(
      [
        { type: "position", id: "in", position: { x: 10, y: 20 } },
        { type: "position", id: "out", position: { x: 500, y: 60 } },
      ],
      nodes,
    ) as CanvasNode[];
    const back = fromCanvas(moved, edges, d);
    expect(back.nodes[0].position).toEqual({ x: 10, y: 20 });
    expect(back.nodes[2].position).toEqual({ x: 500, y: 60 });
    expect(back.nodes[1]).toEqual(d.nodes[1]);
  });

  it("serializes to exactly the backend definition shape", () => {
    const d = sampleDefinition();
    const { nodes, edges } = toCanvas(d);
    const json = JSON.parse(JSON.stringify(fromCanvas(nodes, edges, d)));
    expect(Object.keys(json).sort()).toEqual(["edges", "nodes", "settings", "version"]);
    expect(Object.keys(json.nodes[0]).sort()).toEqual(["config", "id", "name", "position", "type"]);
    expect(Object.keys(json.edges[0]).sort()).toEqual(["id", "source", "source_port", "target", "target_port"]);
  });
});
