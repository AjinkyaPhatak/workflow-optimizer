"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { useState, type FormEvent } from "react";
import { ErrorBanner } from "@/components/ui/ErrorBanner";
import { TopBar } from "@/components/ui/TopBar";
import { useWorkflowDashboard } from "@/features/workflows/useWorkflowDashboard";
import { RequireAuth } from "@/lib/auth/RequireAuth";

function Dashboard() {
  const router = useRouter();
  const d = useWorkflowDashboard();
  const [name, setName] = useState("");
  const [projectId, setProjectId] = useState("");
  const [creating, setCreating] = useState(false);
  const [createError, setCreateError] = useState<unknown>(null);
  const canCreate = d.workspace && d.workspace.role !== "viewer" && (d.projects.length > 0 || d.workspace.role !== "member");

  async function create(e: FormEvent) {
    e.preventDefault();
    setCreating(true);
    setCreateError(null);
    try {
      const wf = await d.createWorkflow(name.trim(), projectId || undefined);
      router.push(`/workflows/${wf.id}/editor`);
    } catch (err) {
      setCreateError(err);
      setCreating(false);
    }
  }

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
          <form className="card stack" style={{ padding: 16 }} onSubmit={create}>
            <h3>New workflow</h3>
            <ErrorBanner error={createError} />
            <div className="row">
              <input aria-label="Workflow name" placeholder="Workflow name" value={name} onChange={(e) => setName(e.target.value)} required maxLength={255} />
              {d.projects.length > 1 && (
                <select aria-label="Project" style={{ width: 200 }} value={projectId} onChange={(e) => setProjectId(e.target.value)}>
                  {d.projects.map((p) => (
                    <option key={p.id} value={p.id}>{p.name}</option>
                  ))}
                </select>
              )}
              <button className="primary" type="submit" disabled={creating || !name.trim()}>
                + New Workflow
              </button>
            </div>
          </form>
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
