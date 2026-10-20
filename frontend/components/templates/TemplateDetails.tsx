"use client";

// One template up close (Phase K): its steps in plain words, "Before you
// start" (accounts and keys, ticked when the workspace has them, and the
// template's own notes), and "Use this template".

import Link from "next/link";
import { useState, type FormEvent } from "react";
import { Spinner } from "@/components/ui/BrandMark";
import { ErrorBanner } from "@/components/ui/ErrorBanner";
import { NodeIcon } from "@/components/ui/NodeIcon";
import { friendlyNodeName } from "@/lib/workflow/labels";
import { requirements } from "@/lib/workflow/requirements";
import type { ConnectedAccount, Credential, NodeDefinition, WorkflowTemplate } from "@/types/api";

export function TemplateDetails({ template, catalog, have, canCreate, onClose, onUse, onDelete }: {
  template: WorkflowTemplate;
  catalog: Map<string, NodeDefinition>;
  have: { accounts: ConnectedAccount[]; credentials: Credential[] } | null;
  canCreate: boolean;
  onClose: () => void;
  onUse: (name: string) => Promise<void>;
  onDelete?: () => Promise<void>;
}) {
  const [name, setName] = useState(template.name);
  const [busy, setBusy] = useState<"use" | "delete" | null>(null);
  const [error, setError] = useState<unknown>(null);
  const needs = requirements(template.definition, catalog, have, template.setup);
  const steps = template.definition.nodes.filter((n) => n.type !== "input" && n.type !== "output");
  const questions = template.definition.nodes
    .map((n) => (n.type === "input" && typeof n.config.field === "string" ? n.config.field : ""))
    .filter(Boolean);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy("use");
    setError(null);
    try {
      await onUse(name.trim() || template.name);
    } catch (err) {
      setError(err);
      setBusy(null);
    }
  }

  return (
    <div className="run-overlay" role="dialog" aria-modal="true" aria-labelledby="template-title" data-testid="template-details"
      onMouseDown={(ev) => ev.target === ev.currentTarget && onClose()}>
      <form className="run-card template-details" onSubmit={submit}>
        <div className="run-head">
          <h2 id="template-title">{template.name}</h2>
          <span className="spacer" />
          <button type="button" className="icon-button" aria-label="Close" onClick={onClose}>✕</button>
        </div>
        <div className="template-body">
          <p>{template.description}</p>
          <h3>What it does</h3>
          <ol className="template-steps">
            {questions.length > 0 && (
              <li><span className="step-icon"><NodeIcon def={{ type: "input", category: "" }} size={18} /></span>
                Asks you for: {questions.map((q) => q.replace(/_/g, " ")).join(", ")}</li>
            )}
            {steps.map((n) => {
              const def = catalog.get(n.type);
              const app = def?.integration?.name;
              return (
                <li key={n.id}>
                  <span className="step-icon"><NodeIcon def={def ?? { type: n.type, category: "" }} size={18} /></span>
                  {n.name}{app ? <span className="muted"> · {app}</span> : def && !def.integration ? <span className="muted"> · {friendlyNodeName(def)}</span> : null}
                </li>
              );
            })}
          </ol>
          <h3>Before you start</h3>
          {needs.length === 0 ? (
            <p className="muted small">Nothing to prepare: it is ready to use.</p>
          ) : (
            <ul className="checklist" data-testid="before-you-start">
              {needs.map((r) => (
                <li key={r.key} className={r.done ? "done" : ""}>
                  <span className="check" aria-label={r.kind === "note" ? undefined : r.done ? "Done" : "To do"}>
                    {r.kind === "note" ? "•" : r.done ? "✓" : "○"}
                  </span>
                  <span>{r.label}</span>
                  {r.href && !r.done && <Link className="button small-button" href={r.href} target="_blank">{r.action}</Link>}
                </li>
              ))}
            </ul>
          )}
          <ErrorBanner error={error} />
          {canCreate ? (
            <div className="field">
              <label htmlFor="template-name">Name your workflow</label>
              <input id="template-name" value={name} onChange={(e) => setName(e.target.value)} maxLength={255} />
            </div>
          ) : (
            <p className="muted small">Viewers cannot create workflows in this workspace.</p>
          )}
        </div>
        <div className="row template-actions">
          {onDelete && (
            <button type="button" className="danger" disabled={busy !== null} onClick={async () => {
              if (!window.confirm(`Delete the saved template “${template.name}”? Workflows made from it are kept.`)) return;
              setBusy("delete");
              try {
                await onDelete();
              } catch (err) {
                setError(err);
                setBusy(null);
              }
            }}>{busy === "delete" ? "Deleting…" : "Delete template"}</button>
          )}
          <span className="spacer" />
          <span className="small muted">You can change anything after.</span>
          <button className="primary" type="submit" disabled={!canCreate || busy !== null} data-testid="use-template">
            {busy === "use" && <Spinner />}
            {busy === "use" ? "Creating…" : "Use this template"}
          </button>
        </div>
      </form>
    </div>
  );
}
