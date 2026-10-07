"use client";

import { formatCost, formatDuration } from "@/features/executions/timeline";
import type { ExecutionDetails } from "@/types/api";

function statusClass(s: string): string {
  if (s === "COMPLETED") return "published";
  if (s === "FAILED") return "failed";
  if (s === "RUNNING" || s === "PENDING") return "running";
  return "";
}

export function StatusBadge({ status }: { status: string }) {
  return <span className={`badge ${statusClass(status)}`} data-testid="execution-status">{status}</span>;
}

/** Live duration for an execution that has started but not finished. */
function liveDuration(e: ExecutionDetails, now: number): number | null {
  if (e.duration_ms !== null) return e.duration_ms;
  if (!e.started_at) return null;
  return Math.max(0, now - Date.parse(e.started_at));
}

export function ExecutionSummary({ execution, now }: { execution: ExecutionDetails; now: number }) {
  const items: [string, string][] = [
    ["Duration", formatDuration(liveDuration(execution, now))],
    ["Nodes", String(execution.node_count)],
    ["Attempts", `${execution.attempt} / ${execution.max_attempts}`],
    ["Retries", String(execution.retries)],
    ["Tokens", execution.usage ? execution.usage.total_tokens.toLocaleString() : "—"],
    ["Estimated cost", formatCost(execution.estimated_cost_usd)],
  ];
  return (
    <dl className="summary" data-testid="execution-summary">
      {items.map(([k, v]) => (
        <div key={k}>
          <dt>{k}</dt>
          <dd data-testid={`summary-${k.toLowerCase().replace(/\s+/g, "-")}`}>{v}</dd>
        </div>
      ))}
    </dl>
  );
}
