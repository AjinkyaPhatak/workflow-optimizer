import type { ExecutionEvent, NodeExecutionDetails, WorkflowDefinition } from "@/types/api";

export const definition: WorkflowDefinition = {
  version: 1,
  settings: {},
  nodes: [
    { id: "in", type: "input", name: "Input", position: { x: 0, y: 0 }, config: {} },
    { id: "llm_1", type: "llm", name: "Answer", position: { x: 200, y: 0 }, config: { provider: "openai", model: "gpt-5-mini" } },
    { id: "out", type: "output", name: "Output", position: { x: 400, y: 0 }, config: {} },
  ],
  edges: [
    { id: "e1", source: "in", source_port: "data", target: "llm_1", target_port: "prompt" },
    { id: "e2", source: "llm_1", source_port: "response", target: "out", target_port: "value" },
  ],
};

let seq = 0;
export function record(nodeId: string, status: string, extra: Partial<NodeExecutionDetails> = {}): NodeExecutionDetails {
  seq++;
  return {
    id: `rec-${seq}`,
    execution_id: "exec-1",
    node_id: nodeId,
    node_type: nodeId === "llm_1" ? "llm" : nodeId === "in" ? "input" : "output",
    status,
    attempt: 1,
    execution_attempt: 1,
    created_at: `2026-01-01T00:00:0${seq}Z`,
    started_at: `2026-01-01T00:00:0${seq}Z`,
    completed_at: status === "RUNNING" ? null : `2026-01-01T00:00:0${seq}Z`,
    duration_ms: status === "RUNNING" ? null : 120,
    error: null,
    input: { ports: { prompt: "What is quantum computing?" }, config: { model: "gpt-5-mini" } },
    output: status === "COMPLETED" ? { response: "An answer" } : undefined,
    provider: nodeId === "llm_1" ? "openai" : null,
    model: nodeId === "llm_1" ? "gpt-5-mini-2025" : null,
    usage: nodeId === "llm_1" && status === "COMPLETED" ? { input_tokens: 812, output_tokens: 231, total_tokens: 1043 } : null,
    estimated_cost_usd: nodeId === "llm_1" && status === "COMPLETED" ? 0.0012 : null,
    ...extra,
  };
}

export function ev(type: ExecutionEvent["type"], nodeId: string | null = null, data: Record<string, unknown> = {}): ExecutionEvent {
  seq++;
  return { id: `ev-${seq}`, execution_id: "exec-1", node_id: nodeId, type, timestamp: `2026-01-01T00:00:${String(seq).padStart(2, "0")}Z`, data };
}

export const rateLimited = { code: "RATE_LIMITED", message: "provider rate limit exceeded", retryable: true, node_id: "llm_1" };
