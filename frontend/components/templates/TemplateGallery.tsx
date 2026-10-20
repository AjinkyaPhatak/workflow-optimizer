"use client";

// The Templates page (Phase K): ready-made workflows by category, the
// workspace's saved ones, and for each what you need before you start.

import { useRouter } from "next/navigation";
import { useMemo, useState } from "react";
import { NodeIcon } from "@/components/ui/NodeIcon";
import { ErrorBanner } from "@/components/ui/ErrorBanner";
import { Spinner } from "@/components/ui/BrandMark";
import { useTemplateGallery } from "@/features/templates/useTemplateGallery";
import { appsOf } from "@/lib/workflow/requirements";
import { useShowDetails } from "@/stores/ui/details";
import type { NodeDefinition, WorkflowTemplate } from "@/types/api";
import { TemplateDetails } from "./TemplateDetails";

export const ALL = "All";
export const SAVED = "Saved";
const ADVANCED = "Advanced";

/** The categories to offer, in gallery order (Saved first when there are
 * any; Advanced only with technical details on). */
export function categoriesOf(list: WorkflowTemplate[], showAdvanced: boolean): string[] {
  const out = [ALL];
  for (const t of list) {
    if (!out.includes(t.category) && (t.category !== ADVANCED || showAdvanced)) out.push(t.category);
  }
  return out;
}

/** Templates of a category matching the search (name, description, apps). */
export function filterTemplates(list: WorkflowTemplate[], category: string, query: string, showAdvanced: boolean,
  catalog: Map<string, NodeDefinition>): WorkflowTemplate[] {
  const q = query.trim().toLowerCase();
  return list.filter((t) => {
    if (t.category === ADVANCED && !showAdvanced) return false;
    if (category !== ALL && t.category !== category) return false;
    if (!q) return true;
    const apps = appsOf(t.node_types, catalog).map((a) => a.name).join(" ");
    return `${t.name} ${t.description} ${apps} ${t.category}`.toLowerCase().includes(q);
  });
}

function TemplateCard({ template, catalog, onOpen }: { template: WorkflowTemplate; catalog: Map<string, NodeDefinition>; onOpen: () => void }) {
  const apps = appsOf(template.node_types, catalog);
  const ai = template.node_types.some((t) => t === "llm" || t === "transcribe_audio");
  return (
    <button type="button" className="gallery-card" onClick={onOpen} data-testid={`template-${template.id}`}>
      <span className="gallery-icons" aria-hidden>
        {apps.map((a) => <NodeIcon key={a.id} integrationId={a.id} size={22} />)}
        {ai && <NodeIcon def={{ type: "llm", category: "ai" }} size={22} />}
        {!apps.length && !ai && <NodeIcon def={{ type: template.node_types[1] ?? "text", category: "" }} size={22} />}
      </span>
      <b>{template.name}</b>
      <span className="small muted">{template.description}</span>
      {template.saved && <span className="badge draft">Saved</span>}
    </button>
  );
}

export function TemplateGallery() {
  const router = useRouter();
  const g = useTemplateGallery();
  const showAdvanced = useShowDetails();
  const [category, setCategory] = useState(ALL);
  const [query, setQuery] = useState("");
  const [open, setOpen] = useState<WorkflowTemplate | null>(null);

  const list = useMemo(() => g.templates ?? [], [g.templates]);
  const categories = categoriesOf(list, showAdvanced);
  const active = categories.includes(category) ? category : ALL;
  const shown = filterTemplates(list, active, query, showAdvanced, g.catalog);

  return (
    <main className="dashboard stack gallery">
      <div className="row">
        <div>
          <h1>Templates</h1>
          <p className="muted">Start from a ready-made workflow. Pick one to see what you need before you start.</p>
        </div>
        <span className="spacer" />
        {g.workspaces.length > 1 && (
          <select aria-label="Workspace" style={{ width: 240 }} value={g.workspace?.id ?? ""} onChange={(e) => g.setWorkspaceId(e.target.value)}>
            {g.workspaces.map((w) => <option key={w.id} value={w.id}>{w.name}</option>)}
          </select>
        )}
      </div>
      <ErrorBanner error={g.error} />
      <div className="gallery-filters">
        <input type="search" aria-label="Search templates" placeholder="Search templates, e.g. Gmail or summary" value={query}
          onChange={(e) => setQuery(e.target.value)} />
        <div className="gallery-tabs" role="tablist" aria-label="Categories">
          {categories.map((c) => (
            <button key={c} type="button" role="tab" aria-selected={c === active} className={c === active ? "active" : ""}
              onClick={() => setCategory(c)}>{c}</button>
          ))}
        </div>
      </div>
      {g.templates === null ? (
        <div className="empty"><Spinner /> Loading templates…</div>
      ) : shown.length === 0 ? (
        <div className="empty">
          <b>{active === SAVED ? "No saved templates yet" : "No templates match"}</b>
          <div className="small">{active === SAVED ? "Open a workflow and choose “Save as template” to add it here." : "Try another word or category."}</div>
        </div>
      ) : (
        <div className="gallery-grid">
          {shown.map((t) => <TemplateCard key={t.id} template={t} catalog={g.catalog} onOpen={() => setOpen(t)} />)}
        </div>
      )}
      {open && (
        <TemplateDetails template={open} catalog={g.catalog} have={g.have} canCreate={g.canCreate}
          onClose={() => setOpen(null)}
          onUse={async (name) => {
            const wf = await g.use(open, name);
            router.push(`/workflows/${wf.id}/editor`);
          }}
          onDelete={open.saved && g.canCreate ? async () => {
            await g.remove(open);
            setOpen(null);
          } : undefined} />
      )}
    </main>
  );
}
