import type { NodeDefinition, WorkflowDefinition } from "@/types/api";

const port = (name: string, type: string, required = true, multiple = false) => ({ name, type, required, multiple, description: "" });

/** A catalog shaped like GET /api/v1/nodes (subset of the real V1 nodes). */
export const catalogList: NodeDefinition[] = [
  { type: "input", name: "Input", category: "general", description: "", role: "entry", side_effects: "none",
    inputs: [], outputs: [port("data", "json")], config: [] },
  { type: "llm", name: "LLM", category: "ai", description: "", role: "", side_effects: "none",
    inputs: [port("prompt", "string"), port("system", "string", false)],
    outputs: [port("response", "string"), port("usage", "json", false)],
    config: [
      { name: "provider", type: "string", required: true, default: "openai", description: "" },
      { name: "model", type: "string", required: true, default: "gpt-5", description: "" },
      { name: "temperature", type: "number", required: false, default: 0.2, description: "" },
      { name: "max_tokens", type: "number", required: false, default: null, description: "" },
      { name: "credential_id", type: "string", required: false, default: "", description: "" },
    ] },
  { type: "output", name: "Output", category: "general", description: "", role: "exit", side_effects: "none",
    inputs: [port("value", "json")], outputs: [], config: [] },
  { type: "http", name: "HTTP Request", category: "integration", description: "", role: "", side_effects: "unsafe",
    inputs: [port("url", "string", false), port("headers", "object", false)],
    outputs: [port("response", "json"), port("status_code", "number")], config: [] },
  { type: "merge", name: "Merge", category: "general", description: "", role: "", side_effects: "none",
    inputs: [port("inputs", "json", true, true)], outputs: [port("merged", "json")], config: [] },
];

export const catalog = new Map(catalogList.map((d) => [d.type, d]));

export const def = (type: string) => catalog.get(type)!;

/** A definition as the backend stores it, including an unpositioned node,
 * nested config and settings. */
export function sampleDefinition(): WorkflowDefinition {
  return {
    version: 1,
    nodes: [
      { id: "in", type: "input", name: "Input", position: { x: 0, y: 40.5 }, config: {} },
      { id: "llm_1", type: "llm", name: "Ask", position: { x: 300, y: 40 },
        config: { provider: "openai", model: "gpt-5", temperature: 0.7, credential_id: "c-1", nested: { a: [1, 2, { b: "{{input.query}}" }] } } },
      { id: "out", type: "output", name: "Out", position: null, config: {} },
    ],
    edges: [
      { id: "e1", source: "in", source_port: "data", target: "llm_1", target_port: "prompt" },
      { id: "e2", source: "llm_1", source_port: "response", target: "out", target_port: "value" },
    ],
    settings: { timeout: "30s", tags: ["a"] },
  };
}
