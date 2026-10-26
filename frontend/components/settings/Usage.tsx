"use client";

// The Usage page (Phase M): how much the workspace used in a recent period,
// in plain numbers: runs and how they ended, AI work, app steps, the
// busiest workflows and stored files.

import Link from "next/link";
import { useEffect, useMemo, useState } from "react";
import { Spinner } from "@/components/ui/BrandMark";
import { ErrorBanner } from "@/components/ui/ErrorBanner";
import { accountApi, authApi } from "@/lib/api";
import { timeAgo } from "@/lib/time";
import type { Usage, WorkspaceRef } from "@/types/api";

export const PERIODS = [7, 30, 90] as const;

/** "12.3 KB" */
export function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  const units = ["KB", "MB", "GB"];
  let v = n / 1024;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v < 10 ? v.toFixed(1) : Math.round(v)} ${units[i]}`;
}

/** "1,234" */
export function formatCount(n: number): string {
  return new Intl.NumberFormat("en").format(n);
}

/** Every day of the period (UTC), with the days that had no runs as zero. */
export function fillDays(u: Pick<Usage, "since" | "days" | "daily">): Usage["daily"] {
  const byDate = new Map(u.daily.map((d) => [d.date, d]));
  const start = Date.parse(u.since.slice(0, 10) + "T00:00:00Z");
  return Array.from({ length: u.days }, (_, i) => {
    const date = new Date(start + i * 86_400_000).toISOString().slice(0, 10);
    return byDate.get(date) ?? { date, completed: 0, failed: 0, cancelled: 0 };
  });
}

function Stat({ label, value, note }: { label: string; value: string; note?: string }) {
  return (
    <div className="stat-tile">
      <div className="stat-label">{label}</div>
      <div className="stat-value">{value}</div>
      {note && <div className="small muted">{note}</div>}
    </div>
  );
}

const day = (date: string) => new Date(date + "T00:00:00Z").toLocaleDateString(undefined, { month: "short", day: "numeric", timeZone: "UTC" });

/** Runs per day: worked and did not work, stacked, with a hover label and
 * the numbers as a table for screen readers. */
function DailyRuns({ days }: { days: Usage["daily"] }) {
  const max = Math.max(1, ...days.map((d) => d.completed + d.failed + d.cancelled));
  const [hover, setHover] = useState<number | null>(null);
  const h = hover === null ? null : days[hover];
  return (
    <div className="card stack" data-testid="daily-runs">
      <div className="row">
        <h2>Runs per day</h2>
        <span className="spacer" />
        <span className="legend"><i className="swatch ok" aria-hidden /> Worked</span>
        <span className="legend"><i className="swatch bad" aria-hidden /> Did not work</span>
      </div>
      <div className="small muted" aria-live="polite" style={{ minHeight: 18 }}>
        {h ? `${day(h.date)}: ${h.completed} worked, ${h.failed + h.cancelled} did not` : "Point at a day to see its runs."}
      </div>
      <div className="bars" role="img" aria-label="Runs per day" onMouseLeave={() => setHover(null)}>
        {days.map((d, i) => {
          const bad = d.failed + d.cancelled;
          return (
            <div key={d.date} className={`bar-col ${hover === i ? "hover" : ""}`} onMouseEnter={() => setHover(i)}>
              <div className="bar-stack" style={{ height: `${((d.completed + bad) / max) * 100}%` }}>
                {bad > 0 && <div className="bar-seg bad" style={{ flexGrow: bad }} />}
                {d.completed > 0 && <div className="bar-seg ok" style={{ flexGrow: d.completed }} />}
              </div>
            </div>
          );
        })}
      </div>
      <div className="bars-axis small muted">
        <span>{day(days[0].date)}</span>
        <span>{day(days[days.length - 1].date)}</span>
      </div>
      <table className="visually-hidden">
        <caption>Runs per day</caption>
        <thead><tr><th>Day</th><th>Worked</th><th>Did not work</th></tr></thead>
        <tbody>{days.map((d) => <tr key={d.date}><td>{d.date}</td><td>{d.completed}</td><td>{d.failed + d.cancelled}</td></tr>)}</tbody>
      </table>
    </div>
  );
}

export function UsageView({ usage }: { usage: Usage }) {
  const days = useMemo(() => fillDays(usage), [usage]);
  const r = usage.runs;
  const finished = r.completed + r.failed + r.cancelled;
  const success = finished ? Math.round((r.completed / finished) * 100) : null;
  return (
    <>
      <div className="stat-grid">
        <Stat label="Runs" value={formatCount(r.total)} note={r.active ? `${r.active} still running or waiting` : undefined} />
        <Stat label="Worked" value={success === null ? "—" : `${success}%`} note={`${formatCount(r.completed)} worked, ${formatCount(r.failed + r.cancelled)} did not`} />
        <Stat label="AI steps" value={formatCount(usage.ai.steps)} note={usage.ai.total_tokens ? `${formatCount(usage.ai.total_tokens)} tokens` : undefined} />
        <Stat label="App steps" value={formatCount(usage.app_steps)} note="Gmail, Sheets, Docs and other apps" />
        <Stat label="Workflows" value={formatCount(usage.live_workflows)} />
        <Stat label="Files" value={formatCount(usage.files.count)} note={formatBytes(usage.files.bytes)} />
      </div>
      {finished > 0 && <DailyRuns days={days} />}
      <div className="card stack">
        <h2>Busiest workflows</h2>
        {usage.workflows.length === 0 ? (
          <p className="muted small">No runs in this period yet. Open a workflow and press ▶ Run.</p>
        ) : (
          <table className="usage-table">
            <thead><tr><th>Workflow</th><th>Runs</th><th>Did not work</th><th>Last run</th></tr></thead>
            <tbody>
              {usage.workflows.map((w) => (
                <tr key={w.workflow_id}>
                  <td><Link href={`/workflows/${w.workflow_id}/executions`}>{w.name}</Link></td>
                  <td>{formatCount(w.runs)}</td>
                  <td>{w.failed ? formatCount(w.failed) : "—"}</td>
                  <td>{timeAgo(w.last_run_at)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
      {usage.ai.steps > 0 && (
        <p className="small muted">
          AI steps use your own AI key, so the provider&apos;s limits apply: the free Gemini plan allows a limited number of requests per minute and per day.
        </p>
      )}
    </>
  );
}

export function UsagePage() {
  const [workspaces, setWorkspaces] = useState<WorkspaceRef[]>([]);
  const [workspaceId, setWorkspaceId] = useState<string | null>(null);
  const [days, setDays] = useState<number>(30);
  const [usage, setUsage] = useState<Usage | null>(null);
  const [error, setError] = useState<unknown>(null);

  useEffect(() => {
    authApi.me().then((me) => {
      setWorkspaces(me.workspaces);
      setWorkspaceId((c) => c ?? me.workspaces[0]?.id ?? null);
    }).catch(setError);
  }, []);
  useEffect(() => {
    if (!workspaceId) return;
    let cancelled = false;
    accountApi.usage(workspaceId, days)
      .then((u) => !cancelled && (setUsage(u), setError(null)))
      .catch((e) => !cancelled && setError(e));
    return () => {
      cancelled = true;
    };
  }, [workspaceId, days]);

  return (
    <main className="dashboard stack usage-page">
      <div className="row">
        <h1>Usage</h1>
        <span className="spacer" />
        {workspaces.length > 1 && (
          <select aria-label="Workspace" style={{ width: 220 }} value={workspaceId ?? ""} onChange={(e) => setWorkspaceId(e.target.value)}>
            {workspaces.map((w) => <option key={w.id} value={w.id}>{w.name}</option>)}
          </select>
        )}
        <div className="gallery-tabs" role="tablist" aria-label="Period">
          {PERIODS.map((p) => (
            <button key={p} type="button" role="tab" aria-selected={p === days} className={p === days ? "active" : ""} onClick={() => setDays(p)}>
              Last {p} days
            </button>
          ))}
        </div>
      </div>
      <ErrorBanner error={error} />
      {usage ? <UsageView usage={usage} /> : !error && <div className="empty"><Spinner /> Loading usage…</div>}
    </main>
  );
}
