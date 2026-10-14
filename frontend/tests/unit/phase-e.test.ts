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
