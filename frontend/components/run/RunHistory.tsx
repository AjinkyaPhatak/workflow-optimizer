"use client";

// A workflow's runs in plain sentences (Phase I2): what happened, when, how
// long it took, a preview of the result; open one to see the full result,
// the answers it was given, or what went wrong and what to do.

import Link from "next/link";
import { useState } from "react";
import { FriendlyValue } from "@/components/run/FriendlyValue";
import { friendlyError } from "@/lib/workflow/friendlyErrors";
import { questionLabel } from "@/lib/workflow/runFields";
import { timeAgo, tookLabel } from "@/lib/time";
import { useShowDetails } from "@/stores/ui/details";
import type { Execution } from "@/types/api";

/** The single value of a run's result (an Output step's value), else the whole result. */
export function resultValue(output: Record<string, unknown> | null): unknown {
  if (!output) return null;
  const outs = Object.values(output);
  if (outs.length !== 1) return output;
  const ports = outs[0];
  if (ports && typeof ports === "object" && !Array.isArray(ports)) {
    const keys = Object.keys(ports);
    if (keys.length === 1) return (ports as Record<string, unknown>)[keys[0]];
  }
  return ports;
}

/** A short line of a result for the list. */
export function preview(value: unknown): string {
  if (value === null || value === undefined) return "";
  const text = typeof value === "string" ? value : Array.isArray(value) ? `${value.length} item${value.length === 1 ? "" : "s"}` : JSON.stringify(value);
  const line = text.replace(/\s+/g, " ").trim();
  return line.length > 110 ? `${line.slice(0, 107)}…` : line;
}

/** What happened, as a sentence. */
export function runSentence(e: Pick<Execution, "status" | "error" | "attempt">): { icon: string; text: string; tone: "ok" | "bad" | "busy" | "off" } {
  switch (e.status) {
    case "COMPLETED": return { icon: "✓", text: e.attempt > 1 ? `Worked (after ${e.attempt} tries)` : "Worked", tone: "ok" };
    case "FAILED": return { icon: "✕", text: `Did not work: ${friendlyError(e.error?.code, e.error?.message).title.toLowerCase()}`, tone: "bad" };
    case "CANCELLED": return { icon: "■", text: "Stopped", tone: "off" };
    case "RUNNING": return { icon: "…", text: "Running", tone: "busy" };
    default: return { icon: "…", text: "Waiting to start", tone: "busy" };
  }
}

function Row({ e }: { e: Execution }) {
  const [open, setOpen] = useState(false);
  const details = useShowDetails();
  const s = runSentence(e);
  const value = e.status === "COMPLETED" ? resultValue(e.output) : null;
  const took = e.started_at && e.completed_at ? tookLabel(Date.parse(e.completed_at) - Date.parse(e.started_at)) : null;
  const friendly = e.status === "FAILED" ? friendlyError(e.error?.code, e.error?.message) : null;
  const answers = Object.entries(e.input ?? {});
  return (
    <li className={`history-row ${s.tone}`} data-testid={`run-${e.id}`}>
      <button type="button" className="history-summary" aria-expanded={open} onClick={() => setOpen(!open)}>
        <span className={`history-icon ${s.tone}`} aria-hidden>{s.icon}</span>
        <span className="history-main">
          <span className="history-text">{s.text}</span>
          {value !== null && preview(value) && <span className="history-preview">{preview(value)}</span>}
        </span>
        <span className="history-when">
          {timeAgo(e.created_at)}
          {took && <span className="muted"> · took {took}</span>}
        </span>
      </button>
      {open && (
        <div className="history-details">
          {answers.length > 0 && (
            <div className="small">
              <b>You gave:</b> {answers.map(([k, v]) => `${questionLabel(k)} = ${typeof v === "string" ? v : JSON.stringify(v)}`).join(" · ")}
            </div>
          )}
          {e.status === "COMPLETED" && <FriendlyValue value={value} />}
          {friendly && (
            <div className="run-problem">
              <b>{friendly.title}</b>
              {friendly.detail && <div className="small">{friendly.detail}</div>}
              <div className="small">{friendly.advice}</div>
              {details && e.error?.code && <div className="small muted"><code>{e.error.code}</code></div>}
            </div>
          )}
        </div>
      )}
      <Link className="history-steps small" href={`/executions/${e.id}`}>See each step</Link>
    </li>
  );
}

export function RunHistory({ runs }: { runs: Execution[] }) {
  return (
    <ul className="history-list" data-testid="execution-list">
      {runs.map((e) => <Row key={e.id} e={e} />)}
    </ul>
  );
}
