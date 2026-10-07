"use client";

import { useState } from "react";
import { formatCost, formatDuration, type TimelineRow } from "@/features/executions/timeline";
import { JsonViewer } from "./JsonViewer";

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="inspector-field">
      <dt>{label}</dt>
      <dd data-testid={`inspector-${label.toLowerCase().replace(/\s+/g, "-")}`}>{children}</dd>
    </div>
  );
}

/** Everything persisted about one node: per attempt, status, timing, error,
 * provider/model/usage, and its (redacted) input and output. */
export function NodeInspector({ row }: { row: TimelineRow | null }) {
  const [attemptIndex, setAttemptIndex] = useState<number | null>(null);
  if (!row) return <aside className="inspector muted">Select a node in the timeline to inspect it.</aside>;
  const index = attemptIndex !== null && attemptIndex < row.attempts.length ? attemptIndex : row.attempts.length - 1;
  const view = row.attempts[index];
  const r = view?.record;

  return (
    <aside className="inspector" aria-label="Node details" data-testid="node-inspector">
      <h2>{row.name}</h2>
      <div className="muted small">
        {row.type} · <code>{row.nodeId}</code>
      </div>
      {row.attempts.length > 1 && (
        <div className="attempt-tabs" role="tablist">
          {row.attempts.map((a, i) => (
            <button key={a.record.id} role="tab" className={i === index ? "active" : ""} onClick={() => setAttemptIndex(i)}>
              Attempt {a.record.attempt}
            </button>
          ))}
        </div>
      )}
      {!r ? (
        <p className="muted">{row.notRun ? "This node did not run." : "This node has not run yet."}</p>
      ) : (
        <>
          <dl className="inspector-fields">
            <Field label="Status">{r.status}</Field>
            <Field label="Duration">{formatDuration(r.duration_ms)}</Field>
            <Field label="Attempt">{r.attempt}{r.execution_attempt > 1 ? ` (execution attempt ${r.execution_attempt})` : ""}</Field>
            <Field label="Started">{r.started_at ? new Date(r.started_at).toLocaleTimeString() : "—"}</Field>
            {r.provider && <Field label="Provider">{r.provider}</Field>}
            {r.model && <Field label="Model">{r.model}</Field>}
            {r.usage && (
              <Field label="Tokens">
                Input {r.usage.input_tokens.toLocaleString()} · Output {r.usage.output_tokens.toLocaleString()} · Total{" "}
                {r.usage.total_tokens.toLocaleString()}
              </Field>
            )}
            {r.estimated_cost_usd !== null && <Field label="Estimated cost">{formatCost(r.estimated_cost_usd)} (estimate)</Field>}
          </dl>
          {r.error && (
            <div className="error-banner" data-testid="inspector-error">
              <b>{r.error.code}</b>: {r.error.message}
              <div className="small">{r.error.retryable ? "Retryable" : "Not retryable"}</div>
            </div>
          )}
          {view.retry && (
            <p className="small muted">
              Retry scheduled ({view.retry.scope === "node" ? "node retried in place" : "new execution attempt"}
              {view.retry.delayMs !== null ? ` after ${formatDuration(view.retry.delayMs)}` : ""}).
            </p>
          )}
          {r.input !== undefined ? <JsonViewer label="Input" value={r.input} defaultDepth={3} /> : <p className="small muted">Input hidden by policy.</p>}
          {r.output !== undefined ? <JsonViewer label="Output" value={r.output} defaultDepth={3} /> : r.status === "COMPLETED" && <p className="small muted">Output hidden by policy.</p>}
        </>
      )}
    </aside>
  );
}
