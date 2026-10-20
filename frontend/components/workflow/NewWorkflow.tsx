"use client";

import Link from "next/link";
import { useState, type FormEvent } from "react";
import { Spinner } from "@/components/ui/BrandMark";
import { ErrorBanner } from "@/components/ui/ErrorBanner";
import type { Project, Workflow } from "@/types/api";

/** Creating a workflow: an empty one here, or from a ready-made one in the
 * Templates gallery (Phase K). */
export function NewWorkflow({ projects, create, onCreated }: {
  projects: Project[];
  create: (name: string, projectId?: string) => Promise<Workflow>;
  onCreated: (wf: Workflow) => void;
}) {
  const [name, setName] = useState("");
  const [projectId, setProjectId] = useState("");
  const [creating, setCreating] = useState(false);
  const [error, setError] = useState<unknown>(null);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setCreating(true);
    setError(null);
    try {
      onCreated(await create(name.trim(), projectId || undefined));
    } catch (err) {
      setError(err);
      setCreating(false);
    }
  }

  return (
    <form className="card new-workflow" onSubmit={submit} data-testid="new-workflow">
      <div className="row">
        <h3>New workflow</h3>
        <span className="spacer" />
        <Link className="button primary" href="/templates" data-testid="browse-templates">Start from a template</Link>
      </div>
      <p className="small muted">Templates are ready-made workflows for email, documents, meetings and more. Or start with an empty canvas:</p>
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
        <button type="submit" disabled={creating || !name.trim()}>
          {creating && <Spinner />}
          {creating ? "Creating…" : "Create empty workflow"}
        </button>
      </div>
    </form>
  );
}
