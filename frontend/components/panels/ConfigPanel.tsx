"use client";

import { useState, type KeyboardEvent } from "react";
import { useEditorContext } from "@/features/workflows/EditorContext";
import { variableSuggestions } from "@/lib/workflow/variables";
import { useEditorStore } from "@/stores/workflow-editor/store";
import type { ConfigField, Credential, WorkflowNode } from "@/types/api";

/** A text input that commits on blur or Enter (one undo step per edit, not
 * per keystroke). It restarts from the stored value whenever that changes
 * (undo, redo, another node). */
function CommitInput(props: { id: string; value: string; onCommit: (v: string) => void; placeholder?: string; type?: string; list?: string; disabled?: boolean }) {
  return <CommitInputDraft key={`${props.id}:${props.value}`} {...props} />;
}

function CommitInputDraft({
  id, value, onCommit, placeholder, type = "text", list, disabled,
}: { id: string; value: string; onCommit: (v: string) => void; placeholder?: string; type?: string; list?: string; disabled?: boolean }) {
  const [draft, setDraft] = useState(value);
  const commit = () => {
    if (draft !== value) onCommit(draft);
  };
  return (
    <input
      id={id} type={type} value={draft} placeholder={placeholder} list={list} disabled={disabled}
      onChange={(e) => setDraft(e.target.value)}
      onBlur={commit}
      onKeyDown={(e: KeyboardEvent<HTMLInputElement>) => {
        if (e.key === "Enter") commit();
      }}
    />
  );
}

function JsonField(props: { id: string; value: unknown; onCommit: (v: unknown) => void; disabled?: boolean }) {
  const text = props.value === undefined ? "" : JSON.stringify(props.value, null, 2);
  return <JsonFieldDraft key={`${props.id}:${text}`} text={text} {...props} />;
}

function JsonFieldDraft({ id, text, onCommit, disabled }: { id: string; text: string; onCommit: (v: unknown) => void; disabled?: boolean }) {
  const [draft, setDraft] = useState(text);
  const [error, setError] = useState<string | null>(null);
  return (
    <>
      <textarea
        id={id} value={draft} disabled={disabled}
        onChange={(e) => setDraft(e.target.value)}
        onBlur={() => {
          if (draft.trim() === "") {
            setError(null);
            onCommit(undefined);
            return;
          }
          try {
            onCommit(JSON.parse(draft));
            setError(null);
          } catch {
            setError("Not valid JSON");
          }
        }}
      />
      {error && <div className="field-error">{error}</div>}
    </>
  );
}

function CredentialSelect({ id, value, provider, credentials, onCommit, disabled }: {
  id: string; value: string; provider: string | undefined; credentials: Credential[]; onCommit: (v: string | undefined) => void; disabled?: boolean;
}) {
  // Metadata only: id, name, provider, type. No secret ever reaches the browser.
  const options = credentials.filter((c) => !provider || c.provider === provider);
  const known = options.some((c) => c.id === value);
  return (
    <select id={id} value={value} disabled={disabled} onChange={(e) => onCommit(e.target.value || undefined)}>
      <option value="">— No credential —</option>
      {options.map((c) => (
        <option key={c.id} value={c.id}>{c.name} ({c.provider}, {c.credential_type})</option>
      ))}
      {value && !known && <option value={value}>Unknown credential ({value.slice(0, 8)}…)</option>}
    </select>
  );
}

