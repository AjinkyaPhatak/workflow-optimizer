"use client";

import Link from "next/link";
import { ErrorBanner } from "@/components/ui/ErrorBanner";
import { isActiveAutomationRun, useAutomationRuns } from "@/features/assistant/useAutomationRuns";
import type { Automation } from "@/types/automation";

export function AutomationCard({ item, busy, onToggle, onDelete }: {
  item: Automation & { id: string }; busy: boolean; onToggle: () => Promise<void>; onDelete: () => Promise<void>;
}) {
  const state = useAutomationRuns(item.id);
  const active = state.runs.filter(isActiveAutomationRun);
  const history = state.runs.filter(run => !isActiveAutomationRun(run));
  const runLink = (id: string) => "/home?run=" + encodeURIComponent(id);
  return <section className="card automation-card" aria-label={item.name}>
    <div className="automation-row">
      <div className="automation-description"><h3>{item.name}</h3>
        <p className="muted">{item.cron ? item.cron + " · " + item.timezone : item.trigger}</p>
        <p className="hint">{item.enabled ? "Enabled · continues until paused or deleted." : "Paused · no new runs will start."} Existing runs have their own controls below.</p>
        {item.trigger === "webhook" && <p className="hint">POST /api/v1/triggers/{item.id} · signed with your deployment’s webhook key.</p>}
      </div>
      <div className="automation-actions">
        <button disabled={busy} onClick={() => void onToggle()}>{item.enabled ? "Pause future runs" : "Enable future runs"}</button>
        <button className="small-button danger" disabled={busy || state.loading || !!state.loadError || active.length > 0}
          onClick={() => void onDelete()}>Delete automation</button>
      </div>
    </div>
    <div className="automation-runs">
      <div className="row"><h4>Active runs{active.length ? ` (${active.length})` : ""}</h4><span className="spacer" />
        <button className="small-button" onClick={() => void state.refresh()}>Refresh runs</button></div>
      <ErrorBanner error={state.error} />
      {!!state.loadError && <p className="hint">Run status could not be refreshed. Try Refresh runs before changing an execution.</p>}
      {state.loading ? <p role="status" className="muted">Loading runs…</p> : !active.length && !state.loadError && <p className="muted">No active runs.</p>}
      {active.map(run => <div key={run.id} className="automation-run">
        <div><span className={"run-status status-" + run.status.toLowerCase()}>{run.status.toLowerCase().replaceAll("_", " ")}</span>
          <p className="hint">Started {new Date(run.created_at).toLocaleString()}</p>
          {run.status === "PAUSED" && <p className="hint">Open the run to resume or renew its budget.</p>}
          {run.status === "WAITING_FOR_INPUT" && <p className="hint">Open the run to answer its question.</p>}
        </div>
        <div className="automation-actions"><Link className="button" href={runLink(run.id)}>Open run</Link>
          {run.status !== "PAUSED" && <button disabled={!!state.requested[run.id] || !!state.loadError}
            onClick={() => void state.control(run, "pause")}>{state.requested[run.id] === "pause" ? "Pausing…" : "Pause run"}</button>}
          <button className="small-button danger" disabled={state.requested[run.id] === "cancel" || !!state.loadError}
            onClick={() => void state.control(run, "cancel")}>{state.requested[run.id] === "cancel" ? "Stopping…" : "Stop run"}</button>
        </div>
      </div>)}
      {state.notice && <p role="status" className="hint">{state.notice}</p>}
      {active.length > 0 && <p className="hint">Pause future runs and stop the active runs to stop this automation completely. Stop cannot undo actions already completed. Stop active runs before deleting the automation.</p>}
      <details className="automation-history"><summary>Recent history ({history.length})</summary>
        <p className="hint">The latest 20 finished runs are shown.</p>
        {!history.length && <p className="muted">No finished runs yet.</p>}
        {history.map(run => <div key={run.id} className="automation-run"><div>
          <span className={"run-status status-" + run.status.toLowerCase()}>{run.status.toLowerCase()}</span>
          <p className="hint">Started {new Date(run.created_at).toLocaleString()}</p>
        </div><Link className="button" href={runLink(run.id)}>Open run</Link></div>)}
      </details>
    </div>
  </section>;
}
