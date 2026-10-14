"use client";

// Phase E2: what happens when one node fails (stop, continue, or route the
// failure to the node's error output) and how often it is retried.

import { useEditorStore } from "@/stores/workflow-editor/store";
import type { NodeDefinition, OnErrorMode, WorkflowNode } from "@/types/api";

const MODES: { value: OnErrorMode; label: string; help: string }[] = [
  { value: "stop", label: "Stop the workflow", help: "The run fails at this node (default)." },
  { value: "continue", label: "Continue without it", help: "The failure is recorded; nodes that need this one's output are skipped, the rest runs." },
  { value: "error_output", label: "Go to the error output", help: "Adds an “error” output with code and message: connect it to handle the failure (e.g. send an alert)." },
];

function NumberSetting({ id, label, value, min, max, placeholder, disabled, onCommit }: {
  id: string; label: string; value: number | undefined; min: number; max: number; placeholder: string; disabled: boolean;
  onCommit: (v: number | undefined) => void;
}) {
  return (
    <div className="field">
      <label htmlFor={id}>{label}</label>
      <input id={id} type="number" min={min} max={max} step={1} placeholder={placeholder} disabled={disabled}
        key={`${id}:${value ?? ""}`} defaultValue={value ?? ""}
        onBlur={(e) => {
          const raw = e.target.value.trim();
          const n = Number(raw);
          onCommit(raw === "" || !Number.isFinite(n) ? undefined : Math.min(max, Math.max(min, Math.round(n))));
        }} />
    </div>
  );
}

export function FailureSettings({ node, def, readonly }: { node: WorkflowNode; def: NodeDefinition | undefined; readonly: boolean }) {
  const setSettings = useEditorStore((s) => s.setNodeSettings);
  const mode = node.settings?.on_error ?? "stop";
  const unsafe = def?.side_effects === "unsafe";
  return (
    <div className="section" data-testid="failure-settings">
      <h3 className="section-title">When this node fails</h3>
      <div className="field">
        <label htmlFor="node-on-error">On error</label>
        <select id="node-on-error" value={mode} disabled={readonly}
          onChange={(e) => setSettings(node.id, { on_error: e.target.value as OnErrorMode })}>
          {MODES.map((m) => <option key={m.value} value={m.value}>{m.label}</option>)}
        </select>
        <div className="field-help">{MODES.find((m) => m.value === mode)?.help}</div>
      </div>
      <div className="row" style={{ gap: 8 }}>
        <NumberSetting id="node-retry-attempts" label="Attempts" min={1} max={5} placeholder="Default" disabled={readonly}
          value={node.settings?.retry_attempts} onCommit={(v) => setSettings(node.id, { retry_attempts: v })} />
        <NumberSetting id="node-retry-delay" label="Pause between (ms)" min={0} max={30000} placeholder="Default" disabled={readonly}
          value={node.settings?.retry_delay_ms} onCommit={(v) => setSettings(node.id, { retry_delay_ms: v })} />
      </div>
      <div className="field-help">
        Retries happen only after temporary failures (rate limits, timeouts, server errors)
        {unsafe ? ", and never when this node may already have made its change (it is not safe to repeat)" : ""}.
      </div>
    </div>
  );
}
