"use client";

import { FailureSettings } from "@/components/panels/FailureSettings";
import { RepeatSettings } from "@/components/panels/RepeatSettings";
import { NodeTest } from "@/components/panels/NodeTest";
import { useTestRunStore } from "@/stores/test-run/store";
import { DynamicSelect, KeyValueField } from "@/components/panels/SettingFields";
import { useFieldOptions } from "@/features/nodes/useFieldOptions";
import Link from "next/link";
import { useMemo, useState, type KeyboardEvent } from "react";
import { useEditorContext } from "@/features/workflows/EditorContext";
import { useFocusIssue, useIssues } from "@/features/workflows/useIssues";
import { categoryClass } from "@/features/nodes/catalog";
import type { Issue } from "@/lib/workflow/issues";
import { effectiveConfig, fieldControl, friendlyNodeName, labelOf, optionsFor, providerName, typeLabel } from "@/lib/workflow/labels";
import { NodeIcon } from "@/components/ui/NodeIcon";
import { useShowDetails, useTechnicalDetails } from "@/stores/ui/details";
import { availableVariables } from "@/lib/workflow/variables";
import { useEditorStore } from "@/stores/workflow-editor/store";
import { accountLabel, statusLabel } from "@/lib/connectedAccounts";
import { ACCOUNT_NAMES } from "@/lib/workflow/requirements";
import type { ConfigField, ConfigOption, ConnectedAccount, Credential, WorkflowNode } from "@/types/api";
import { VariableField } from "./VariableField";
import { VariablesEditor } from "./VariablesEditor";

const DEFAULT_CHOICE = "__default__";
const CUSTOM_CHOICE = "__custom__";
const CURRENT_CHOICE = "__current__";

/** An option's <option> value: strings as themselves, others as JSON. */
const optionKey = (v: unknown) => (typeof v === "string" ? v : JSON.stringify(v));

/** A text input that commits on blur or Enter (one undo step per edit, not
 * per keystroke). It restarts from the stored value whenever that changes
 * (undo, redo, another node). */
function CommitInput(props: CommitInputProps) {
  return <CommitInputDraft key={`${props.id}:${props.value}`} {...props} />;
}

interface CommitInputProps {
  id: string;
  value: string;
  onCommit: (v: string) => void;
  placeholder?: string;
  type?: string;
  list?: string;
  disabled?: boolean;
  invalid?: boolean;
  describedBy?: string;
  step?: string;
  min?: number;
  max?: number;
}

function CommitInputDraft({ id, value, onCommit, placeholder, type = "text", list, disabled, invalid, describedBy, step, min, max }: CommitInputProps) {
  const [draft, setDraft] = useState(value);
  const commit = () => {
    if (draft !== value) onCommit(draft);
  };
  return (
    <input
      id={id} type={type} value={draft} placeholder={placeholder} list={list} disabled={disabled} step={step} min={min} max={max}
      autoComplete="off"
      aria-invalid={invalid || undefined}
      aria-describedby={describedBy}
      onChange={(e) => setDraft(e.target.value)}
      onBlur={commit}
      onKeyDown={(e: KeyboardEvent<HTMLInputElement>) => {
        if (e.key === "Enter") commit();
      }}
    />
  );
}

function JsonField(props: { id: string; value: unknown; onCommit: (v: unknown) => void; disabled?: boolean; invalid?: boolean; describedBy?: string; placeholder?: string }) {
  const text = props.value === undefined ? "" : JSON.stringify(props.value, null, 2);
  return <JsonFieldDraft key={`${props.id}:${text}`} text={text} {...props} />;
}

