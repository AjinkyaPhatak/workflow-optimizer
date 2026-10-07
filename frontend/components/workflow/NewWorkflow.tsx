"use client";

import { useEffect, useState, type FormEvent } from "react";
import { Spinner } from "@/components/ui/BrandMark";
import { ErrorBanner } from "@/components/ui/ErrorBanner";
import { workflowApi } from "@/lib/api";
import type { Project, Workflow, WorkflowTemplate } from "@/types/api";

const BLANK = "";

/** Creating a workflow: blank, or from a template (the backend copies the
 * template's definition into the first draft with fresh node IDs). */
export function NewWorkflow({ projects, create, onCreated }: {
  projects: Project[];
  create: (name: string, projectId?: string, templateId?: string) => Promise<Workflow>;
  onCreated: (wf: Workflow) => void;
}) {
  const [name, setName] = useState("");
  const [projectId, setProjectId] = useState("");
  const [choice, setChoice] = useState(BLANK);
  const [templates, setTemplates] = useState<WorkflowTemplate[] | null>(null);
  const [creating, setCreating] = useState(false);
  const [error, setError] = useState<unknown>(null);

  useEffect(() => {
    let cancelled = false;
    workflowApi
      .templates()
      .then((l) => !cancelled && setTemplates(l.items))
      .catch(() => !cancelled && setTemplates([]));
    return () => {
      cancelled = true;
    };
  }, []);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setCreating(true);
    setError(null);
    try {
      onCreated(await create(name.trim(), projectId || undefined, choice || undefined));
    } catch (err) {
      setError(err);
      setCreating(false);
    }
  }

  const chosen = templates?.find((t) => t.id === choice);
  return (
    <form className="card new-workflow" onSubmit={submit} data-testid="new-workflow">
      <h3>New workflow</h3>
      <ErrorBanner error={error} />
      <div className="row">
        <input aria-label="Workflow name" placeholder="Workflow name" value={name} onChange={(e) => setName(e.target.value)} required maxLength={255} />
        {projects.length > 1 && (
          <select aria-label="Project" style={{ width: 200 }} value={projectId} onChange={(e) => setProjectId(e.target.value)}>
            {projects.map((p) => (
              <option key={p.id} value={p.id}>{p.name}</option>
            ))}
          </select>
        )}
      </div>
      <div className="start-label small muted">Start from</div>
      <div className="template-grid" role="radiogroup" aria-label="Start from">
        <button type="button" role="radio" aria-checked={choice === BLANK} className={`template-card ${choice === BLANK ? "selected" : ""}`} onClick={() => setChoice(BLANK)}>
          <b>Blank workflow</b>
          <span className="small muted">An empty canvas.</span>
        </button>
        {templates === null && (
          <div className="template-card muted small"><Spinner /> Loading templates…</div>
        )}
        {templates?.map((t) => (
          <button key={t.id} type="button" role="radio" aria-checked={choice === t.id} data-testid={`template-${t.id}`}
            className={`template-card ${choice === t.id ? "selected" : ""}`} onClick={() => setChoice(t.id)}>
            <b>{t.name}</b>
            <span className="small muted">{t.description}</span>
            <span className="template-flow small">{t.definition.nodes.map((n) => n.name).join(" → ")}</span>
          </button>
        ))}
      </div>
      <div className="row">
        <span className="small muted">{chosen ? `Creates a draft from “${chosen.name}”.` : "Creates an empty draft workflow."}</span>
        <span className="spacer" />
        <button className="primary" type="submit" disabled={creating || !name.trim()}>
          {creating && <Spinner />}
          {creating ? "Creating…" : "Create workflow"}
        </button>
      </div>
    </form>
  );
}
