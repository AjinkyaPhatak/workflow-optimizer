"use client";

import Link from "next/link";
import { useEffect, useMemo, useState } from "react";
import { ErrorBanner } from "@/components/ui/ErrorBanner";
import { TopBar } from "@/components/ui/TopBar";
import { buildTimeline, failurePoint } from "@/features/executions/timeline";
import { useExecutionDebugger } from "@/features/executions/useExecutionDebugger";
import { EventLog } from "./EventLog";
import { ExecutionGraph } from "./ExecutionGraph";
import { ExecutionSummary, StatusBadge } from "./ExecutionSummary";
import { ExecutionTimeline } from "./ExecutionTimeline";
import { NodeInspector } from "./NodeInspector";

export function ExecutionDebugger({ executionId }: { executionId: string }) {
  const { snapshot, workflow, definition, error, polling, refresh } = useExecutionDebugger(executionId);
  const [selected, setSelected] = useState<string | null>(null);
  const [tab, setTab] = useState<"graph" | "events">("graph");
  const [now, setNow] = useState(() => Date.now());

  // Ticks the live duration while the execution is active.
  useEffect(() => {
    if (!polling) return;
    const t = setInterval(() => setNow(Date.now()), 500);
    return () => clearInterval(t);
  }, [polling]);

  const rows = useMemo(
    () => (snapshot ? buildTimeline(snapshot.execution.status, definition, snapshot.nodes, snapshot.events) : []),
    [snapshot, definition],
  );
  // Default selection: where it failed, else the running node, else the last one.
  const effective =
    selected ?? failurePoint(rows)?.nodeId ?? rows.find((r) => r.status === "RUNNING")?.nodeId ?? rows.filter((r) => r.attempts.length).at(-1)?.nodeId ?? null;
  const row = rows.find((r) => r.nodeId === effective) ?? null;

  if (!snapshot) {
    return (
      <>
        <TopBar />
        <main className="dashboard">{error ? <ErrorBanner error={error} /> : <div className="page-loading">Loading execution…</div>}</main>
      </>
    );
  }
  const e = snapshot.execution;
  return (
    <>
      <TopBar />
      <main className="debugger">
        <header className="debugger-header">
          <div className="row">
            <Link href={`/workflows/${e.workflow_id}/executions`}>← Executions</Link>
            <h1>{workflow?.name ?? "Workflow"}</h1>
            <span className="muted">Execution <code title={e.id}>#{e.id.slice(0, 8)}</code></span>
            <StatusBadge status={e.status} />
            {polling ? <span className="small muted" data-testid="polling">● Live (refreshing)</span> : <span className="small muted">Final</span>}
            <span className="spacer" />
            <button onClick={refresh}>Refresh</button>
            <Link className="button" href={`/workflows/${e.workflow_id}/editor`}>Open workflow</Link>
          </div>
          <ExecutionSummary execution={e} now={now} />
          {e.error && (
            <div className="error-banner" data-testid="execution-error">
              <b>{e.error.code}</b>
              {e.error.node_id ? ` at node ${e.error.node_id}` : ""}: {e.error.message}
            </div>
          )}
          <ErrorBanner error={error} />
        </header>
        <div className="debugger-body">
          <section className="debugger-left">
            <h3>Timeline</h3>
            <ExecutionTimeline rows={rows} selected={effective} onSelect={setSelected} />
            <div className="bottom-tabs" role="tablist">
              <button role="tab" className={tab === "graph" ? "active" : ""} onClick={() => setTab("graph")}>Graph</button>
              <button role="tab" className={tab === "events" ? "active" : ""} onClick={() => setTab("events")}>
                Events ({snapshot.events.length})
              </button>
            </div>
            {tab === "graph" ? (
              definition ? (
                <ExecutionGraph definition={definition} rows={rows} selected={effective} onSelect={setSelected} />
              ) : (
                <p className="muted small">The workflow version is not available.</p>
              )
            ) : (
              <EventLog events={snapshot.events} />
            )}
          </section>
          <NodeInspector key={row?.nodeId ?? "none"} row={row} />
        </div>
      </main>
    </>
  );
}
