"use client";

import type { ExecutionEvent } from "@/types/api";

function describe(e: ExecutionEvent): string {
  const d = e.data;
  const err = d.error as { code?: string } | undefined;
  const parts: string[] = [];
  if (typeof d.attempt === "number") parts.push(`attempt ${d.attempt}`);
  if (typeof d.invocation === "number") parts.push(`invocation ${d.invocation}`);
  if (typeof d.duration_ms === "number") parts.push(`${d.duration_ms} ms`);
  if (typeof d.delay_ms === "number") parts.push(`in ${d.delay_ms} ms`);
  if (typeof d.scope === "string") parts.push(String(d.scope));
  if (err?.code) parts.push(err.code);
  if (typeof d.dead_letter === "string") parts.push(`dead letter: ${d.dead_letter}`);
  if (typeof d.reason === "string") parts.push(String(d.reason));
  return parts.join(" · ");
}

/** The append-only event history, oldest first. */
export function EventLog({ events }: { events: ExecutionEvent[] }) {
  if (events.length === 0) return <p className="muted small">No events recorded yet.</p>;
  return (
    <table className="event-log" data-testid="event-log">
      <thead>
        <tr>
          <th>Time</th>
          <th>Event</th>
          <th>Node</th>
          <th>Details</th>
        </tr>
      </thead>
      <tbody>
        {events.map((e) => (
          <tr key={e.id} className={e.type.endsWith("FAILED") ? "failed" : ""}>
            <td className="small">{new Date(e.timestamp).toLocaleTimeString(undefined, { hour12: false, fractionalSecondDigits: 3 } as Intl.DateTimeFormatOptions)}</td>
            <td><code>{e.type}</code></td>
            <td>{e.node_id ?? ""}</td>
            <td className="small">{describe(e)}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}