function JsonFieldDraft({ id, text, onCommit, disabled, invalid, describedBy, placeholder }: {
  id: string; text: string; onCommit: (v: unknown) => void; disabled?: boolean; invalid?: boolean; describedBy?: string; placeholder?: string;
}) {
  const [draft, setDraft] = useState(text);
  const [error, setError] = useState<string | null>(null);
  const apply = (formatted: boolean) => {
    if (draft.trim() === "") {
      setError(null);
      onCommit(undefined);
      return;
    }
    try {
      const v = JSON.parse(draft);
      setError(null);
      if (formatted) setDraft(JSON.stringify(v, null, 2));
      onCommit(v);
    } catch {
      setError("This is not valid JSON.");
    }
  };
  return (
    <>
      <textarea
        id={id} value={draft} disabled={disabled} spellCheck={false} className="code" placeholder={placeholder}
        aria-invalid={invalid || error !== null || undefined}
        aria-describedby={describedBy}
        onChange={(e) => setDraft(e.target.value)}
        onBlur={() => apply(false)}
      />
      {!disabled && (
        <div className="field-actions">
          <button type="button" className="small-button" onMouseDown={(e) => e.preventDefault()} onClick={() => apply(true)}>Format</button>
        </div>
      )}
      {error && <div className="field-error" role="alert">{error}</div>}
    </>
  );
}

function CredentialSelect({ id, value, provider, credentials, onCommit, disabled, invalid, describedBy }: {
  id: string; value: string; provider: string | undefined; credentials: Credential[]; onCommit: (v: string | undefined) => void;
  disabled?: boolean; invalid?: boolean; describedBy?: string;
}) {
  // Metadata only: id, name, provider, type. No secret ever reaches the browser.
  const options = credentials.filter((c) => !provider || c.provider === provider);
  const known = options.some((c) => c.id === value);
  return (
    <>
      <select
        id={id} value={value} disabled={disabled} aria-invalid={invalid || undefined} aria-describedby={describedBy}
        onChange={(e) => onCommit(e.target.value || undefined)}
      >
        <option value="">{options.length ? "Select a credential…" : "No credential"}</option>
        {options.map((c) => (
          <option key={c.id} value={c.id}>{c.name} ({providerName(c.provider)})</option>
        ))}
        {value && !known && <option value={value}>Unknown credential ({value.slice(0, 8)}…)</option>}
      </select>
      <div className="hint">
        {options.length === 0 ? `No ${provider ? `${providerName(provider)} ` : ""}key added yet${provider === "gemini" ? " (it is free)" : ""}. ` : ""}
        <Link href="/settings/credentials">{options.length === 0 ? "Add a key" : "Manage keys"}</Link>
      </div>
    </>
  );
}


/** The account picker of nodes that use connected accounts (OAuth). The
 * node stores the account's credential_id; the account itself (and its
 * tokens) is managed on the Connected accounts page. */
function AccountSelect({ id, value, provider, accounts, onCommit, disabled, invalid, describedBy }: {
  id: string; value: string; provider: string; accounts: ConnectedAccount[]; onCommit: (v: string | undefined) => void;
  disabled?: boolean; invalid?: boolean; describedBy?: string;
}) {
  const options = accounts.filter((a) => a.provider === provider);
  const selected = options.find((a) => a.credential_id === value);
  return (
    <>
      <select
        id={id} value={value} disabled={disabled} aria-invalid={invalid || undefined} aria-describedby={describedBy}
        onChange={(e) => onCommit(e.target.value || undefined)}
      >
        <option value="">{options.length ? "Select an account…" : "No account connected"}</option>
        {options.map((a) => (
          <option key={a.id} value={a.credential_id}>
            {accountLabel(a)}{a.status === "ACTIVE" ? "" : ` (${statusLabel(a.status).toLowerCase()})`}
          </option>
        ))}
        {value && !selected && <option value={value}>Unknown account ({value.slice(0, 8)}…)</option>}
      </select>
      {selected && selected.status !== "ACTIVE" && (
        <div className="field-error" role="alert">This account is {statusLabel(selected.status).toLowerCase()}. Reconnect it to run this node.</div>
      )}
      {options.length === 0 ? (
        <Link className="button primary connect-button" href="/settings/connected-accounts" target="_blank" data-testid="connect-account">
          Connect {ACCOUNT_NAMES[provider] ?? provider}
        </Link>
      ) : (
        <div className="hint"><Link href="/settings/connected-accounts">Manage connected accounts</Link></div>
      )}
    </>
  );
}

/** A dropdown of the backend's options for the field (only those whose
 * conditions hold for this node). With allow_custom, "Custom..." switches to
 * a text input for any other value. */
