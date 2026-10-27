"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { useMemo, useState } from "react";
import { Backdrop } from "@/components/ui/Backdrop";
import { ErrorBanner } from "@/components/ui/ErrorBanner";
import { NodeIcon } from "@/components/ui/NodeIcon";
import { TopBar } from "@/components/ui/TopBar";
import { NewWorkflow } from "@/components/workflow/NewWorkflow";
import { WorkflowCard } from "@/components/workflow/WorkflowCard";
import { useProviders } from "@/features/connected-accounts/useProviders";
import { useWorkflowCards } from "@/features/workflows/useWorkflowCards";
import { useWorkflowDashboard } from "@/features/workflows/useWorkflowDashboard";
import { workflowApi } from "@/lib/api";
import { RequireAuth } from "@/lib/auth/RequireAuth";
import { comingSoonApps, unavailableApps } from "@/lib/workflow/availability";
import { appsOf } from "@/lib/workflow/requirements";

function Dashboard() {
  const router = useRouter();
  const d = useWorkflowDashboard();
  const role = d.workspace?.role;
  const canCreate = Boolean(d.workspace) && role !== "viewer" && (d.projects.length > 0 || role !== "member");
  const canEdit = Boolean(d.workspace) && role !== "viewer";
  const [creating, setCreating] = useState(false);
  const [query, setQuery] = useState("");

  const workflows = useMemo(
    () => [...d.rows].map((r) => r.workflow).sort((a, b) => b.updated_at.localeCompare(a.updated_at)),
    [d.rows],
  );
  const cards = useWorkflowCards(workflows, d.workspace?.id ?? null);
  const providers = useProviders();

  // Three templates to suggest: ready to use, and not already made.
  const suggestions = useMemo(() => {
    const unavailable = unavailableApps(cards.catalog.values(), providers);
    const names = new Set(workflows.map((w) => w.name.toLowerCase()));
    return cards.templates
      .filter((t) => !t.saved && t.category !== "Advanced" && !names.has(t.name.toLowerCase()))
      .filter((t) => comingSoonApps(t.node_types, cards.catalog, unavailable).length === 0)
      .slice(0, 3);
  }, [cards.templates, cards.catalog, providers, workflows]);

  const q = query.trim().toLowerCase();
  const shown = q ? workflows.filter((w) => w.name.toLowerCase().includes(q)) : workflows;

  return (
    <>
      <TopBar />
      <main className="wf-page textured">
        <section className="wf-hero" data-testid="new-workflow">
          <Backdrop />
          <div className="wf-hero-inner">
            <span className="wf-eyebrow">Your workflows</span>
            <h1>What would you like to automate?</h1>
            <p>Build from scratch, or start from one of our ready-made templates.</p>
            {canCreate && (
              <button type="button" className="wf-new" onClick={() => setCreating(true)}>
                <span aria-hidden>+</span> New workflow
              </button>
            )}
            <div className="wf-suggest">
              {suggestions.map((t) => {
                const app = appsOf(t.node_types, cards.catalog)[0];
                return (
                  <Link key={t.id} className="wf-pill" href={`/templates?template=${encodeURIComponent(t.id)}`}>
                    <NodeIcon integrationId={app?.id} def={app ? undefined : { type: "llm", category: "ai" }} size={16} />
                    {t.name}
                  </Link>
                );
              })}
              <Link className="wf-pill wf-pill-link" href="/templates" data-testid="browse-templates">Browse all templates →</Link>
            </div>
          </div>
        </section>

        <section className="wf-list">
          <ErrorBanner error={d.error} />
          <div className="wf-list-head">
            <h2>Recent</h2>
            {!d.loading && <span className="muted">{workflows.length} {workflows.length === 1 ? "workflow" : "workflows"}</span>}
            <span className="spacer" />
            {d.workspaces.length > 1 && (
              <select aria-label="Workspace" className="wf-select" value={d.workspace?.id ?? ""} onChange={(e) => d.setWorkspaceId(e.target.value)}>
                {d.workspaces.map((w) => <option key={w.id} value={w.id}>{w.name}</option>)}
              </select>
            )}
            {workflows.length > 0 && (
              <input type="search" className="wf-search" aria-label="Search workflows" placeholder="Search" value={query}
                onChange={(e) => setQuery(e.target.value)} />
            )}
          </div>

          {d.loading ? (
            <div className="wf-grid">
              {[0, 1, 2].map((i) => <div key={i} className="wf-card wf-skeleton" aria-hidden />)}
            </div>
          ) : (
            <div className="wf-grid">
              {shown.map((wf) => (
                <WorkflowCard key={wf.id} wf={wf} details={cards.details.get(wf.id)} catalog={cards.catalog} canEdit={canEdit}
                  onRename={async (name) => {
                    await workflowApi.update(wf.id, { name });
                    d.reload();
                  }}
                  onDelete={async () => {
                    await workflowApi.remove(wf.id);
                    d.reload();
                  }} />
              ))}
              {canCreate && !q && (
                <button type="button" className="wf-card wf-empty-tile" onClick={() => setCreating(true)}>
                  <span className="wf-plus" aria-hidden>+</span>
                  {workflows.length === 0 ? "Create your first workflow" : "Empty canvas"}
                </button>
              )}
              {q && shown.length === 0 && <p className="muted">No workflows match &ldquo;{query}&rdquo;.</p>}
            </div>
          )}
        </section>
      </main>

      {creating && (
        <div className="wf-dialog-backdrop" onMouseDown={(e) => e.target === e.currentTarget && setCreating(false)}>
          <div className="wf-dialog" role="dialog" aria-modal="true" aria-label="New workflow">
            <button type="button" className="wf-dialog-close" aria-label="Close" onClick={() => setCreating(false)}>×</button>
            <NewWorkflow projects={d.projects} create={d.createWorkflow} onCreated={(wf) => router.push(`/workflows/${wf.id}/editor`)} />
          </div>
        </div>
      )}
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
