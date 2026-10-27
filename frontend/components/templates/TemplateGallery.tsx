"use client";

// The Templates page (Phase K): ready-made workflows by category, the
// workspace's saved ones, and for each what you need before you start.

import { useRouter } from "next/navigation";
import { useMemo, useState, type ReactNode } from "react";
import { Backdrop } from "@/components/ui/Backdrop";
import { NodeIcon } from "@/components/ui/NodeIcon";
import { ErrorBanner } from "@/components/ui/ErrorBanner";
import { useTemplateGallery } from "@/features/templates/useTemplateGallery";
import { useProviders } from "@/features/connected-accounts/useProviders";
import { comingSoonApps, unavailableApps } from "@/lib/workflow/availability";
import { appsOf } from "@/lib/workflow/requirements";
import { useShowDetails } from "@/stores/ui/details";
import type { NodeDefinition, WorkflowTemplate } from "@/types/api";
import { TemplateDetails } from "./TemplateDetails";

export const ALL = "All";
export const SAVED = "Saved";
const ADVANCED = "Advanced";

/** Shown first under "All": the templates most people start with. */
const FEATURED = ["inbox-summary", "todays-agenda", "meeting-recording-notes"];
const FEATURED_MOTIFS = ["motif-teal", "motif-charcoal", "motif-sun"];

/** Line icons and colours for the categories. */
const CATEGORY_LOOK: Record<string, { glyph: string; tone: string }> = {
  [ALL]: { glyph: "M4 4h7v7H4zM13 4h7v7h-7zM4 13h7v7H4zM13 13h7v7h-7z", tone: "tone-charcoal" },
  Email: { glyph: "M3 6h18v12H3zM3 7l9 6 9-6", tone: "tone-peach" },
  Meetings: { glyph: "M4 6h16v14H4zM4 10h16M9 3v4M15 3v4", tone: "tone-teal" },
  Writing: { glyph: "M4 20l4-1 11-11-3-3L5 16zM14 6l3 3", tone: "tone-sun" },
  "Sales & customers": { glyph: "M9 11a3 3 0 100-6 3 3 0 000 6zM3 20c0-3 3-5 6-5s6 2 6 5M16 5a3 3 0 010 6M21 20c0-2-1.5-4-4-4.5", tone: "tone-sandy" },
  "Team & projects": { glyph: "M4 8h16v11H4zM9 8V5h6v3M4 13h16", tone: "tone-charcoal" },
  [SAVED]: { glyph: "M7 4h10v16l-5-4-5 4z", tone: "tone-teal" },
  [ADVANCED]: { glyph: "M9 8l-4 4 4 4M15 8l4 4-4 4", tone: "tone-gray" },
};

function lookOf(category: string) {
  return CATEGORY_LOOK[category] ?? { glyph: CATEGORY_LOOK[ALL].glyph, tone: "tone-gray" };
}

function Glyph({ d, size = 16 }: { d: string; size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" aria-hidden fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round">
      <path d={d} />
    </svg>
  );
}

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

const usesAI = (t: WorkflowTemplate) => t.node_types.some((x) => x === "llm" || x === "transcribe_audio");

/** What a template needs, in a word or two each: "Google", "AI key". */
function needsOf(t: WorkflowTemplate, catalog: Map<string, NodeDefinition>): string[] {
  const out: string[] = [];
  for (const a of appsOf(t.node_types, catalog)) {
    const name = a.id.startsWith("google_") || a.id === "gmail" ? "Google" : a.name;
    if (!out.includes(name)) out.push(name);
  }
  if (usesAI(t)) out.push("AI key");
  return out;
}

/** The template's apps in order, with AI, as icons. */
function TemplateIcons({ template, catalog, size }: { template: WorkflowTemplate; catalog: Map<string, NodeDefinition>; size: number }) {
  const apps = appsOf(template.node_types, catalog);
  const icons = [...apps.map((a) => <NodeIcon key={a.id} integrationId={a.id} size={size} />)];
  if (usesAI(template)) icons.splice(Math.min(1, icons.length), 0, <NodeIcon key="ai" def={{ type: "llm", category: "ai" }} size={size} />);
  if (icons.length === 0) icons.push(<NodeIcon key="x" def={{ type: template.node_types[1] ?? "text", category: "" }} size={size} />);
  return <>{icons.slice(0, 3).map((ic, i) => <span key={i} className="tpl-step">{i > 0 && <span className="wf-link" aria-hidden />}<span className="wf-icon">{ic}</span></span>)}</>;
}

