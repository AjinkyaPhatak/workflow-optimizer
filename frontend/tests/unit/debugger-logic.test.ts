import { describe, expect, it, vi } from "vitest";
import { startPolling } from "@/features/executions/poller";
import { buildTimeline, failurePoint, formatCost, formatDuration } from "@/features/executions/timeline";
import { snapshotSettled, type DebuggerSnapshot } from "@/features/executions/useExecutionDebugger";
import type { ExecutionDetails } from "@/types/api";
import { definition, ev, rateLimited, record } from "./debugger-fixtures";

describe("execution timeline", () => {
  it("shows a running execution from persisted records only", () => {
    const rows = buildTimeline("RUNNING", definition, [record("in", "COMPLETED"), record("llm_1", "RUNNING")], []);
    expect(rows.map((r) => [r.nodeId, r.status])).toEqual([
      ["in", "COMPLETED"],
      ["llm_1", "RUNNING"],
      ["out", "PENDING"],
    ]);
    expect(rows[2].notRun).toBe(false); // still may run
    expect(rows[1].name).toBe("Answer");
  });

  it("shows a completed execution", () => {
    const rows = buildTimeline("COMPLETED", definition, [record("in", "COMPLETED"), record("llm_1", "COMPLETED"), record("out", "COMPLETED")], []);
    expect(rows.every((r) => r.status === "COMPLETED")).toBe(true);
    expect(rows[1].durationMs).toBe(120);
    expect(failurePoint(rows)).toBeNull();
  });

  it("identifies where a failed execution failed; unrun nodes are marked", () => {
    const rows = buildTimeline("FAILED", definition, [record("in", "COMPLETED"), record("llm_1", "FAILED", { error: rateLimited })], []);
    expect(failurePoint(rows)?.nodeId).toBe("llm_1");
    expect(rows[2]).toMatchObject({ nodeId: "out", status: "PENDING", notRun: true });
  });

  it("visualizes an in-place retry: attempt 1 failed, retry scheduled, attempt 2 completed", () => {
    const failed = record("llm_1", "FAILED", { error: rateLimited });
    const ok = record("llm_1", "COMPLETED", { attempt: 2 });
    const events = [
      ev("NODE_FAILED", "llm_1", { error: rateLimited }),
      ev("RETRY_SCHEDULED", "llm_1", { scope: "node", attempt: 2, delay_ms: 1000 }),
      ev("RETRY_STARTED", "llm_1", { scope: "node", attempt: 2 }),
    ];
    const rows = buildTimeline("COMPLETED", definition, [record("in", "COMPLETED"), failed, ok, record("out", "COMPLETED")], events);
    const llm = rows.find((r) => r.nodeId === "llm_1")!;
    expect(llm.status).toBe("COMPLETED");
    expect(llm.attempts.map((a) => [a.record.attempt, a.record.status])).toEqual([
      [1, "FAILED"],
      [2, "COMPLETED"],
    ]);
    expect(llm.attempts[0].retry).toEqual({ delayMs: 1000, scope: "node" });
    expect(llm.attempts[1].retry).toBeUndefined();
  });

  it("attributes an execution retry to the failed node and marks reused nodes", () => {
    const events = [
      ev("NODE_FAILED", "llm_1"),
      ev("RETRY_SCHEDULED", null, { scope: "execution", attempt: 2, delay_ms: 200 }),
      ev("RETRY_STARTED", null, { scope: "execution", attempt: 2 }),
      ev("NODE_SKIPPED", "in", { reason: "completed_in_previous_attempt" }),
    ];
    const rows = buildTimeline(
      "COMPLETED",
      definition,
      [record("in", "COMPLETED"), record("llm_1", "FAILED", { error: rateLimited }), record("llm_1", "COMPLETED", { attempt: 2, execution_attempt: 2 })],
      events,
    );
    expect(rows[0].reused).toBe(true);
    expect(rows[1].attempts[0].retry).toEqual({ delayMs: 200, scope: "execution" });
  });

  it("formats durations and estimated costs", () => {
    expect(formatDuration(42)).toBe("42 ms");
    expect(formatDuration(4820)).toBe("4.82 s");
    expect(formatDuration(null)).toBe("—");
    expect(formatCost(0.012)).toBe("$0.0120");
    expect(formatCost(null)).toBe("—");
  });
});

function snap(status: ExecutionDetails["status"], terminalEvent: boolean): DebuggerSnapshot {
  return {
    execution: { status } as ExecutionDetails,
    nodes: [],
    events: terminalEvent ? [ev("EXECUTION_COMPLETED")] : [ev("EXECUTION_STARTED")],
  };
}

describe("polling", () => {
  it("polls while running and stops after the terminal state is settled", async () => {
    vi.useFakeTimers();
    try {
      const sequence = [snap("PENDING", false), snap("RUNNING", false), snap("COMPLETED", false), snap("COMPLETED", true), snap("COMPLETED", true)];
      let i = 0;
      const load = vi.fn(async () => sequence[Math.min(i++, sequence.length - 1)]);
      const seen: string[] = [];
      const poller = startPolling({
        load,
        isDone: snapshotSettled,
        isTerminal: (s) => s.execution.status === "COMPLETED",
        onData: (s) => seen.push(s.execution.status + (snapshotSettled(s) ? "!" : "")),
        intervalMs: 2000,
        settleMs: 500,
      });
      await vi.advanceTimersByTimeAsync(0);
      expect(load).toHaveBeenCalledTimes(1);
      await vi.advanceTimersByTimeAsync(2000);
      await vi.advanceTimersByTimeAsync(2000);
      expect(seen).toEqual(["PENDING", "RUNNING", "COMPLETED"]);
      // Terminal, but its event has not arrived: settle quickly once more.
      await vi.advanceTimersByTimeAsync(500);
      expect(seen).toEqual(["PENDING", "RUNNING", "COMPLETED", "COMPLETED!"]);
      expect(poller.active()).toBe(false);
      await vi.advanceTimersByTimeAsync(20_000);
      expect(load).toHaveBeenCalledTimes(4); // nothing after the terminal state
    } finally {
      vi.useRealTimers();
    }
  });

  it("gives up settling after a few tries and can be stopped", async () => {
    vi.useFakeTimers();
    try {
      const load = vi.fn(async () => snap("FAILED", false)); // terminal event never arrives (event store down)
      const poller = startPolling({ load, isDone: snapshotSettled, isTerminal: () => true, onData: () => {}, settleMs: 100, maxSettles: 3 });
      await vi.advanceTimersByTimeAsync(5000);
      expect(load).toHaveBeenCalledTimes(4);
      expect(poller.active()).toBe(false);

      const running = vi.fn(async () => snap("RUNNING", false));
      const p2 = startPolling({ load: running, isDone: () => false, onData: () => {}, intervalMs: 1000 });
      await vi.advanceTimersByTimeAsync(0);
      p2.stop();
      await vi.advanceTimersByTimeAsync(10_000);
      expect(running).toHaveBeenCalledTimes(1);
    } finally {
      vi.useRealTimers();
    }
  });
});
