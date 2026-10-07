"use client";

import Link from "next/link";
import { useState } from "react";
import { ErrorBanner } from "@/components/ui/ErrorBanner";
import { useExecution } from "@/features/executions/useExecution";
import { useEditorContext } from "@/features/workflows/EditorContext";
import { useFocusIssue, useIssues } from "@/features/workflows/useIssues";
import type { Issue } from "@/lib/workflow/issues";
import { useEditorStore, useIsDirty } from "@/stores/workflow-editor/store";
import type { WorkflowVariable } from "@/types/api";

export type BottomTab = "validation" | "run";

/** Findings grouped by where they are (a node, a connection, the workflow),
 * in the order the backend reported them. */
function groupIssues(issues: Issue[]): { key: string; title: string; items: Issue[] }[] {
  const groups = new Map<string, { key: string; title: string; items: Issue[] }>();
  for (const i of issues) {
    const key = i.edgeId ? `edge:${i.edgeId}` : i.nodeId ? `node:${i.nodeId}` : i.variable !== undefined ? "variables" : "workflow";
    const g = groups.get(key) ?? { key, title: i.title, items: [] };
    g.items.push(i);
    groups.set(key, g);
  }
  return [...groups.values()];
}

function ValidationTab() {
  const { validate, busy } = useEditorContext();
  const validation = useEditorStore((s) => s.validation);
  const dirty = useIsDirty();
  const issues = useIssues();
  const focus = useFocusIssue();

  if (!validation) {
    return (
      <div className="panel-empty">
        <p className="muted">Not validated yet. Validation checks the saved workflow with the backend and lists anything that must be fixed before publishing.</p>
        <button onClick={() => void validate()} disabled={busy !== null}>{busy === "validating" ? "Validating…" : "Validate now"}</button>
      </div>
    );
  }
  const errors = issues.filter((i) => i.severity === "error");
  const stale = dirty && <p className="small muted stale-note">The workflow has changed since this check. Validate again to refresh.</p>;
  if (validation.valid && issues.length === 0) {
    return (
      <>
        <p className="validation-ok" data-testid="validation-ok">✓ The workflow is valid and can be published.</p>
        {stale}
      </>
    );
  }

  return (
    <div>
      <div className="validation-summary" data-testid="validation-summary">
        {errors.length > 0 ? (
          <b>{errors.length} problem{errors.length === 1 ? "" : "s"} found</b>
        ) : (
          <b className="ok-text">✓ Valid</b>
        )}
        {issues.length > errors.length && <span className="muted"> · {issues.length - errors.length} warning{issues.length - errors.length === 1 ? "" : "s"}</span>}
        <span className="muted small"> · Click a problem to show it on the canvas.</span>
      </div>
      {stale}
      <ol className="validation-list" data-testid="validation-errors">
        {groupIssues(issues).map(({ key, title, items }) => (
          <li key={key} className={items.some((i) => i.severity === "error") ? "error" : "warning"}>
            <div className="issue-title">{title}</div>
            {items.map((i) => (
              <button key={i.key} type="button" className={i.severity} onClick={() => focus(i)} disabled={!i.nodeId && !i.edgeId && i.variable === undefined}>
                <span className="issue-icon" aria-hidden>⚠</span>
                <span className="issue-body">
                  <span>{i.message}</span>
                  {i.hint && <span className="issue-hint">{i.hint}</span>}
                </span>
                <span className="code">{i.code}</span>
              </button>
            ))}
          </li>
        ))}
      </ol>
    </div>
  );
}

type RunRow = { key: string; value: string; variable?: WorkflowVariable };

/** A run-input row's value: declared variables are converted to their type
 * (the backend checks it); an empty variable row is left out so its default
 * applies. Other rows are text. */
function rowValue(r: RunRow): { set: boolean; value?: unknown } {
  const v = r.variable;
  if (!v) return { set: true, value: r.value };
  if (r.value === "") return { set: false };
  switch (v.type) {
    case "string": return { set: true, value: r.value };
    case "number": return { set: true, value: Number.isNaN(Number(r.value)) ? r.value : Number(r.value) };
    case "boolean": return { set: true, value: r.value === "true" ? true : r.value === "false" ? false : r.value };
    default:
      try {
        return { set: true, value: JSON.parse(r.value) };
      } catch {
        return { set: true, value: r.value };
      }
  }
}

function initialRows(variables: WorkflowVariable[]): RunRow[] {
  return [
    { key: "query", value: "" },
    ...variables.filter((v) => v.name !== "query").map((v) => ({ key: v.name, value: "", variable: v })),
  ];
}

