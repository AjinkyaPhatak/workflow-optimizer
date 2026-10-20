"use client";

// The Run window (Phase I1/I3): the workflow's questions, a Run button, and
// the result in plain words, or what went wrong and how to fix it. It stays
// mounted while closed so a run keeps going and the answers are kept.

import Link from "next/link";
import { useEffect, useMemo, useState } from "react";
import { FriendlyValue } from "@/components/run/FriendlyValue";
import { useEditorContext } from "@/features/workflows/EditorContext";
import { useFocusIssue, useIssues } from "@/features/workflows/useIssues";
import { useRunWorkflow } from "@/features/workflows/useRunWorkflow";
import { fileApi } from "@/lib/api";
import { friendlyError } from "@/lib/workflow/friendlyErrors";
import { questionLabel, runFields, runInput, type RunField } from "@/lib/workflow/runFields";
import { useShowDetails } from "@/stores/ui/details";
import { useEditorStore } from "@/stores/workflow-editor/store";
import { useTestRunStore } from "@/stores/test-run/store";
import type { WorkspaceFile } from "@/types/api";

/** After this long still queued, the runner is probably not started. */
const QUEUED_HINT_MS = 12_000;

function Question({ field, value, files, onChange }: { field: RunField; value: string; files: WorkspaceFile[] | null; onChange: (v: string) => void }) {
  const id = `run-q-${field.name}`;
  const label = (
    <label htmlFor={id}>
      {questionLabel(field.name)}
      {field.required ? <span className="req"> *</span> : <span className="muted small"> (optional)</span>}
    </label>
  );
  if (field.kind === "file") {
    return (
      <div className="field">
        {label}
        <select id={id} value={value} onChange={(e) => onChange(e.target.value)}>
          <option value="">{files === null ? "Loading files…" : files.length ? "Choose a file…" : "No files yet"}</option>
          {(files ?? []).map((f) => <option key={f.id} value={f.id}>{f.name}</option>)}
        </select>
        <div className="hint">Upload files on the <Link href="/files" target="_blank">Files page</Link>.</div>
      </div>
    );
  }
  if (field.kind === "boolean") {
    return (
      <div className="field">
        {label}
        <select id={id} value={value} onChange={(e) => onChange(e.target.value)}>
          <option value="">{field.variable?.default !== undefined && field.variable?.default !== null ? `Default (${field.variable.default ? "Yes" : "No"})` : "Choose…"}</option>
          <option value="true">Yes</option>
          <option value="false">No</option>
        </select>
      </div>
    );
  }
  const placeholder = field.variable?.default !== undefined && field.variable?.default !== null
    ? `Default: ${typeof field.variable.default === "string" ? field.variable.default : JSON.stringify(field.variable.default)}` : undefined;
  return (
    <div className="field">
      {label}
      <textarea id={id} rows={field.kind === "text" ? 2 : 1} value={value} placeholder={placeholder} onChange={(e) => onChange(e.target.value)} />
    </div>
  );
}

