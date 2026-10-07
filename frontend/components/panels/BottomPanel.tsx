"use client";

import { useReactFlow } from "@xyflow/react";
import { useState } from "react";
import { ErrorBanner } from "@/components/ui/ErrorBanner";
import { useExecution } from "@/features/executions/useExecution";
import { useEditorContext } from "@/features/workflows/EditorContext";
import { useEditorStore } from "@/stores/workflow-editor/store";

export type BottomTab = "validation" | "run";

function ValidationTab() {
  const flow = useReactFlow();
  const validation = useEditorStore((s) => s.validation);
  const nodes = useEditorStore((s) => s.definition.nodes);
  const select = useEditorStore((s) => s.select);

  if (!validation) return <p className="muted">Not validated yet. Validate checks the saved version with the backend graph validator.</p>;
  if (validation.valid) return <p style={{ color: "var(--ok)" }} data-testid="validation-ok">✓ The workflow is valid and can be published.</p>;

  return (
    <ol className="validation-list" data-testid="validation-errors">
      {validation.errors.map((e, i) => {
        const node = e.node_id ? nodes.find((n) => n.id === e.node_id) : undefined;
        return (
          <li
            key={i}
            onClick={() => {
              if (!node) return;
              select([node.id]);
              void flow.fitView({ nodes: [{ id: node.id }], duration: 300, maxZoom: 1.2 });
            }}
          >
            <b>{node?.name ?? (e.node_id || "Workflow")}</b>{e.port ? ` · ${e.port}` : ""} — {e.message} <span className="code">{e.code}</span>
          </li>
        );
      })}
    </ol>
  );
}

function RunTab({ workflowId }: { workflowId: string }) {
  const { workflow, version } = useEditorContext();
  const { execution, nodes, running, error, run } = useExecution(workflowId);
  const [rows, setRows] = useState<{ key: string; value: string }[]>([{ key: "query", value: "" }]);
  const activeVersion = workflow?.active_version_id ?? null;

  const input = Object.fromEntries(rows.filter((r) => r.key.trim()).map((r) => [r.key.trim(), r.value]));

  return (
    <div className="run-grid">
      <div>
        <h3 className="small" style={{ marginBottom: 8 }}>Test input</h3>
        {rows.map((r, i) => (
          <div className="kv-row" key={i}>
            <input aria-label={`Input key ${i + 1}`} value={r.key} onChange={(e) => setRows(rows.map((x, j) => (j === i ? { ...x, key: e.target.value } : x)))} />
            <input aria-label={`Input ${r.key || i + 1}`} value={r.value} onChange={(e) => setRows(rows.map((x, j) => (j === i ? { ...x, value: e.target.value } : x)))} />
            <button onClick={() => setRows(rows.filter((_, j) => j !== i))} aria-label="Remove input">✕</button>
          </div>
        ))}
        <div className="row">
          <button onClick={() => setRows([...rows, { key: "", value: "" }])}>+ Field</button>
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
  return (
    <section className="bottom-panel">
      <div className="bottom-tabs" role="tablist">
        <button role="tab" className={tab === "validation" ? "active" : ""} onClick={() => onTab("validation")}>
          Validation{errors ? ` (${errors})` : ""}
        </button>
        <button role="tab" className={tab === "run" ? "active" : ""} onClick={() => onTab("run")}>Run</button>
      </div>
      {/* Both stay mounted so a running execution survives switching tabs. */}
      <div className="bottom-content" hidden={tab !== "validation"}><ValidationTab /></div>
      <div className="bottom-content" hidden={tab !== "run"}><RunTab workflowId={workflowId} /></div>
    </section>
  );
}
