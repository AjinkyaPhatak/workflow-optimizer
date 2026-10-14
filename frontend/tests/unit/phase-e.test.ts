import { describe, expect, it } from "vitest";
import { buildTimeline } from "@/features/executions/timeline";
import type { ExecutionEvent } from "@/types/api";
import { definition, record } from "./debugger-fixtures";

const event = (node: string, reason: string): ExecutionEvent => ({
  id: `ev-${node}`, execution_id: "exec-1", node_id: node, type: "NODE_SKIPPED", timestamp: "2026-01-01T00:00:00Z", data: { reason },
});

describe("Phase E1: branches not taken", () => {
  it("shows a node on a branch not taken as skipped, not as not-run or reused", () => {
    const rows = buildTimeline("COMPLETED", definition, [record("in", "COMPLETED"), record("out", "COMPLETED")], [event("llm_1", "branch_not_taken")]);
    const llm = rows.find((r) => r.nodeId === "llm_1")!;
    expect(llm.status).toBe("SKIPPED");
    expect(llm.branchSkipped).toBe(true);
    expect(llm.notRun).toBe(false);
    expect(llm.reused).toBe(false);
  });

  it("keeps reused nodes from earlier attempts as before", () => {
    const rows = buildTimeline("COMPLETED", definition, [record("in", "COMPLETED")], [event("in", "completed_in_previous_attempt")]);
    const input = rows.find((r) => r.nodeId === "in")!;
    expect(input.reused).toBe(true);
    expect(input.branchSkipped).toBe(false);
    expect(input.status).toBe("COMPLETED");
  });
});

import { checkConnection, nodeOutputs } from "@/lib/workflow/ports";
import { useEditorStore } from "@/stores/workflow-editor/store";
import { catalog, sampleDefinition } from "./fixtures";

describe("Phase E2: node error handling", () => {
  const llm = catalog.get("llm")!;

  it("adds the error output only with error_output", () => {
    expect(nodeOutputs(llm, {}).map((p) => p.name)).not.toContain("error");
    expect(nodeOutputs(llm, { settings: { on_error: "continue" } }).map((p) => p.name)).not.toContain("error");
    expect(nodeOutputs(llm, { settings: { on_error: "error_output" } }).map((p) => p.name)).toContain("error");
  });

  it("stores settings, drops defaults, and removes error edges when the output goes away", () => {
    const def = sampleDefinition();
    def.nodes.push({ id: "alert", type: "output", name: "Alert", position: null, config: {} });
    useEditorStore.getState().load(def);
    const s = useEditorStore.getState();
    const llmId = def.nodes.find((n) => n.type === "llm")!.id;
    const out = "alert";
    s.setNodeSettings(llmId, { on_error: "error_output", retry_attempts: 3 });
    const node = () => useEditorStore.getState().definition.nodes.find((n) => n.id === llmId)!;
    expect(node().settings).toEqual({ on_error: "error_output", retry_attempts: 3 });
    expect(checkConnection({ source: llmId, sourcePort: "error", target: out, targetPort: "value" }, useEditorStore.getState().definition, catalog)).toEqual({ ok: true });
    expect(useEditorStore.getState().connect({ source: llmId, sourcePort: "error", target: out, targetPort: "value" }, catalog).ok).toBe(true);
    expect(useEditorStore.getState().definition.edges.some((e) => e.source === llmId && e.source_port === "error")).toBe(true);
    // Back to the default: the setting disappears and so does the error edge.
    useEditorStore.getState().setNodeSettings(llmId, { on_error: "stop", retry_attempts: undefined });
    expect(node().settings).toBeUndefined();
    expect(useEditorStore.getState().definition.edges.some((e) => e.source === llmId && e.source_port === "error")).toBe(false);
    expect(checkConnection({ source: llmId, sourcePort: "error", target: out, targetPort: "value" }, useEditorStore.getState().definition, catalog).ok).toBe(false);
  });
});
