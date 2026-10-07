"use client";

import { STATUS_ICON, formatDuration, type TimelineRow } from "@/features/executions/timeline";

export function ExecutionTimeline({
  rows,
  selected,
  onSelect,
}: {
  rows: TimelineRow[];
  selected: string | null;
  onSelect: (nodeId: string) => void;
}) {
  if (rows.length === 0) return <p className="muted">No node has run yet.</p>;
  return (
    <ol className="timeline" aria-label="Execution timeline">
      {rows.map((r) => {
        const last = r.attempts[r.attempts.length - 1]?.record;
        return (
          <li
            key={r.nodeId}
            className={`timeline-row status-${r.status.toLowerCase()} ${selected === r.nodeId ? "selected" : ""}`}
            data-testid={`timeline-${r.nodeId}`}
            data-status={r.status}
          >
            <button className="timeline-button" onClick={() => onSelect(r.nodeId)} aria-pressed={selected === r.nodeId}>
              <span className="timeline-icon" aria-label={r.status}>{STATUS_ICON[r.status]}</span>
              <span className="timeline-main">
                <span className="timeline-name">{r.name}</span>
                <span className="muted small"> {r.type}</span>
                <span className="timeline-meta small">
                  {r.notRun ? "not run" : formatDuration(r.durationMs)}
                  {r.attempts.length > 1 && ` · ${r.attempts.length} attempts`}
                  {r.reused && " · reused from an earlier attempt"}
                </span>
                {r.attempts.length > 1 && (
                  <span className="timeline-attempts small">
                    {r.attempts.map((a, i) => (
                      <span key={a.record.id} className={`attempt status-${a.record.status.toLowerCase()}`} data-testid={`attempt-${r.nodeId}-${i + 1}`}>
                        Attempt {a.record.attempt}: {a.record.status}
                        {a.record.error ? ` · ${a.record.error.code}` : ""}
                        {a.retry ? ` → retry${a.retry.delayMs !== null ? ` in ${formatDuration(a.retry.delayMs)}` : ""}` : ""}
                      </span>
                    ))}
                  </span>
                )}
                {r.status === "FAILED" && last?.error && (
                  <span className="timeline-error small">{last.error.code}: {last.error.message}</span>
                )}
              </span>
            </button>
          </li>
        );
      })}
    </ol>
  );
}