function OptionSelect({ id, field, value, options, onCommit, disabled, invalid, describedBy }: {
  id: string; field: ConfigField; value: unknown; options: ConfigOption[]; onCommit: (v: unknown) => void;
  disabled?: boolean; invalid?: boolean; describedBy?: string;
}) {
  const known = options.some((o) => optionKey(o.value) === optionKey(value));
  const [custom, setCustom] = useState(value !== undefined && !known && !!field.allow_custom);
  const hasDefault = field.default !== null && field.default !== undefined && field.default !== "";
  const defaultLabel = hasDefault ? options.find((o) => optionKey(o.value) === optionKey(field.default))?.label ?? String(field.default) : "";

  if (custom) {
    return (
      <>
        <CommitInput id={id} disabled={disabled} invalid={invalid} describedBy={describedBy} placeholder="Enter a value"
          value={value === undefined ? "" : String(value)} onCommit={(v) => onCommit(v.trim() === "" ? undefined : v.trim())} />
        {!disabled && (
          <div className="field-actions">
            <button type="button" className="link" onClick={() => setCustom(false)}>Choose from the list</button>
          </div>
        )}
      </>
    );
  }
  const current = value === undefined ? DEFAULT_CHOICE : known ? optionKey(value) : CURRENT_CHOICE;
  return (
    <select id={id} value={current} disabled={disabled} aria-invalid={invalid || (value !== undefined && !known) || undefined} aria-describedby={describedBy}
      onChange={(e) => {
        const k = e.target.value;
        if (k === CUSTOM_CHOICE) setCustom(true);
        else if (k === DEFAULT_CHOICE) onCommit(undefined);
        else if (k !== CURRENT_CHOICE) onCommit(options.find((o) => optionKey(o.value) === k)?.value);
      }}>
      <option value={DEFAULT_CHOICE}>{hasDefault ? `Default (${defaultLabel})` : "Select…"}</option>
      {options.map((o) => (
        <option key={optionKey(o.value)} value={optionKey(o.value)}>{o.label}</option>
      ))}
      {value !== undefined && !known && <option value={CURRENT_CHOICE}>{String(value)} (not available)</option>}
      {field.allow_custom && <option value={CUSTOM_CHOICE}>Custom…</option>}
    </select>
  );
}

function BooleanField({ id, value, def, onCommit, disabled }: { id: string; value: unknown; def: unknown; onCommit: (v: boolean) => void; disabled?: boolean }) {
  const checked = value === undefined ? def === true : value === true;
  return (
    <label className="switch-row" htmlFor={id}>
      <input id={id} type="checkbox" role="switch" className="switch" disabled={disabled} checked={checked} onChange={(e) => onCommit(e.target.checked)} />
      <span className="small muted">{value === undefined ? `Default (${checked ? "on" : "off"})` : checked ? "On" : "Off"}</span>
    </label>
  );
}

/** One setting's editor. The settings panel shows it in full; node cards
 * (Phase H3) show it compact, with the help text as a tooltip and IDs
 * prefixed per node (several cards are on screen at once). */
