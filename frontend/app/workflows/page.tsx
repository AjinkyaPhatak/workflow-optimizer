"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { ErrorBanner } from "@/components/ui/ErrorBanner";
import { TopBar } from "@/components/ui/TopBar";
import { NewWorkflow } from "@/components/workflow/NewWorkflow";
import { useWorkflowDashboard } from "@/features/workflows/useWorkflowDashboard";
import { RequireAuth } from "@/lib/auth/RequireAuth";

function Dashboard() {
  const router = useRouter();
  const d = useWorkflowDashboard();
  const canCreate = d.workspace && d.workspace.role !== "viewer" && (d.projects.length > 0 || d.workspace.role !== "member");

  return (
    <>
      <TopBar />
      <main className="dashboard stack">
        <div className="row">
          <h1>Workflows</h1>
          <span className="spacer" />
          {d.workspaces.length > 1 && (
            <select aria-label="Workspace" style={{ width: 240 }} value={d.workspace?.id ?? ""} onChange={(e) => d.setWorkspaceId(e.target.value)}>
              {d.workspaces.map((w) => (
                <option key={w.id} value={w.id}>{w.name}</option>
              ))}
            </select>
          )}
        </div>
        <ErrorBanner error={d.error} />

        <section className="card">
          {d.loading ? (
            <div className="empty"><span className="spinner" aria-hidden /> Loading workflows…</div>
          ) : d.rows.length === 0 ? (
            <div className="empty">
              <b>No workflows yet</b>
              <div className="small">{canCreate ? "Create your first workflow below." : "Workflows created in this workspace appear here."}</div>
            </div>
          ) : (
            <ul className="workflow-list">
              {d.rows.map(({ workflow, project }) => (
                <li key={workflow.id}>
                  <Link href={`/workflows/${workflow.id}/editor`}>{workflow.name}</Link>
                  <span className="muted small">{project.name}</span>
                  <span className="spacer" />
                  {workflow.active_version_id ? (
                    <span className="badge published">Published</span>
                  ) : (
                    <span className="badge draft">Draft</span>
                  )}
                  <span className="muted small">Updated {new Date(workflow.updated_at).toLocaleString()}</span>
                </li>
              ))}
            </ul>
          )}
        </section>

        {canCreate && (
          <NewWorkflow projects={d.projects} create={d.createWorkflow} onCreated={(wf) => router.push(`/workflows/${wf.id}/editor`)} />
        )}
      </main>
    </>
  );
}

export default function WorkflowsPage() {
  return (
    <RequireAuth>
      <Dashboard />
    </RequireAuth>
  );
}
