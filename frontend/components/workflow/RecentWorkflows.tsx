"use client";

import { useState, type ReactNode } from "react";
import { motifOf } from "@/components/ui/Backdrop";
import { NodeIcon } from "@/components/ui/NodeIcon";
import { stepIcons, type CardDetails } from "@/features/workflows/useWorkflowCards";
import { timeAgo } from "@/lib/time";
import type { NodeDefinition, Workflow } from "@/types/api";

/**
 * The Recent rail on the Workflows page: a slim column on the left with a
 * stack of the latest workflows that slides open into the full list on
 * hover. Clicking it keeps it open (the only way on touch screens); focus
 * inside it or a search in progress keeps it open too.
 */
export function RecentWorkflows({ workflows, details, catalog, loading, forceOpen, controls, children }: {
  workflows: Workflow[];
  details: Map<string, CardDetails>;
  catalog: Map<string, NodeDefinition>;
  loading: boolean;
  /** Keep the rail open regardless of hover (a search is active). */
  forceOpen: boolean;
  /** Workspace picker and search, shown at the top of the open rail. */
  controls: ReactNode;
  children: ReactNode;
}) {
  const [pinned, setPinned] = useState(false);
  const open = pinned || forceOpen;
  const latest = workflows[0];

  return (
    <aside className={`wf-rail${open ? " open" : ""}`} data-testid="recents" aria-label="Recent workflows">
      <button type="button" className="wf-rail-closed" aria-expanded={open} aria-controls="wf-rail-panel" onClick={() => setPinned(true)}>
        <span className="wf-chevron" aria-hidden>
          <svg viewBox="0 0 16 16" width="14" height="14"><path d="M6 4l4 4-4 4" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" /></svg>
        </span>
        {!loading && <span className="wf-count">{workflows.length}</span>}
        <span className="wf-rail-label">Recent</span>
        <span className="wf-stack" aria-hidden>
          {workflows.slice(0, 5).map((wf, i) => {
            const d = details.get(wf.id);
            const ic = d ? stepIcons(d.steps, catalog)[0] : undefined;
            return (
              <span key={wf.id} className={`wf-mini motif ${motifOf(wf.id)}`} style={{ zIndex: 5 - i }}>
                <span className="wf-mini-icon">
                  {ic ? <NodeIcon integrationId={ic.integrationId} def={ic.def} size={13} /> : null}
                </span>
              </span>
            );
          })}
          {!loading && workflows.length === 0 && <span className="wf-mini wf-mini-empty">+</span>}
        </span>
      </button>

      <div className="wf-rail-panel" id="wf-rail-panel">
        <div className="wf-rail-head">
          <div className="wf-rail-title">
            <span className="wf-recents-title">Recent{!loading && <span className="wf-count">{workflows.length}</span>}</span>
            <button type="button" className={`wf-pin${pinned ? " on" : ""}`} aria-pressed={pinned} onClick={() => setPinned(!pinned)}>
              {pinned ? "Unpin" : "Pin open"}
            </button>
          </div>
          <span className="wf-recents-sub">
            {loading ? "Loading your workflows…"
              : latest ? <>Last edited <b>{latest.name}</b> · {timeAgo(latest.updated_at)}</>
              : "Nothing here yet"}
          </span>
          <div className="wf-recents-controls">{controls}</div>
        </div>
        <div className="wf-rail-list">{children}</div>
      </div>
    </aside>
  );
}