function FieldEditor({ node, field }: { node: WorkflowNode; field: ConfigField }) {
  const { credentials, catalog } = useEditorContext();
  const definition = useEditorStore((s) => s.definition);
  const readonly = useEditorStore((s) => s.mode === "readonly");
  const setConfig = useEditorStore((s) => s.setConfigValue);
  const value = node.config[field.name];
  const id = `cfg-${field.name}`;
  const placeholder = field.default === null || field.default === undefined || field.default === "" ? undefined : `Default: ${JSON.stringify(field.default)}`;
  const commit = (v: unknown) => setConfig(node.id, field.name, v);

  let input;
  if (field.name === "credential_id") {
    const provider = typeof node.config.provider === "string" ? node.config.provider : undefined;
    input = <CredentialSelect id={id} value={typeof value === "string" ? value : ""} provider={provider} credentials={credentials} onCommit={commit} disabled={readonly} />;
  } else if (field.type === "number") {
    input = (
      <CommitInput id={id} type="number" disabled={readonly} placeholder={placeholder}
        value={typeof value === "number" ? String(value) : ""}
        onCommit={(v) => commit(v.trim() === "" || Number.isNaN(Number(v)) ? undefined : Number(v))} />
    );
  } else if (field.type === "boolean") {
    input = (
      <div className="checkbox-row">
        <input id={id} type="checkbox" disabled={readonly} checked={value === true} onChange={(e) => commit(e.target.checked)} />
        <span className="small muted">{value === undefined ? "Not set" : String(value)}</span>
      </div>
    );
  } else if (field.type === "string") {
    const listId = `${id}-vars`;
    input = (
      <>
        <CommitInput id={id} list={listId} disabled={readonly} placeholder={placeholder}
          value={typeof value === "string" ? value : value === undefined ? "" : JSON.stringify(value)}
          onCommit={(v) => commit(v === "" ? undefined : v)} />
        <datalist id={listId}>
          {variableSuggestions(definition, catalog, node.id).map((s) => <option key={s} value={s} />)}
        </datalist>
      </>
    );
  } else {
    input = <JsonField id={id} value={value} onCommit={commit} disabled={readonly} />;
  }

  return (
    <div className="field">
      <label htmlFor={id}>
        {field.name} {field.required && <span className="req">*</span>} <span className="muted small">{field.type}</span>
      </label>
      {input}
      {field.description && <div className="hint">{field.description}</div>}
    </div>
  );
}

export function ConfigPanel() {
  const { catalog } = useEditorContext();
  const definition = useEditorStore((s) => s.definition);
  const selectedNodeIds = useEditorStore((s) => s.selectedNodeIds);
  const selectedEdgeIds = useEditorStore((s) => s.selectedEdgeIds);
  const readonly = useEditorStore((s) => s.mode === "readonly");
  const rename = useEditorStore((s) => s.renameNode);
  const removeNodes = useEditorStore((s) => s.removeNodes);
  const removeEdges = useEditorStore((s) => s.removeEdges);

  const node = selectedNodeIds.length === 1 ? definition.nodes.find((n) => n.id === selectedNodeIds[0]) : undefined;
  const edge = !node && selectedEdgeIds.length === 1 ? definition.edges.find((e) => e.id === selectedEdgeIds[0]) : undefined;

  if (node) {
    const def = catalog.get(node.type);
    const known = new Set(def?.config.map((f) => f.name));
    const extra = Object.keys(node.config).filter((k) => !known.has(k));
    return (
      <aside className="config-panel" aria-label="Node configuration" data-testid="config-panel">
        <h2>{def?.name ?? node.type}</h2>
        <div className="muted small">{def?.description}</div>
        <div className="section">
          <div className="field">
            <label htmlFor="cfg-node-name">Name</label>
            <CommitInput id="cfg-node-name" value={node.name} disabled={readonly} onCommit={(v) => v.trim() && rename(node.id, v.trim())} />
            <div className="hint">ID: <code>{node.id}</code> · reference outputs as <code>{`{{${node.id}.<port>}}`}</code></div>
          </div>
          {def?.config.map((f) => <FieldEditor key={f.name} node={node} field={f} />)}
          {def && def.config.length === 0 && <p className="muted">This node has no configuration.</p>}
          {extra.length > 0 && (
            <div className="field-error">Unknown configuration keys (rejected by the backend): {extra.join(", ")}</div>
          )}
        </div>
        {!readonly && (
          <div className="section">
            <button className="danger" onClick={() => removeNodes([node.id])}>Delete node</button>
          </div>
        )}
      </aside>
    );
  }

  if (edge) {
    const name = (id: string) => definition.nodes.find((n) => n.id === id)?.name ?? id;
    return (
      <aside className="config-panel" aria-label="Connection">
        <h2>Connection</h2>
        <p>{name(edge.source)}.<b>{edge.source_port}</b> → {name(edge.target)}.<b>{edge.target_port}</b></p>
        {!readonly && <button className="danger" onClick={() => removeEdges([edge.id])}>Delete connection</button>}
      </aside>
    );
  }

  return (
    <aside className="config-panel" aria-label="Workflow">
      <h2>Workflow</h2>
      <p className="muted">
        {selectedNodeIds.length > 1
          ? `${selectedNodeIds.length} nodes selected. Press Delete to remove them.`
          : "Select a node to configure it. Drag from an output port to an input port to connect nodes."}
      </p>
      <div className="section small muted stack">
        <div>{definition.nodes.length} nodes · {definition.edges.length} connections</div>
        <div>Shortcuts: Ctrl+Z undo · Ctrl+Shift+Z redo · Ctrl+S save · Delete removes the selection · Shift+drag selects several</div>
      </div>
    </aside>
  );
}
