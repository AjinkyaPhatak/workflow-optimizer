"use client";

import Link from "next/link";
import { useEffect, useRef, useState, type FormEvent } from "react";
import { motifOf } from "@/components/ui/Backdrop";
import { ErrorBanner } from "@/components/ui/ErrorBanner";
import { NodeIcon } from "@/components/ui/NodeIcon";
import { stepIcons, type CardDetails } from "@/features/workflows/useWorkflowCards";
import { timeAgo } from "@/lib/time";
import type { NodeDefinition, Workflow } from "@/types/api";

/** One line of status: live or draft, and how the last run went. */
function statusOf(wf: Workflow, d: CardDetails | undefined): { tone: "live" | "draft" | "failed" | "running"; text: string } {
  const last = d?.last;
  if (last?.status === "FAILED") return { tone: "failed", text: `Last run didn't finish · ${timeAgo(last.created_at)}` };
  if (last && (last.status === "RUNNING" || last.status === "PENDING")) return { tone: "running", text: "Running now" };
  if (last?.status === "WAITING_FOR_INPUT") return { tone: "running", text: "Waiting for your answer" };
  if (last?.status === "WAITING_FOR_EVENT") return { tone: "running", text: "Waiting for an event" };
  if (last?.status === "PAUSED") return { tone: "draft", text: "Run paused" };
  if (last?.status === "CANCELLED") return { tone: "draft", text: `Last run cancelled · ${timeAgo(last.created_at)}` };
  if (last?.status === "COMPLETED") return { tone: "live", text: `Last run completed · ${timeAgo(last.created_at)}` };
  if (d?.ready && !wf.active_version_id) return { tone: "live", text: "Ready · not run yet" };
  if (!wf.active_version_id) return { tone: "draft", text: `Draft · edited ${timeAgo(wf.updated_at)}` };
  return { tone: "live", text: last ? `Live · ran ${timeAgo(last.created_at)}` : "Live · not run yet" };
}

export function WorkflowCard({ wf, details, catalog, kind, canEdit, onRename, onDelete }: {
  wf: Workflow;
  details: CardDetails | undefined;
  catalog: Map<string, NodeDefinition>;
  kind?: "compiler" | "legacy";
  canEdit: boolean;
  onRename: (name: string) => Promise<void>;
  onDelete: () => Promise<void>;
}) {
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const [menu, setMenu] = useState(false);
  const [renaming, setRenaming] = useState(false);
  const [name, setName] = useState(wf.name);
  const ref = useRef<HTMLDivElement>(null);
  const icons = details ? stepIcons(details.steps, catalog) : [];
  const status = statusOf(wf, details);
  const href = `/workflows/${wf.id}/editor`;

  useEffect(() => {
    if (!menu) return;
    const close = (e: MouseEvent | KeyboardEvent) => {
      if (e instanceof KeyboardEvent ? e.key === "Escape" : !ref.current?.contains(e.target as Node)) setMenu(false);
    };
    document.addEventListener("mousedown", close);
    document.addEventListener("keydown", close);
    return () => {
      document.removeEventListener("mousedown", close);
      document.removeEventListener("keydown", close);
    };
  }, [menu]);

  async function rename(e: FormEvent) {
    e.preventDefault();
    if (busy) return;
    const next = name.trim();
    if (!next) return;
    setError(null);
    setBusy(true);
    try {
      if (next !== wf.name) await onRename(next);
      setRenaming(false);
    } catch (err) {
      setError(err);
    } finally {
      setBusy(false);
    }
  }

  async function remove() {
    if (busy || !window.confirm(`Delete "${wf.name}"? You can't undo this.`)) return;
    setError(null);
    setBusy(true);
    try {
      await onDelete();
      setMenu(false);
    } catch (err) {
      setError(err);
      setMenu(true);
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={`wf-card${menu ? " menu-open" : ""}`} ref={ref}>
      <Link href={href} className={`wf-band motif ${motifOf(wf.id)}`} aria-hidden="true" tabIndex={-1}>
        {icons.length > 0 ? icons.map((ic, i) => (
          <span key={ic.key} className="wf-step">
            {i > 0 && <span className="wf-link" aria-hidden />}
            <span className="wf-icon"><NodeIcon integrationId={ic.integrationId} def={ic.def} size={18} /></span>
          </span>
        )) : <span className="wf-icon wf-icon-empty" aria-hidden />}
      </Link>
      <div className="wf-body">
        {renaming ? (
          <form onSubmit={rename} className="wf-rename">
            <input aria-label="Workflow name" value={name} autoFocus maxLength={255} readOnly={busy} onChange={(e) => setName(e.target.value)}
              onBlur={() => { if (!busy && !error) setRenaming(false); }} onKeyDown={(e) => e.key === "Escape" && setRenaming(false)} />
          </form>
        ) : (
          <Link href={href} className="wf-name">{wf.name}</Link>
        )}
        <span className={`wf-status ${status.tone}`}><span className="dot" aria-hidden />{status.text}</span>
        <ErrorBanner error={error} />
      </div>
      <button type="button" className="wf-more" aria-label={`More for ${wf.name}`} aria-haspopup="menu" aria-expanded={menu}
        onClick={() => setMenu(!menu)}>⋯</button>
      {menu && (
        <div className="wf-menu" role="menu">
          <Link role="menuitem" href={href}>Open</Link>
          {kind && <Link role="menuitem" href={kind === "compiler" ? href : `/workflows/${wf.id}/executions`}>Run history</Link>}
          {canEdit && kind === "legacy" && (
            <>
              <button type="button" role="menuitem" disabled={busy} onClick={() => { setError(null); setMenu(false); setName(wf.name); setRenaming(true); }}>Rename</button>
              <button type="button" role="menuitem" className="danger" disabled={busy} onClick={() => void remove()}>Delete</button>
            </>
          )}
        </div>
      )}
    </div>
  );
}