export function RunDialog({ open, onClose, workflowId }: { open: boolean; onClose: () => void; workflowId: string }) {
  const { workspaceId, workflow, catalog } = useEditorContext();
  const definition = useEditorStore((s) => s.definition);
  const fields = useMemo(() => runFields(definition), [definition]);
  const answers = useTestRunStore((s) => s.answers);
  const setAnswer = useTestRunStore((s) => s.setAnswer);
  const [missing, setMissing] = useState<string[]>([]);
  const [files, setFiles] = useState<WorkspaceFile[] | null>(null);
  const [now, setNow] = useState(() => Date.now());
  const run = useRunWorkflow(workflowId);
  const issues = useIssues().filter((i) => i.severity === "error");
  const focus = useFocusIssue();
  const details = useShowDetails();

  const needsFiles = fields.some((f) => f.kind === "file");
  useEffect(() => {
    if (!open || !needsFiles || !workspaceId || files !== null) return;
    let cancelled = false;
    fileApi.list(workspaceId).then((l) => !cancelled && setFiles(l.items), () => !cancelled && setFiles([]));
    return () => {
      cancelled = true;
    };
  }, [open, needsFiles, workspaceId, files]);

  // Notice a run that waits for the runner (the worker) for long.
  const queued = run.execution?.status === "PENDING";
  useEffect(() => {
    if (!queued) return;
    const t = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(t);
  }, [queued]);

  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [open, onClose]);

  const start = () => {
    const { input, missing: left } = runInput(fields, answers);
    setMissing(left);
    if (left.length > 0) {
      document.getElementById(`run-q-${left[0]}`)?.focus();
      return;
    }
    void run.run(input);
  };

  const showStep = (nodeId: string | undefined) => {
    if (!nodeId) return;
    onClose();
    focus({ key: nodeId, severity: "error", code: "", nodeId, title: "", message: "" });
  };

  const e = run.execution;
  const failedNode = run.nodes.find((n) => n.status === "FAILED");
  const failure = e?.status === "FAILED" || e?.status === "CANCELLED"
    ? { code: failedNode?.error?.code ?? e.error?.code, message: failedNode?.error?.message ?? e.error?.message, nodeId: failedNode?.node_id ?? e.error?.node_id }
    : null;
  const friendly = failure ? friendlyError(failure.code, failure.message) : null;
  const nameOf = (id: string | undefined) => (id ? definition.nodes.find((n) => n.id === id)?.name ?? id : "");
  const outputs = e?.status === "COMPLETED" && e.output ? Object.entries(e.output) : [];
  const seconds = e?.started_at && e.completed_at ? Math.max(0, Math.round((Date.parse(e.completed_at) - Date.parse(e.started_at)) / 100) / 10) : null;

  if (!open) return null;
  return (
    <div className="run-overlay" role="dialog" aria-modal="true" aria-labelledby="run-title" data-testid="run-dialog"
      onMouseDown={(ev) => ev.target === ev.currentTarget && onClose()}>
      <div className="run-card">
        <div className="run-head">
          <h2 id="run-title">Run “{workflow?.name}”</h2>
          <span className="spacer" />
          <button type="button" className="icon-button" aria-label="Close" onClick={onClose}>✕</button>
        </div>
        <div className="run-body">
          <section className="run-questions">
            <h3>Inputs</h3>
            {fields.length === 0 ? (
              <p className="muted small">This workflow does not need any answers: just press Run.</p>
            ) : (
              fields.map((f) => (
                <Question key={f.name} field={f} value={answers[f.name] ?? ""} files={files}
                  onChange={(v) => setAnswer(f.name, v)} />
              ))
            )}
            {missing.length > 0 && <div className="field-error" role="alert">Fill in {missing.map(questionLabel).join(", ")}.</div>}
            <button type="button" className="primary run-button" disabled={run.busy} onClick={start} data-testid="run-start">
              {run.phase === "preparing" ? "Getting ready…" : run.busy ? "Running…" : e ? "▶ Run again" : "▶ Run"}
            </button>
          </section>
          <section className="run-result" data-testid="run-result">
            <h3>Result</h3>
            {run.blocked === "not_allowed" && (
              <div className="run-problem">Only workspace owners and admins can run changes. Ask one of them to run it, or open the saved version.</div>
            )}
            {run.blocked === "problems" && (
              <div className="run-problem" data-testid="run-problems">
                <b>Some steps need attention before this can run:</b>
                <ul>
                  {issues.slice(0, 8).map((i) => (
                    <li key={i.key}>
                      <span><b>{i.title}:</b> {i.message}</span>
                      {(i.nodeId || i.edgeId) && (
                        <button type="button" className="small-button" onClick={() => {
                          onClose();
                          focus(i);
                        }}>Show me</button>
                      )}
                    </li>
                  ))}
                </ul>
              </div>
            )}
            {run.error != null && <div className="run-problem">The run could not start: {run.error instanceof Error ? run.error.message : "something went wrong"}.</div>}
            {!e && !run.busy && !run.blocked && run.error == null && <p className="muted">Answer the questions and press Run. The result appears here.</p>}
            {e && !failure && e.status !== "COMPLETED" && (
              <div className="run-status" data-testid="run-status">
                <span className="spinner" aria-hidden /> {queued ? "Waiting to start…" : "Working…"}
                {queued && e && now - Date.parse(e.created_at) > QUEUED_HINT_MS && (
                  <div className="hint">Still waiting: make sure the worker is running (go run ./cmd/worker).</div>
                )}
              </div>
            )}
            {e?.status === "COMPLETED" && (
              <div data-testid="run-success">
                <div className="run-ok">✓ Done{seconds !== null ? ` in ${seconds} s` : ""}</div>
                {outputs.length === 0 && <p className="muted">It finished; there was nothing to show.</p>}
                {outputs.map(([id, ports]) => {
                  const p = ports as Record<string, unknown>;
                  // One output (an Output step's result) shows as itself.
                  const keys = p && typeof p === "object" ? Object.keys(p) : [];
                  const value = keys.length === 1 ? p[keys[0]] : p;
                  return (
                    <div key={id} className="run-output">
                      {outputs.length > 1 && <div className="run-output-name">{nameOf(id)}</div>}
                      <FriendlyValue value={value} />
                    </div>
                  );
                })}
              </div>
            )}
            {friendly && failure && (
              <div className="run-problem" data-testid="run-failure">
                <b>{friendly.title}{failure.nodeId ? ` (${nameOf(failure.nodeId)})` : ""}</b>
                {friendly.detail && <div className="small">{friendly.detail}</div>}
                <div className="small">{friendly.advice}</div>
                <div className="row" style={{ marginTop: 8 }}>
                  {failure.nodeId && <button type="button" onClick={() => showStep(failure.nodeId)}>Show me</button>}
                  {friendly.action === "connect_account" && <Link className="button" href="/settings/connected-accounts">Connected accounts</Link>}
                  {friendly.action === "add_key" && <Link className="button" href="/settings/credentials">Keys</Link>}
                  {friendly.action === "try_again" && <button type="button" onClick={start}>Try again</button>}
                </div>
                {details && (failure.code || failure.message) && (
                  <div className="small muted" style={{ marginTop: 6 }}><code>{[failure.code, failure.message].filter(Boolean).join(": ")}</code></div>
                )}
              </div>
            )}
            {e && (e.status === "COMPLETED" || failure) && (
              <div className="small muted run-more">
                <Link href={`/workflows/${workflowId}/executions`}>All runs</Link>
                {details && <> · <Link href={`/executions/${e.id}`}>Step-by-step details</Link></>}
              </div>
            )}
          </section>
        </div>
        {catalog.size === 0 && <p className="muted small">Loading…</p>}
      </div>
    </div>
  );
}
