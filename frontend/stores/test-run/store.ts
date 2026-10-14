// Test runs of the editor's draft (Phase E3): the shared run input and the
// last result, per editor session.

import { create } from "zustand";
import type { RunRow } from "@/lib/workflow/runInput";
import type { TestNodeResult, TestRunResult } from "@/types/api";

interface TestRunState {
  /** Run input rows (null until the Run tab or a test first needs them). */
  rows: RunRow[] | null;
  setRows: (rows: RunRow[]) => void;
  running: boolean;
  /** The node the last test ran up to (null: the whole workflow). */
  until: string | null;
  result: TestRunResult | null;
  error: unknown;
  start: (until: string | null) => void;
  finish: (result: TestRunResult | null, error?: unknown) => void;
  reset: () => void;
}

export const useTestRunStore = create<TestRunState>((set) => ({
  rows: null,
  setRows: (rows) => set({ rows }),
  running: false,
  until: null,
  result: null,
  error: null,
  start: (until) => set({ running: true, until, error: null }),
  finish: (result, error = null) => set({ running: false, result, error }),
  reset: () => set({ rows: null, running: false, until: null, result: null, error: null }),
}));

/** The last test's result for one node. */
export function useNodeTestResult(nodeId: string): TestNodeResult | undefined {
  return useTestRunStore((s) => s.result?.nodes.find((n) => n.node_id === nodeId));
}
