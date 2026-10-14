"use client";

// Phase C8 setting controls: a picker filled from the node's account, and a
// name -> value editor for object settings (sheet values, Notion
// properties, HTTP query and headers).

import { useState } from "react";
import { VariableField } from "@/components/panels/VariableField";
import type { FieldOptions } from "@/features/nodes/useFieldOptions";
import type { VariableGroup } from "@/lib/workflow/variables";

function asText(v: unknown): string {
  if (v === undefined || v === null) return "";
  return typeof v === "string" ? v : typeof v === "number" || typeof v === "boolean" ? String(v) : JSON.stringify(v);
}

/** A choice list loaded through the account, with typing as a fallback
 * (links, IDs, or anything the list does not show). */
export function DynamicSelect({ id, value, choices, onCommit, disabled, invalid, describedBy, placeholder }: {
  id: string; value: unknown; choices: FieldOptions; onCommit: (v: string | undefined) => void;
  disabled?: boolean; invalid?: boolean; describedBy?: string; placeholder?: string;
}) {
  const text = asText(value);
  const known = choices.options.some((o) => asText(o.value) === text);
  const [typing, setTyping] = useState(false);
  const [draft, setDraft] = useState(text);
  const listMode = !typing && choices.options.length > 0 && (text === "" || known);

  return (
    <div className="dynamic-field">
      {listMode ? (
        <div className="row">
          <select id={id} value={text} disabled={disabled} aria-invalid={invalid || undefined} aria-describedby={describedBy}
            onChange={(e) => onCommit(e.target.value || undefined)}>
            <option value="">Select…</option>
            {choices.options.map((o) => (
              <option key={asText(o.value)} value={asText(o.value)}>{o.label}</option>
            ))}
          </select>
          <button type="button" className="icon-button" title="Reload the list" aria-label="Reload the list" disabled={disabled || choices.loading}
            onClick={choices.reload}>↻</button>
        </div>
      ) : (
        <input id={id} value={typing ? draft : text} disabled={disabled} placeholder={placeholder} aria-invalid={invalid || undefined}
          aria-describedby={describedBy}
          onFocus={() => {
            setDraft(text);
            setTyping(true);
          }}
          onChange={(e) => setDraft(e.target.value)}
          onBlur={() => {
            setTyping(false);
            const v = draft.trim();
            if (v !== text) onCommit(v === "" ? undefined : v);
          }} />
      )}
      <div className="field-actions small">
        {choices.loading && <span className="muted"><span className="spinner" aria-hidden /> Loading choices…</span>}
        {!choices.loading && choices.hint && <span className="muted" data-testid={`${id}-hint`}>{choices.hint}</span>}
        {!listMode && choices.options.length > 0 && !disabled && (
          <button type="button" className="link" onClick={() => onCommit(undefined)}>Choose from the list</button>
        )}
        {listMode && !disabled && (
          <button type="button" className="link" onClick={() => {
            setDraft(text);
            setTyping(true);
          }}>Type a value instead</button>
        )}
      </div>
    </div>
  );
}

type Row = { key: string; value: string; original?: unknown };

function rowsOf(value: unknown): Row[] {
  if (!value || typeof value !== "object" || Array.isArray(value)) return [];
  return Object.entries(value as Record<string, unknown>).map(([key, v]) => ({ key, value: asText(v), original: v }));
}

/** Rows to the stored object: empty names are dropped, later rows win, and
 * an unedited non-text value (a number, a boolean) keeps its type. A new
 * row left empty is not stored (an empty value clears a cell or property;
 * only a stored one is kept). */
export function rowsToObject(rows: Row[]): Record<string, unknown> | undefined {
  const out: Record<string, unknown> = {};
  for (const r of rows) {
    const k = r.key.trim();
    if (!k || (r.value === "" && r.original === undefined)) continue;
    out[k] = r.original !== undefined && typeof r.original !== "string" && asText(r.original) === r.value ? r.original : r.value;
  }
  return Object.keys(out).length ? out : undefined;
}

/** Name -> value rows with {{variable}} values and suggested names. */
export function KeyValueField({ id, value, onCommit, groups, suggestions, disabled, describedBy, keyPlaceholder = "Name" }: {
  id: string; value: unknown; onCommit: (v: Record<string, unknown> | undefined) => void; groups: VariableGroup[];
  suggestions: FieldOptions | null; disabled?: boolean; describedBy?: string; keyPlaceholder?: string;
}) {
  const stored = JSON.stringify(value ?? null);
  const [state, setState] = useState<{ stored: string; rows: Row[] }>(() => ({ stored, rows: rowsOf(value) }));
  // Restart from the stored value when it changes elsewhere (undo, another node).
  const rows = state.stored === stored ? state.rows : rowsOf(value);
  const set = (next: Row[]) => setState({ stored, rows: next });
  const commit = (next: Row[]) => {
    const obj = rowsToObject(next);
    setState({ stored: JSON.stringify(obj ?? null), rows: next });
    if (JSON.stringify(obj ?? null) !== stored) onCommit(obj);
  };
  const listId = `${id}-names`;
  const used = new Set(rows.map((r) => r.key.trim()));
  const unused = suggestions?.options.filter((o) => !used.has(asText(o.value))) ?? [];

  return (
    <div className="kv-field" id={id} aria-describedby={describedBy} data-testid={`${id}-rows`}>
      {suggestions && (
        <datalist id={listId}>
          {suggestions.options.map((o) => <option key={asText(o.value)} value={asText(o.value)}>{o.label}</option>)}
        </datalist>
      )}
      {rows.map((r, i) => (
        <div className="kv-row" key={i}>
          <input aria-label={`${keyPlaceholder} ${i + 1}`} placeholder={keyPlaceholder} value={r.key} disabled={disabled} list={suggestions ? listId : undefined}
            onChange={(e) => set(rows.map((x, j) => (j === i ? { ...x, key: e.target.value } : x)))}
            onBlur={() => commit(rows)} />
          <span className="kv-arrow" aria-hidden>→</span>
          <div className="kv-value">
            <VariableField id={`${id}-value-${i}`} groups={groups} disabled={disabled} placeholder="Value or {{variable}}" value={r.value}
              onCommit={(v) => commit(rows.map((x, j) => (j === i ? { ...x, value: v } : x)))} />
          </div>
          {!disabled && (
            <button type="button" className="icon-button" aria-label={`Remove ${r.key || `row ${i + 1}`}`}
              onClick={() => commit(rows.filter((_, j) => j !== i))}>✕</button>
          )}
        </div>
      ))}
      {!disabled && (
        <div className="field-actions small">
          <button type="button" className="link" onClick={() => set([...rows, { key: "", value: "" }])}>+ Add row</button>
          {unused.length > 0 && (
            <button type="button" className="link" onClick={() => set([...rows, ...unused.map((o) => ({ key: asText(o.value), value: "" }))])}>
              + Add all {unused.length} {unused.length === 1 ? "name" : "names"}
            </button>
          )}
          {suggestions?.loading && <span className="muted"><span className="spinner" aria-hidden /> Loading names…</span>}
          {!suggestions?.loading && suggestions?.hint && <span className="muted" data-testid={`${id}-hint`}>{suggestions.hint}</span>}
        </div>
      )}
    </div>
  );
}