/** A template's apps in order, with AI, as small icons joined by lines. */
function StepIcons({ template, catalog }: { template: WorkflowTemplate; catalog: Map<string, NodeDefinition> }) {
  const keys: { key: string; node: ReactNode }[] = appsOf(template.node_types, catalog).map((a) => ({ key: a.id, node: <NodeIcon integrationId={a.id} size={13} /> }));
  if (usesAI(template)) keys.splice(Math.min(1, keys.length), 0, { key: "ai", node: <NodeIcon def={{ type: "llm", category: "ai" }} size={13} /> });
  return (
    <span className="tpl-steps" aria-hidden>
      {keys.slice(0, 4).map((k, i) => <span key={k.key} className="tpl-mini-step">{i > 0 && <span className="tpl-mini-link" />}<span className="tpl-mini">{k.node}</span></span>)}
    </span>
  );
}

function TemplateCard({ template, catalog, soon, onOpen }: {
  template: WorkflowTemplate; catalog: Map<string, NodeDefinition>; soon: string[]; onOpen: () => void;
}) {
  const first = appsOf(template.node_types, catalog)[0];
  const look = lookOf(template.category);
  const tile = soon.length ? "tile-gray" : look.tone.replace("tone-", "tile-");
  return (
    <button type="button" className={`tpl-card ${look.tone.replace("tone-", "card-")} ${soon.length ? "soon" : ""}`} onClick={onOpen} data-testid={`template-${template.id}`}
      disabled={soon.length > 0} title={soon.length ? `Coming soon: needs ${soon.join(" and ")}` : undefined}>
      <span className="tpl-head">
        <span className={`tpl-icon ${tile}`} aria-hidden>
          <span className="tpl-icon-inner">
            {first ? <NodeIcon integrationId={first.id} size={18} /> : usesAI(template) ? <NodeIcon def={{ type: "llm", category: "ai" }} size={18} /> : <Glyph d={look.glyph} size={16} />}
          </span>
        </span>
        <b>{template.name}</b>
      </span>
      <span className="tpl-desc">{template.description}</span>
      <span className="tpl-foot">
        {!soon.length && <StepIcons template={template} catalog={catalog} />}
        <span className="tpl-needs">
          {soon.length > 0 ? <em>Coming soon · {soon.join(", ")}</em> : <em>{needsOf(template, catalog).join(" · ") || "Ready to use"}</em>}
          {template.saved && !soon.length && <em className="saved">Saved</em>}
        </span>
        {!soon.length && <span className="tpl-use" aria-hidden>Use →</span>}
      </span>
    </button>
  );
}
function FeaturedCard({ template, catalog, motif, onOpen }: { template: WorkflowTemplate; catalog: Map<string, NodeDefinition>; motif: string; onOpen: () => void }) {
  return (
    <button type="button" className="wf-card tpl-featured" onClick={onOpen} data-testid={`featured-${template.id}`}>
      <span className={`wf-band motif ${motif}`}><TemplateIcons template={template} catalog={catalog} size={18} /></span>
      <span className="wf-body">
        <b className="wf-name">{template.name}</b>
        <span className="tpl-desc">{template.description}</span>
      </span>
    </button>
  );
}