export function FieldEditor({ node, field, issues, idPrefix = "cfg", compact = false }: {
  node: WorkflowNode; field: ConfigField; issues: Issue[]; idPrefix?: string; compact?: boolean;
}) {
  const { credentials, connectedAccounts, catalog, workspaceId } = useEditorContext();
  const definition = useEditorStore((s) => s.definition);
  const readonly = useEditorStore((s) => s.mode === "readonly");
  const setConfig = useEditorStore((s) => s.setConfigValue);
  const value = node.config[field.name];
  const def = catalog.get(node.type);
  const id = `${idPrefix}-${field.name}`;
  const helpId = `${id}-help`;
  const errorId = `${id}-error`;
  const control = fieldControl(field);
  const hasDefault = !(field.default === null || field.default === undefined || field.default === "");
  const placeholder = hasDefault && typeof field.default !== "object" ? `Default: ${String(field.default)}` : undefined;
  const commit = (v: unknown) => setConfig(node.id, field.name, v);
  const invalid = issues.length > 0;
  const describedBy = [!compact && (field.description || control === "credential") ? helpId : "", invalid ? errorId : ""].filter(Boolean).join(" ") || undefined;
  const testNodes = useTestRunStore((s) => s.result?.nodes);
  const details = useShowDetails();
  const groups = useMemo(() => availableVariables(definition, catalog, node.id,
    new Map((testNodes ?? []).map((n) => [n.node_id, n.output]))), [definition, catalog, node.id, testNodes]);
  // Phase C8: choices (or key names) listed through the node's account.
  const choices = useFieldOptions(node.type, def, field.options_source ?? field.keys_source, field.depends_on ?? [], node.config, workspaceId);

  let input;
  if (control === "credential") {
    // Integration actions declare their credential provider; the LLM node
    // picks one with its provider setting.
    const provider = def?.auth?.provider ?? (typeof node.config.provider === "string" ? node.config.provider : undefined);
    input = def?.auth?.credential_type === "OAUTH2" ? (
      <AccountSelect id={id} value={typeof value === "string" ? value : ""} provider={def.auth.provider} accounts={connectedAccounts ?? []}
        onCommit={commit} disabled={readonly} invalid={invalid} describedBy={describedBy} />
    ) : (
      <CredentialSelect id={id} value={typeof value === "string" ? value : ""} provider={provider} credentials={credentials}
        onCommit={commit} disabled={readonly} invalid={invalid} describedBy={describedBy} />
    );
  } else if (control === "dynamic") {
    input = (
      <DynamicSelect key={id} id={id} value={value} choices={choices} onCommit={commit} disabled={readonly} invalid={invalid}
        describedBy={describedBy} placeholder={placeholder} />
    );
  } else if (control === "key_value") {
    input = (
      <KeyValueField id={id} value={value} onCommit={commit} groups={groups} disabled={readonly} describedBy={describedBy}
        suggestions={field.keys_source ? choices : null} />
    );
  } else if (control === "select") {
    input = (
      <OptionSelect id={id} field={field} value={value} options={optionsFor(field, effectiveConfig(def, node.config))}
        onCommit={commit} disabled={readonly} invalid={invalid} describedBy={describedBy} />
    );
  } else if (control === "number") {
    input = (
      <CommitInput id={id} type="number" step={field.step !== undefined ? String(field.step) : "any"} min={field.min} max={field.max}
        disabled={readonly} placeholder={placeholder} invalid={invalid} describedBy={describedBy}
        value={typeof value === "number" ? String(value) : ""}
        onCommit={(v) => commit(v.trim() === "" || Number.isNaN(Number(v)) ? undefined : Number(v))} />
    );
  } else if (control === "boolean") {
    input = <BooleanField id={id} value={value} def={field.default} onCommit={commit} disabled={readonly} />;
  } else if (control === "json") {
    input = <JsonField id={id} value={value} onCommit={commit} disabled={readonly} invalid={invalid} describedBy={describedBy}
      placeholder={hasDefault ? JSON.stringify(field.default) : undefined} />;
  } else {
    input = (
      <VariableField id={id} groups={groups} multiline={control === "multiline"} disabled={readonly} placeholder={placeholder}
        invalid={invalid} describedBy={describedBy}
        value={typeof value === "string" ? value : value === undefined ? "" : JSON.stringify(value)}
        onCommit={(v) => commit(v === "" ? undefined : v)} />
    );
  }

  const help = control === "credential" ? "Which credential should this node use?" : field.description;
  return (
    <div className={`field ${invalid ? "has-error" : ""} ${compact ? "compact" : ""}`} data-field={field.name}>
      <label htmlFor={id} title={compact ? help : undefined}>
        {labelOf(field)}
        {/* Same rule as the backend: a required field with a declared default
            may be left empty. */}
        {field.required && (field.default === null || field.default === undefined) && <span className="req" title="Required"> *</span>}
        {control === "json" && details && <span className="field-type">{typeLabel(field.type)}</span>}
      </label>
      {help && !compact && <div className="field-help" id={helpId}>{help}</div>}
      {input}
      {invalid && (
        <div className="field-error" id={errorId} data-testid={`field-error-${field.name}`}>
          {issues.map((i) => <div key={i.key}>{i.message}</div>)}
        </div>
      )}
    </div>
  );
}