function RunTab({ workflowId }: { workflowId: string }) {
  const { workflow, version } = useEditorContext();
  const { execution, nodes, running, error, run } = useExecution(workflowId);
  const [rows, setRows] = useState<RunRow[]>(() => initialRows(useEditorStore.getState().definition.variables ?? []));
  const activeVersion = workflow?.active_version_id ?? null;

  const input = Object.fromEntries(
    rows.filter((r) => r.key.trim() && rowValue(r).set).map((r) => [r.key.trim(), rowValue(r).value]),
  );

  return (
    <div className="run-grid">
      <div>
        <h3 className="small" style={{ marginBottom: 8 }}>Test input</h3>
        {rows.map((r, i) => (
          <div className="kv-row" key={i}>
            <input aria-label={`Input key ${i + 1}`} value={r.key} onChange={(e) => setRows(rows.map((x, j) => (j === i ? { ...x, key: e.target.value } : x)))} />
            <input aria-label={`Input ${r.key || i + 1}`} value={r.value}
              placeholder={r.variable ? (r.variable.default === null ? `${r.variable.type}, required` : `default: ${typeof r.variable.default === "string" ? r.variable.default : JSON.stringify(r.variable.default)}`) : undefined}
              onChange={(e) => setRows(rows.map((x, j) => (j === i ? { ...x, value: e.target.value } : x)))} />
            <button onClick={() => setRows(rows.filter((_, j) => j !== i))} aria-label="Remove input">✕</button>
          </div>
        ))}
        <div className="row">
          <button onClick={() => setRows([...rows, { key: "", value: "" }])}>+ Field</button>
          <button onClick={() => setRows(initialRows(useEditorStore.getState().definition.variables ?? []))} title="One field per workflow variable">
            Reset fields
          </button>
          <span className="spacer" />
          <button className="primary" disabled={!activeVersion || running} onClick={() => activeVersion && run(input, activeVersion)}>
            {running ? "Running…" : "Execute"}
          </button>
        </div>
        <p className="small muted">
          {activeVersion
            ? `Runs the published version${version?.id === activeVersion ? ` ${version.version_number}` : ""}. Unsaved or unpublished changes are not included.`
            : "Publish a version first: only published versions can be executed."}
        </p>
      </div>
      <div data-testid="execution-result">
        <ErrorBanner error={error} />
        {execution ? (
          <>
            <div className="row">
              <b>Execution</b>
              <code className="small">{execution.id.slice(0, 8)}</code>
              <Link href={`/executions/${execution.id}`} data-testid="open-debugger">Open in debugger</Link>
              <span className={`badge ${execution.status === "COMPLETED" ? "published" : execution.status === "FAILED" ? "failed" : ""}`} data-testid="execution-status">
                {execution.status}
              </span>
            </div>
            {execution.error && <div className="error-banner" style={{ marginTop: 6 }}>{execution.error.code}: {execution.error.message}</div>}
            {execution.output && <pre className="json" data-testid="execution-output">{JSON.stringify(execution.output, null, 2)}</pre>}
            {nodes.length > 0 && (
              <div className="small muted" style={{ marginTop: 6 }}>
                {nodes.map((n) => `${n.node_id}: ${n.status}${n.duration_ms != null ? ` (${n.duration_ms} ms)` : ""}`).join(" · ")}
              </div>
            )}
          </>
        ) : (
          !running && <p className="muted">No execution yet.</p>
        )}
      </div>
    </div>
  );
}

export function BottomPanel({ tab, onTab, workflowId }: { tab: BottomTab; onTab: (t: BottomTab) => void; workflowId: string }) {
  const errors = useEditorStore((s) => s.validation?.errors.length ?? 0);
  const checked = useEditorStore((s) => s.validation !== null);
  return (
    <section className="bottom-panel">
      <div className="bottom-tabs" role="tablist">
        <button role="tab" className={tab === "validation" ? "active" : ""} onClick={() => onTab("validation")}>
          Validation{errors ? <span className="tab-count error">{errors}</span> : checked ? <span className="tab-count ok">✓</span> : null}
        </button>
        <button role="tab" className={tab === "run" ? "active" : ""} onClick={() => onTab("run")}>Run</button>
      </div>
      {/* Both stay mounted so a running execution survives switching tabs. */}
      <div className="bottom-content" hidden={tab !== "validation"}><ValidationTab /></div>
      <div className="bottom-content" hidden={tab !== "run"}><RunTab workflowId={workflowId} /></div>
    </section>
  );
}