export function TemplateGallery() {
  const router = useRouter();
  const g = useTemplateGallery();
  const showAdvanced = useShowDetails();
  const providers = useProviders();
  const unavailable = useMemo(() => unavailableApps(g.catalog.values(), providers), [g.catalog, providers]);
  const [category, setCategory] = useState(ALL);
  const [query, setQuery] = useState("");
  const [picked, setPicked] = useState<WorkflowTemplate | null>(null);
  // /templates?template=<id> opens that template (links from the Workflows page).
  const [wanted, setWanted] = useState(() => (typeof window === "undefined" ? null : new URLSearchParams(window.location.search).get("template")));

  const list = useMemo(() => g.templates ?? [], [g.templates]);
  const open = picked ?? (wanted ? list.find((t) => t.id === wanted) ?? null : null);
  const setOpen = (t: WorkflowTemplate | null) => {
    setPicked(t);
    setWanted(null);
  };
  const categories = categoriesOf(list, showAdvanced);
  const active = categories.includes(category) ? category : ALL;
  // Templates that need an app not set up yet go last, as Coming soon.
  const soonOf = (t: WorkflowTemplate) => comingSoonApps(t.node_types, g.catalog, unavailable);
  const sorted = (ts: WorkflowTemplate[]) => ts
    .map((t, i) => ({ t, i, soon: soonOf(t) }))
    .sort((a, b) => Number(a.soon.length > 0) - Number(b.soon.length > 0) || a.i - b.i);
  const shown = sorted(filterTemplates(list, active, query, showAdvanced, g.catalog));
  // Under "All" with no search: featured first, then one group per category.
  const grouped = active === ALL && !query.trim();
  const featured = FEATURED.map((id) => list.find((t) => t.id === id)).filter((t): t is WorkflowTemplate => Boolean(t) && soonOf(t!).length === 0);
  const count = list.filter((t) => !t.saved && t.category !== ADVANCED).length;

  const grid = (items: typeof shown) => (
    <div className="tpl-grid">
      {items.map(({ t, soon }) => <TemplateCard key={t.id} template={t} catalog={g.catalog} soon={soon} onOpen={() => setOpen(t)} />)}
    </div>
  );

  return (
    <main className="wf-page textured gallery">
      <section className="wf-hero tpl-hero">
        <Backdrop apps={["gmail", "google_sheets", "google_calendar", "google_meet"]} />
        <div className="wf-hero-inner">
          <span className="wf-eyebrow">Templates</span>
          <h1>Start from something that already works</h1>
          <p>{count > 0 ? `${count} ready-made workflows.` : "Ready-made workflows."} Pick one, fill in the blanks, press Run.</p>
          <label className="tpl-search">
            <Glyph d="M11 4a7 7 0 100 14 7 7 0 000-14zM20 20l-4-4" />
            <input type="search" aria-label="Search templates" placeholder="Search, like “email” or “summary”" value={query}
              onChange={(e) => setQuery(e.target.value)} />
          </label>
          <div className="tpl-chips" role="tablist" aria-label="Categories">
            {categories.map((c) => (
              <button key={c} type="button" role="tab" aria-selected={c === active} className={c === active ? "active" : ""}
                onClick={() => setCategory(c)}>
                <Glyph d={lookOf(c).glyph} size={14} />{c}
              </button>
            ))}
          </div>
          {g.workspaces.length > 1 && (
            <select aria-label="Workspace" className="wf-select tpl-workspace" value={g.workspace?.id ?? ""} onChange={(e) => g.setWorkspaceId(e.target.value)}>
              {g.workspaces.map((w) => <option key={w.id} value={w.id}>{w.name}</option>)}
            </select>
          )}
        </div>
      </section>

      <section className="wf-list">
        <ErrorBanner error={g.error} />
        {g.templates === null ? (
          <div className="wf-grid">{[0, 1, 2].map((i) => <div key={i} className="wf-card wf-skeleton" aria-hidden />)}</div>
        ) : shown.length === 0 ? (
          <div className="empty">
            <b>{active === SAVED ? "No saved templates yet" : "No templates match"}</b>
            <div className="small">{active === SAVED ? "Open a workflow and choose “Save as template” to add it here." : "Try another word or category."}</div>
          </div>
        ) : grouped ? (
          <>
            {featured.length > 0 && (
              <>
                <div className="tpl-group-head"><h2>Most popular</h2></div>
                <div className="wf-grid tpl-featured-grid">
                  {featured.map((t, i) => <FeaturedCard key={t.id} template={t} catalog={g.catalog} motif={FEATURED_MOTIFS[i % FEATURED_MOTIFS.length]} onOpen={() => setOpen(t)} />)}
                </div>
              </>
            )}
            {categories.filter((c) => c !== ALL).map((c) => {
              const items = shown.filter(({ t }) => t.category === c);
              if (items.length === 0) return null;
              return (
                <div key={c} className={`tpl-group shelf ${lookOf(c).tone.replace("tone-", "shelf-")}`}>
                  <div className="tpl-group-head">
                    <span className={`tpl-group-icon ${lookOf(c).tone.replace("tone-", "tile-")}`}><Glyph d={lookOf(c).glyph} size={14} /></span>
                    <h2>{c}</h2>
                    <span className="muted">{items.length} {items.length === 1 ? "template" : "templates"}</span>
                  </div>
                  {grid(items)}
                </div>
              );
            })}
          </>
        ) : (
          grid(shown)
        )}
      </section>

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
