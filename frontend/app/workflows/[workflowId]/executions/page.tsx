"use client";

import Link from "next/link";
import { useParams } from "next/navigation";
import { useEffect, useState } from "react";
import { StatusBadge } from "@/components/executions/ExecutionSummary";
import { ErrorBanner } from "@/components/ui/ErrorBanner";
import { TopBar } from "@/components/ui/TopBar";
import { formatDuration } from "@/features/executions/timeline";
import { executionApi, workflowApi } from "@/lib/api";
import { RequireAuth } from "@/lib/auth/RequireAuth";
import type { Execution, Page, Workflow } from "@/types/api";

function Executions({ workflowId }: { workflowId: string }) {
  const [workflow, setWorkflow] = useState<Workflow | null>(null);
  const [page, setPage] = useState(1);
  const [data, setData] = useState<Page<Execution> | null>(null);
  const [error, setError] = useState<unknown>(null);

  useEffect(() => {
    workflowApi.get(workflowId).then(setWorkflow, setError);
  }, [workflowId]);
  useEffect(() => {
    executionApi.listByWorkflow(workflowId, page, 20).then(setData, setError);
  }, [workflowId, page]);

  const pages = data ? Math.max(1, Math.ceil(data.total / data.page_size)) : 1;
  return (
    <>
      <TopBar />
      <main className="dashboard stack">
        <div className="row">
          <Link href={`/workflows/${workflowId}/editor`}>← Editor</Link>
          <h1>{workflow?.name ?? "Workflow"} · Executions</h1>
        </div>
        <ErrorBanner error={error} />
        <section className="card">
          {!data ? (
            <div className="empty">Loading…</div>
          ) : data.items.length === 0 ? (
            <div className="empty">No executions yet. Run the workflow from the editor.</div>
          ) : (
            <ul className="workflow-list" data-testid="execution-list">
              {data.items.map((e) => (
                <li key={e.id}>
                  <Link href={`/executions/${e.id}`}>#{e.id.slice(0, 8)}</Link>
                  <StatusBadge status={e.status} />
                  <span className="muted small">attempt {e.attempt}</span>
                  {e.error && <span className="small" style={{ color: "var(--danger)" }}>{e.error.code}</span>}
                  <span className="spacer" />
                  <span className="muted small">
                    {e.started_at && e.completed_at ? formatDuration(Date.parse(e.completed_at) - Date.parse(e.started_at)) : ""}
                  </span>
                  <span className="muted small">{new Date(e.created_at).toLocaleString()}</span>
                </li>
              ))}
            </ul>
          )}
        </section>
        {pages > 1 && (
          <div className="row">
            <button disabled={page <= 1} onClick={() => setPage(page - 1)}>Previous</button>
            <span className="small muted">Page {page} of {pages}</span>
            <button disabled={page >= pages} onClick={() => setPage(page + 1)}>Next</button>
          </div>
        )}
      </main>
    </>
  );
}

export default function WorkflowExecutionsPage() {
  const { workflowId } = useParams<{ workflowId: string }>();
  return (
    <RequireAuth>
      <Executions workflowId={workflowId} />
    </RequireAuth>
  );
}
