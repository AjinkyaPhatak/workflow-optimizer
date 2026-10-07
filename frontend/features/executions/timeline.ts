// Builds the debugger's timeline from persisted data only: the workflow
// definition (which nodes exist), node execution records (what ran, in what
// order, with what outcome) and execution events (retries, reused nodes).
// Nothing here invents execution state.

import type { ExecutionEvent, ExecutionStatus, NodeExecutionDetails, WorkflowDefinition } from "@/types/api";

export type NodeRunStatus = "COMPLETED" | "RUNNING" | "FAILED" | "SKIPPED" | "PENDING" | "CANCELLED";

export interface AttemptView {
  record: NodeExecutionDetails;
  /** The retry decided after this attempt failed, if any. */
  retry?: { delayMs: number | null; scope: "node" | "execution" };
}

export interface TimelineRow {
  nodeId: string;
  name: string;
  type: string;
  status: NodeRunStatus;
  durationMs: number | null;
  attempts: AttemptView[];
  /** The node was not re-run in a later execution attempt (output reused). */
  reused: boolean;
  /** For PENDING rows of a finished execution: the node never ran. */
  notRun: boolean;
}

const TERMINAL: ExecutionStatus[] = ["COMPLETED", "FAILED", "CANCELLED"];

export function isTerminal(status: ExecutionStatus | undefined): boolean {
  return !!status && TERMINAL.includes(status);
}

function recordStatus(s: string): NodeRunStatus {
  switch (s) {
    case "COMPLETED":
    case "RUNNING":
    case "FAILED":
    case "SKIPPED":
    case "CANCELLED":
      return s;
    default:
      return "PENDING";
  }
}

export function buildTimeline(
  executionStatus: ExecutionStatus,
  definition: WorkflowDefinition | null,
  records: NodeExecutionDetails[],
  events: ExecutionEvent[],
): TimelineRow[] {
  const byNode = new Map<string, NodeExecutionDetails[]>();
  for (const r of records) byNode.set(r.node_id, [...(byNode.get(r.node_id) ?? []), r]);

  // Retry decisions per node, in order (node-scoped retries name the node;
  // an execution retry applies to the node that failed just before it).
  const retries = new Map<string, { delayMs: number | null; scope: "node" | "execution" }[]>();
  let lastFailedNode: string | null = null;
  const reused = new Set<string>();
  for (const e of events) {
    if (e.type === "NODE_FAILED" && e.node_id) lastFailedNode = e.node_id;
    if (e.type === "NODE_SKIPPED" && e.node_id) reused.add(e.node_id);
    if (e.type === "RETRY_SCHEDULED") {
      const node = e.node_id ?? lastFailedNode;
      if (!node) continue;
      const delay = typeof e.data.delay_ms === "number" ? e.data.delay_ms : null;
      retries.set(node, [...(retries.get(node) ?? []), { delayMs: delay, scope: e.node_id ? "node" : "execution" }]);
    }
  }

  const names = new Map(definition?.nodes.map((n) => [n.id, n]) ?? []);
  const ran = [...byNode.keys()]; // records come in execution order
  const unrun = (definition?.nodes ?? []).map((n) => n.id).filter((id) => !byNode.has(id));
  const finished = isTerminal(executionStatus);

  return [...ran, ...unrun].map((nodeId): TimelineRow => {
    const recs = byNode.get(nodeId) ?? [];
    const failedAttempts = recs.filter((r) => r.status === "FAILED");
    const nodeRetries = retries.get(nodeId) ?? [];
    let retryIndex = 0;
    const attempts = recs.map((record): AttemptView => {
      const view: AttemptView = { record };
      if (record.status === "FAILED" && failedAttempts.includes(record) && retryIndex < nodeRetries.length) {
        view.retry = nodeRetries[retryIndex++];
      }
      return view;
    });
    const last = recs[recs.length - 1];
    const def = names.get(nodeId);
    return {
      nodeId,
      name: def?.name ?? nodeId,
      type: def?.type ?? last?.node_type ?? "",
      status: last ? recordStatus(last.status) : "PENDING",
      durationMs: last?.duration_ms ?? null,
      attempts,
      reused: reused.has(nodeId),
      notRun: !last && finished,
    };
  });
}

/** The failing node, where an execution failed (the last FAILED attempt). */
export function failurePoint(rows: TimelineRow[]): TimelineRow | null {
  for (let i = rows.length - 1; i >= 0; i--) if (rows[i].status === "FAILED") return rows[i];
  return null;
}

export function formatDuration(ms: number | null | undefined): string {
  if (ms === null || ms === undefined) return "—";
  if (ms < 1000) return `${ms} ms`;
  if (ms < 60_000) return `${(ms / 1000).toFixed(2)} s`;
  const m = Math.floor(ms / 60_000);
  return `${m}m ${Math.round((ms % 60_000) / 1000)}s`;
}

export function formatCost(usd: number | null | undefined): string {
  if (usd === null || usd === undefined) return "—";
  return usd < 0.01 ? `$${usd.toFixed(5)}` : `$${usd.toFixed(4)}`;
}

export const STATUS_ICON: Record<NodeRunStatus, string> = {
  COMPLETED: "✓",
  RUNNING: "●",
  FAILED: "✕",
  SKIPPED: "—",
  PENDING: "○",
  CANCELLED: "⊘",
};