function IssueList({ issues }: { issues: Issue[] }) {
  const focus = useFocusIssue();
  if (issues.length === 0) return null;
  return (
    <ul className="panel-issues" data-testid="panel-issues">
      {issues.map((i) => (
        <li key={i.key} className={i.severity}>
          <button type="button" className="link-button" onClick={() => focus(i)}>
            <span className="issue-icon" aria-hidden>⚠</span>
            <span>
              {i.message}
              {i.hint && <span className="issue-hint"> {i.hint}</span>}
            </span>
          </button>
        </li>
      ))}
    </ul>
  );
}

function Shortcuts() {
  const rows: [string, string][] = [
    ["Ctrl Z", "Undo"],
    ["Ctrl Shift Z", "Redo"],
    ["Ctrl S", "Save"],
    ["Ctrl A", "Select all"],
    ["Ctrl C / V", "Copy / paste nodes"],
    ["Delete", "Remove selection"],
    ["Esc", "Clear selection"],
    ["Shift drag", "Select an area"],
  ];
  return (
    <dl className="shortcuts">
      {rows.map(([k, v]) => (
        <div key={k}>
          <dt>{k.split(" ").map((p) => (p === "/" ? " / " : <kbd key={p}>{p}</kbd>))}</dt>
          <dd>{v}</dd>
        </div>
      ))}
    </dl>
  );
}

/** The "Show technical details" switch (Phase H5). */
function DetailsSwitch() {
  const { show, setShow } = useTechnicalDetails();
  return (
    <label className="switch-row" htmlFor="show-details">
      <input id="show-details" type="checkbox" role="switch" className="switch" checked={show} onChange={(e) => setShow(e.target.checked)} />
      <span className="small">Show technical details (error codes, data types, step IDs)</span>
    </label>
  );
}

