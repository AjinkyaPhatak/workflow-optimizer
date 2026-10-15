"use client";

// Phase I4: run a step once for each item of a list ("summarize each
// email"). The list arrives on one of the step's inputs, or a {{name}}.

import { portLabel } from "@/components/nodes/WorkflowNode";
import { variableInputName, variableInputs } from "@/lib/workflow/variableInputs";
import { useEditorStore } from "@/stores/workflow-editor/store";
import type { NodeDefinition, WorkflowNode } from "@/types/api";

const ONCE = "";

/** The inputs a step can repeat over: its own inputs and its {{name}}s. */
export function repeatChoices(node: WorkflowNode, def: NodeDefinition | undefined, definition: Parameters<typeof variableInputs>[1]): { value: string; label: string }[] {
  return [
    ...variableInputs(node, definition).map((n) => ({ value: `var:${n}`, label: `{{${n}}}` })),
    ...(def?.inputs ?? []).map((p) => ({ value: p.name, label: portLabel(p.name) })),
  ];
}

export function repeatLabel(over: string): string {
  const name = variableInputName(over);
  return name !== null ? `{{${name}}}` : portLabel(over);
}

export function RepeatSettings({ node, def, readonly }: { node: WorkflowNode; def: NodeDefinition | undefined; readonly: boolean }) {
  const setSettings = useEditorStore((s) => s.setNodeSettings);
  const definition = useEditorStore((s) => s.definition);
  const over = node.settings?.repeat_over ?? ONCE;
  const choices = repeatChoices(node, def, definition);
  if (choices.length === 0 && !over) return null;
  return (
    <div className="section" data-testid="repeat-settings">
      <h3 className="section-title">Repeat</h3>
      <div className="field">
        <label htmlFor="node-repeat">Run this step</label>
        <select id="node-repeat" value={over} disabled={readonly}
          onChange={(e) => setSettings(node.id, { repeat_over: e.target.value || undefined })}>
          <option value={ONCE}>Once</option>
          {choices.map((c) => <option key={c.value} value={c.value}>Once for each item in {c.label}</option>)}
          {over && !choices.some((c) => c.value === over) && <option value={over}>Once for each item in {repeatLabel(over)} (not available)</option>}
        </select>
        <div className="field-help">
          {over
            ? `Connect a list (for example the emails Gmail found) to ${repeatLabel(over)}. The step runs for each item, up to 100, and each result becomes a list.`
            : "For a list (emails, rows, files), choose “Once for each item” to handle them one by one."}
        </div>
      </div>
    </div>
  );
}