export function ConfigPanel() {
  const { catalog } = useEditorContext();
  const issues = useIssues();
  const definition = useEditorStore((s) => s.definition);
  const selectedNodeIds = useEditorStore((s) => s.selectedNodeIds);
  const selectedEdgeIds = useEditorStore((s) => s.selectedEdgeIds);
  const readonly = useEditorStore((s) => s.mode === "readonly");
  const rename = useEditorStore((s) => s.renameNode);
  const setConfig = useEditorStore((s) => s.setConfigValue);
  const removeNodes = useEditorStore((s) => s.removeNodes);
  const removeEdges = useEditorStore((s) => s.removeEdges);
  const details = useShowDetails();

  const node = selectedNodeIds.length === 1 ? definition.nodes.find((n) => n.id === selectedNodeIds[0]) : undefined;
  const edge = !node && selectedEdgeIds.length === 1 ? definition.edges.find((e) => e.id === selectedEdgeIds[0]) : undefined;

  if (node) {
    const def = catalog.get(node.type);
    const known = new Set(def?.config.map((f) => f.name));
    const extra = Object.keys(node.config).filter((k) => !known.has(k));
    const mine = issues.filter((i) => i.nodeId === node.id && !i.edgeId);
    const general = mine.filter((i) => !i.field || !known.has(i.field));
    return (
      <aside className="config-panel" aria-label="Node configuration" data-testid="config-panel">
        <div className="panel-header">
          <span className={`node-icon large ${categoryClass(def?.category ?? "")}`}><NodeIcon def={def ?? { type: node.type, category: "" }} size={22} /></span>
          <div>
            <h2>{def ? friendlyNodeName(def) : node.type}</h2>
            <div className="panel-sub">{node.name}</div>
          </div>
        </div>
        {def?.description && <p className="panel-desc">{def.description}</p>}
        {!def && <div className="error-banner">Node type “{node.type}” is not in the node catalog.</div>}
        <IssueList issues={general} />
        <div className="section">
          <div className="field">
            <label htmlFor="cfg-node-name">Name</label>
            <CommitInput id="cfg-node-name" value={node.name} disabled={readonly} onCommit={(v) => v.trim() && rename(node.id, v.trim())} />
            {details && (
              <div className="hint">
                Other nodes reference this one as <code>{`{{${node.id}.…}}`}</code>
              </div>
            )}
          </div>
        </div>
        {def && def.config.length > 0 && (
          <div className="section">
            <h3 className="section-title">Settings</h3>
            {def.config.map((f) => (
              <FieldEditor key={`${node.id}:${f.name}`} node={node} field={f} issues={mine.filter((i) => i.field === f.name)} />
            ))}
          </div>
        )}
        {def && def.config.length === 0 && <p className="muted small section">This node has no settings.</p>}
        {extra.length > 0 && (
          <div className="section">
            <div className="error-banner small">
              Unknown settings (the backend rejects them):
              <ul className="extra-keys">
                {extra.map((k) => (
                  <li key={k}>
                    <code>{k}</code>
                    {!readonly && <button type="button" className="small-button" onClick={() => setConfig(node.id, k, undefined)}>Remove</button>}
                  </li>
                ))}
              </ul>
            </div>
          </div>
        )}
        {def && <NodeTest nodeId={node.id} />}
        {def && def.role !== "entry" && def.role !== "exit" && <RepeatSettings node={node} def={def} readonly={readonly} />}
        {def && def.role !== "entry" && def.role !== "exit" && <FailureSettings node={node} def={def} readonly={readonly} />}
        {!readonly && (
          <div className="section">
            <button className="danger" onClick={() => removeNodes([node.id])}>Delete node</button>
          </div>
        )}
      </aside>
    );
  }

  if (edge) {
    const src = definition.nodes.find((n) => n.id === edge.source);
    const tgt = definition.nodes.find((n) => n.id === edge.target);
    const out = src && catalog.get(src.type)?.outputs.find((p) => p.name === edge.source_port);
    const inp = tgt && catalog.get(tgt.type)?.inputs.find((p) => p.name === edge.target_port);
    const edgeIssues = issues.filter((i) => i.edgeId === edge.id || (i.code === "MULTIPLE_CONNECTIONS_NOT_ALLOWED" && i.nodeId === edge.target && i.port === edge.target_port));
    return (
      <aside className="config-panel" aria-label="Connection" data-testid="config-panel">
        <div className="panel-header">
          <div>
            <h2>Connection</h2>
            <div className="panel-sub">Data flows from an output into an input.</div>
          </div>
        </div>
        <div className="connection-ends">
          <div>
            <div className="small muted">From</div>
            <b>{src?.name ?? edge.source}</b> · {edge.source_port} {out && <span className="field-type">{out.type}</span>}
          </div>
          <div className="arrow" aria-hidden>↓</div>
          <div>
            <div className="small muted">To</div>
            <b>{tgt?.name ?? edge.target}</b> · {edge.target_port} {inp && <span className="field-type">{inp.type}</span>}
          </div>
        </div>
        <IssueList issues={edgeIssues} />
        {!readonly && (
          <div className="section">
            <button className="danger" onClick={() => removeEdges([edge.id])}>Delete connection</button>
          </div>
        )}
      </aside>
    );
  }

  const errorCount = issues.filter((i) => i.severity === "error").length;
  return (
    <aside className="config-panel" aria-label="Workflow">
      <div className="panel-header">
        <div>
          <h2>Workflow</h2>
          <div className="panel-sub">{definition.nodes.length} step{definition.nodes.length === 1 ? "" : "s"} · {definition.edges.length} connection{definition.edges.length === 1 ? "" : "s"}</div>
        </div>
      </div>
      <p className="panel-desc">
        {selectedNodeIds.length > 1
          ? `${selectedNodeIds.length} steps selected. Press Delete to remove them.`
          : "Click a step to see all its settings. To connect steps, drag from a dot on one card to a dot on another."}
      </p>
      {errorCount > 0 && <p className="small" style={{ color: "var(--danger)" }}>{errorCount} validation problem{errorCount === 1 ? "" : "s"}: see the Validation panel.</p>}
      <div className="section">
        <h3 className="section-title">Variables</h3>
        <VariablesEditor />
      </div>
      <div className="section">
        <h3 className="section-title">Keyboard</h3>
        <Shortcuts />
      </div>
      <div className="section">
        <h3 className="section-title">Display</h3>
        <DetailsSwitch />
      </div>
    </aside>
  );
}
